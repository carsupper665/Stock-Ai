package webharness

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ClaudeSessions owns the bounded Claude Code transport sessions: one working
// directory per conversation, garbage-collected after the configured idle TTL.
// It is the only server state the web-harness path keeps: the Agent Runtime's
// conversation semantics stay fully outside of it.
type ClaudeSessions struct {
	mu         sync.Mutex
	sessions   map[string]*claudeHarnessSession
	executable string
	sessionTTL time.Duration
	gcStop     chan struct{}
	gcDone     chan struct{}
}

// NewClaudeSessions starts the session garbage collector and returns the store.
func NewClaudeSessions(executable string, sessionTTL time.Duration) *ClaudeSessions {
	store := &ClaudeSessions{
		sessions:   make(map[string]*claudeHarnessSession),
		executable: executable,
		sessionTTL: sessionTTL,
		gcStop:     make(chan struct{}),
		gcDone:     make(chan struct{}),
	}
	go store.collect()
	return store
}

// Executable reports the CLI executable the sessions were prepared for.
func (cs *ClaudeSessions) Executable() string { return cs.executable }

// Stop terminates the garbage collector and removes every session directory.
func (cs *ClaudeSessions) Stop() {
	select {
	case <-cs.gcStop:
	default:
		close(cs.gcStop)
	}
	<-cs.gcDone
	cs.RemoveAll()
}

// Acquire returns the live session for key, creating it on first use, and waits
// for the single in-flight CLI call to finish before handing it out.
func (cs *ClaudeSessions) Acquire(ctx context.Context, key, credential string, loginSnapshot []byte, request WebHarnessRequest) (*claudeHarnessSession, error) {
	for {
		cs.mu.Lock()
		session := cs.sessions[key]
		if session != nil && session.retired {
			cs.mu.Unlock()
			return nil, ErrWebHarnessUnavailable
		}
		if session == nil {
			var err error
			session, err = cs.newSession(key, credential, loginSnapshot, request)
			if err != nil {
				cs.mu.Unlock()
				return nil, err
			}
			cs.sessions[key] = session
		}
		session.active++
		cs.mu.Unlock()
		select {
		case <-ctx.Done():
			cs.mu.Lock()
			session.active--
			remove := session.retired && session.active == 0
			if remove && cs.sessions[key] == session {
				delete(cs.sessions, key)
			}
			cs.mu.Unlock()
			if remove {
				_ = os.RemoveAll(session.workingDirectory)
			}
			return nil, ctx.Err()
		case <-session.gate:
		}
		cs.mu.Lock()
		current := cs.sessions[key] == session && !session.retired
		removed := cs.sessions[key] == session && session.retired
		cs.mu.Unlock()
		if current {
			return session, nil
		}
		cs.Release(session)
		if removed {
			return nil, ErrWebHarnessUnavailable
		}
	}
}

// ReserveReplacement atomically swaps in a fresh cold session for current while
// keeping the working directory UUID reserved, so a retry never reuses a UUID a
// concurrent waiter is still initializing.
func (cs *ClaudeSessions) ReserveReplacement(ctx context.Context, current *claudeHarnessSession, credential string, loginSnapshot []byte, request WebHarnessRequest) (*claudeHarnessSession, error) {
	if err := ctx.Err(); err != nil {
		cs.Retire(current)
		return nil, err
	}
	cs.mu.Lock()
	if cs.sessions[current.key] != current || current.retired {
		cs.mu.Unlock()
		return nil, ErrWebHarnessUnavailable
	}
	replacement, err := cs.newSession(current.key, credential, loginSnapshot, request)
	if err != nil {
		delete(cs.sessions, current.key)
		current.retired = true
		cs.mu.Unlock()
		return nil, err
	}
	<-replacement.gate
	replacement.active = 1
	current.retired = true
	cs.sessions[current.key] = replacement
	cs.mu.Unlock()
	return replacement, nil
}

