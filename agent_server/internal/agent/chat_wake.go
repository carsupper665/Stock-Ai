package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"stock-ai/agent-server/internal/eventloop"
	"stock-ai/agent-server/internal/server"
	"stock-ai/agent-server/internal/tools"
)

const (
	chatWakeInterval = 5 * time.Second
	chatWakeQuiet    = 10 * time.Second
	chatWakeAuthor   = "USER"
	wakeCursor       = "wake_after"
)

func ensureChatWakeColumn(db *sql.DB) error {
	var found int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('session_chat_cursors') WHERE name = 'wake_after'`).Scan(&found); err != nil || found != 0 {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE session_chat_cursors ADD COLUMN wake_after TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE session_chat_cursors SET wake_after = mention_after`)
	return err
}

func (r *AgentRuntime) ChatWakeTask(clock eventloop.Clock) server.BackgroundTask {
	clock = eventClock(clock)
	return server.BackgroundTask{Name: "agent-chat-wake", Interval: chatWakeInterval, Run: func(ctx context.Context) error {
		rows, err := r.db.QueryContext(ctx, `SELECT id, account_id FROM sessions WHERE status = 'running' ORDER BY id`)
		if err != nil {
			return err
		}
		var sessions []Session
		for rows.Next() {
			var session Session
			if err := rows.Scan(&session.ID, &session.AccountID); err != nil {
				_ = rows.Close()
				return err
			}
			sessions = append(sessions, session)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			requestCtx, cancel := context.WithTimeout(ctx, r.config.ToolTimeout)
			account, accountErr := r.fetchAccount(requestCtx, session.AccountID)
			if accountErr != nil || account.UserName == "" || (account.ChatRead != nil && !*account.ChatRead) {
				cancel()
				continue
			}
			page, failure, err := r.fetchAfter(requestCtx, session.ID, wakeCursor, account.Token, url.Values{"tag": {account.UserName}})
			messages := page.Result.Messages
			for err == nil && failure == nil && page.Result.HasMore && len(page.Result.Messages) != 0 {
				after := page.Result.Messages[len(page.Result.Messages)-1].ID
				raw := r.sendMessageRequest(requestCtx, account.Token, http.MethodGet, url.Values{"tag": {account.UserName}, "after": {after}, "page": {"1"}, "sort": {"asc"}}, nil)
				if json.Unmarshal(raw, &page) != nil || !page.OK {
					failure = raw
					break
				}
				if len(page.Result.Messages) != 0 && page.Result.Messages[len(page.Result.Messages)-1].ID == after {
					failure = raw
					break
				}
				messages = append(messages, page.Result.Messages...)
			}
			cancel()
			if err != nil {
				return err
			}
			if failure != nil {
				r.logOperational("chat_wake_error", session.ID, 0, toolErrorCode(failure))
				continue
			}
			if len(messages) == 0 {
				continue
			}
			err = r.dispatchEventRound(ctx, func(tx *sql.Tx) ([]string, error) {
				return r.claimChatWake(tx, session.ID, account, messages, clock.Now())
			})
			if err != nil {
				return err
			}
		}
		return nil
	}}
}

func (r *AgentRuntime) claimChatWake(tx *sql.Tx, sessionID string, account backendAccount, messages []tools.MessageView, now time.Time) ([]string, error) {
	var status, created, delivered string
	err := tx.QueryRow(`SELECT s.status, s.created_at, COALESCE(c.mention_after, '') FROM sessions s
		LEFT JOIN session_chat_cursors c ON c.session_id = s.id WHERE s.id = ?`, sessionID).Scan(&status, &created, &delivered)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "running") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	last := messages[len(messages)-1].ID
	consumed := -1
	for i, message := range messages {
		if message.ID == delivered {
			consumed = i
			break
		}
	}
	_, busy := r.active[sessionID]
	if busy {
		// Only delivered tags may be acknowledged: finalization no longer reads tags.
		if consumed < 0 {
			return nil, nil
		}
		last = delivered
	} else {
		since, _ := time.Parse(time.RFC3339Nano, created)
		if account.ChatReadSince != nil {
			if enabled, err := time.Parse(time.RFC3339Nano, *account.ChatReadSince); err == nil && enabled.After(since) {
				since = enabled
			}
		}
		ids := []string{}
		var latest time.Time
		for _, message := range messages[consumed+1:] {
			at, err := time.Parse(time.RFC3339Nano, message.CreatedAt)
			if err != nil || !at.After(since) || message.UserName != chatWakeAuthor {
				continue
			}
			ids = append(ids, message.ID)
			if at.After(latest) {
				latest = at
			}
		}
		if len(ids) != 0 {
			if now.Sub(latest) < chatWakeQuiet {
				return nil, nil
			}
			payload, _ := json.Marshal(map[string]any{"type": "chat_mention", "message_ids": ids})
			if _, err := tx.Exec(`INSERT INTO pending_triggers (session_id, payload_json, created_at) VALUES (?, ?, ?)`, sessionID, string(payload), now.UTC().Format(time.RFC3339Nano)); err != nil {
				return nil, err
			}
		}
	}
	_, err = tx.Exec(`INSERT INTO session_chat_cursors (session_id, wake_after) VALUES (?, ?)
		ON CONFLICT(session_id) DO UPDATE SET wake_after = excluded.wake_after`, sessionID, last)
	if err != nil || busy {
		return nil, err
	}
	return []string{sessionID}, nil
}