func (cs *ClaudeSessions) newSession(key, credential string, loginSnapshot []byte, request WebHarnessRequest) (*claudeHarnessSession, error) {
	workingDirectory, err := prepareHarnessWithLogin(cs.executable, credential, len(loginSnapshot) != 0, request)
	if err != nil {
		return nil, err
	}
	if len(loginSnapshot) != 0 {
		if err := os.WriteFile(filepath.Join(workingDirectory, ".credentials.json"), loginSnapshot, 0o600); err != nil {
			_ = os.RemoveAll(workingDirectory)
			return nil, ErrWebHarnessUnavailable
		}
	}
	sessionID, err := newHarnessSessionID()
	if err != nil {
		_ = os.RemoveAll(workingDirectory)
		return nil, ErrWebHarnessUnavailable
	}
	session := &claudeHarnessSession{key: key, workingDirectory: workingDirectory, sessionID: sessionID, gate: make(chan struct{}, 1), lastUsed: time.Now()}
	session.gate <- struct{}{}
	return session, nil
}

// Release returns the single in-flight slot and removes the directory once the
// session is retired and no waiters remain.
func (cs *ClaudeSessions) Release(session *claudeHarnessSession) {
	cs.mu.Lock()
	session.active--
	session.lastUsed = time.Now()
	remove := session.retired && session.active == 0
	if remove && cs.sessions[session.key] == session {
		delete(cs.sessions, session.key)
	}
	cs.mu.Unlock()
	session.gate <- struct{}{}
	if remove {
		_ = os.RemoveAll(session.workingDirectory)
	}
}

// Retire marks the session as replaced so it is no longer handed out.
func (cs *ClaudeSessions) Retire(session *claudeHarnessSession) {
	cs.mu.Lock()
	if cs.sessions[session.key] == session {
		delete(cs.sessions, session.key)
	}
	session.retired = true
	cs.mu.Unlock()
}

// collect removes idle sessions once they exceed the TTL.
func (cs *ClaudeSessions) collect() {
	interval := time.Minute
	if cs.sessionTTL < interval {
		interval = cs.sessionTTL
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(cs.gcDone)
	for {
		select {
		case <-cs.gcStop:
			return
		case now := <-ticker.C:
			var directories []string
			cs.mu.Lock()
			for key, session := range cs.sessions {
				if session.active == 0 && now.Sub(session.lastUsed) >= cs.sessionTTL {
					delete(cs.sessions, key)
					session.retired = true
					directories = append(directories, session.workingDirectory)
				}
			}
			cs.mu.Unlock()
			for _, directory := range directories {
				_ = os.RemoveAll(directory)
			}
		}
	}
}

// RemoveAll retires and deletes every session.
func (cs *ClaudeSessions) RemoveAll() {
	cs.mu.Lock()
	directories := make([]string, 0, len(cs.sessions))
	for key, session := range cs.sessions {
		delete(cs.sessions, key)
		session.retired = true
		directories = append(directories, session.workingDirectory)
	}
	cs.mu.Unlock()
	for _, directory := range directories {
		_ = os.RemoveAll(directory)
	}
}

// RemoveConversation retires the Session's Claude transports and its compaction transports.
// In-flight directories survive until Release, so a canceled CLI can finish exiting safely.
func (cs *ClaudeSessions) RemoveConversation(conversationID string) error {
	cs.mu.Lock()
	var directories []string
	for key, session := range cs.sessions {
		if !strings.HasPrefix(key, conversationID+"\x00") && !strings.HasPrefix(key, conversationID+"/") {
			continue
		}
		session.retired = true
		if session.active == 0 {
			delete(cs.sessions, key)
			directories = append(directories, session.workingDirectory)
		}
	}
	cs.mu.Unlock()
	var result error
	for _, directory := range directories {
		result = errors.Join(result, os.RemoveAll(directory))
	}
	return result
}

// GenerateClaudeSession drives one bounded Claude Code transport session for the
// request: it resumes the session while the conversation prefix still matches and
// falls back to a fresh cold session when it cannot.
func GenerateClaudeSession(ctx context.Context, sessions *ClaudeSessions, key, credential string, loginSnapshot []byte, effort string, request WebHarnessRequest, continuation *Continuation) (WebHarnessResponse, error) {
	session, err := sessions.Acquire(ctx, key, credential, loginSnapshot, request)
	if err != nil {
		return WebHarnessResponse{}, err
	}
	fallbackReason := ""
	resumeMessages := []WebHarnessMessage(nil)
	_, directoryErr := os.Stat(session.workingDirectory)
	replace := false
	if directoryErr != nil {
		fallbackReason = "session_directory_missing"
		replace = true
	} else if len(session.delivered) == 0 {
		if continuation != nil {
			fallbackReason = "continuation_unavailable"
		}
	} else if continuation != nil {
		if validClaudeContinuation(session, request.Messages, continuation) {
			resumeMessages = continuation.Messages
		} else {
			fallbackReason = "continuation_invalid"
			replace = true
		}
	} else {
		validPrefix := messagePrefix(request.Messages, session.delivered)
		switch {
		case !validPrefix:
			fallbackReason = "history_prefix_changed"
			replace = true
		case len(request.Messages) == len(session.delivered):
			fallbackReason = "no_new_messages"
			replace = true
		default:
			resumeMessages = request.Messages[len(session.delivered):]
		}
	}
	if fallbackReason != "" {
		log.Printf("web_harness event=fallback harness=claude_code reason=%s", fallbackReason)
	}
	if replace {
		fresh, replaceErr := sessions.ReserveReplacement(ctx, session, credential, loginSnapshot, request)
		sessions.Release(session)
		if replaceErr != nil {
			return WebHarnessResponse{}, replaceErr
		}
		session = fresh
	}
	resume := len(resumeMessages) != 0
	response, invokeErr := (ClaudeCodeWebHarness{Executable: sessions.Executable(), Effort: effort}).generateInDirectory(
		ctx, credential, request, session.workingDirectory, session.sessionID, resume, resumeMessages)
	if invokeErr == nil {
		acknowledgeClaudeSession(session, request.Messages, response)
		response.SessionMode = "cold"
		if resume {
			response.SessionMode = "resume"
		}
		response.FallbackReason = fallbackReason
		response.InvocationCount = 1
		sessions.Release(session)
		return response, nil
	}
	if !resume || ctx.Err() != nil {
		sessions.Retire(session)
		sessions.Release(session)
		if ctx.Err() != nil {
			return WebHarnessResponse{}, ctx.Err()
		}
		return WebHarnessResponse{}, invokeErr
	}
	// A persisted CLI session can become unusable independently of the request.
	// Reserve its one cold retry before releasing waiters so the UUID cannot be initialized twice.
	log.Printf("web_harness event=fallback harness=claude_code reason=resume_failed category=%s", HarnessErrorCategory(invokeErr))
	cold, replaceErr := sessions.ReserveReplacement(ctx, session, credential, loginSnapshot, request)
	sessions.Release(session)
	if replaceErr != nil {
		return WebHarnessResponse{}, replaceErr
	}
	response, coldErr := (ClaudeCodeWebHarness{Executable: sessions.Executable(), Effort: effort}).generateInDirectory(
		ctx, credential, request, cold.workingDirectory, cold.sessionID, false, nil)
	if coldErr == nil {
		acknowledgeClaudeSession(cold, request.Messages, response)
		response.SessionMode = "cold"
		response.FallbackReason = "resume_failed"
		response.InvocationCount = 2
		sessions.Release(cold)
		return response, nil
	}
	sessions.Retire(cold)
	sessions.Release(cold)
	if ctx.Err() != nil {
		return WebHarnessResponse{}, ctx.Err()
	}
	return WebHarnessResponse{}, coldErr
}
