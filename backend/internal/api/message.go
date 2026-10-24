package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backend/internal/auth"
	"backend/internal/database"
	"backend/internal/message"

	"github.com/gin-gonic/gin"
)

type messageView struct {
	ID        string    `json:"id"`
	UserName  string    `json:"user_name"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

// ssePingInterval 讓代理與瀏覽器不會把閒置的推播連線當成斷線。
const ssePingInterval = 25 * time.Second

type postMessageRequest struct {
	Content    string   `json:"content"`
	Tags       []string `json:"tags"`
	AuthorID   *string  `json:"author_id"`
	AuthorName *string  `json:"author_name"`
	AuthorType *string  `json:"author_type"`
}

func (s *Server) postMessage(c *gin.Context) {
	identity, ok := identityOf(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "unauthorized", "發布留言需要 token")
		return
	}

	var req postMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
		return
	}
	if req.AuthorID != nil || req.AuthorName != nil || req.AuthorType != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "作者由 token 決定，不接受 author_id、author_name、author_type")
		return
	}

	posted, err := s.messages.Post(c.Request.Context(), identity, req.Content, req.Tags)
	if err != nil {
		s.writeMessageError(c, err)
		return
	}
	c.JSON(http.StatusCreated, messageView{
		ID: posted.ID, UserName: message.You, Content: posted.Content, Tags: posted.Tags, CreatedAt: posted.CreatedAt.UTC(),
	})
}

func (s *Server) listMessages(c *gin.Context) {
	for key, values := range c.Request.URL.Query() {
		if (key != "page" && key != "limit" && key != "sort" && key != "tag" && key != "after" && key != "before") || len(values) != 1 {
			fail(c, http.StatusBadRequest, "invalid_request", "只接受 page、limit、sort、tag、after、before 查詢參數")
			return
		}
	}

	q := message.Query{Page: 1, Sort: c.Query("sort"), Tag: c.Query("tag"), After: c.Query("after"), Before: c.Query("before")}
	if raw, present := c.GetQuery("page"); present {
		page, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || page <= 0 {
			fail(c, http.StatusBadRequest, "invalid_request", "page 必須是正整數")
			return
		}
		q.Page = page
	}
	if raw, present := c.GetQuery("limit"); present {
		limit, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || limit <= 0 {
			fail(c, http.StatusBadRequest, "invalid_request", message.ErrInvalidLimit.Error())
			return
		}
		q.Limit = limit
	}

	var requester *auth.Identity
	if identity, ok := identityOf(c); ok {
		requester = &identity
	}
	views, hasMore, err := s.messages.List(c.Request.Context(), q, requester)
	if err != nil {
		s.writeMessageError(c, err)
		return
	}

	out := make([]messageView, 0, len(views))
	for _, view := range views {
		out = append(out, toMessageView(view))
	}
	c.JSON(http.StatusOK, gin.H{"messages": out, "page": q.Page, "has_more": hasMore})
}

func (s *Server) deleteMessage(c *gin.Context) {
	identity, _ := identityOf(c)
	if err := s.messages.Delete(c.Request.Context(), c.Param("id"), identity); err != nil {
		s.writeMessageError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) clearMessages(c *gin.Context) {
	if err := s.messages.Clear(c.Request.Context()); err != nil {
		s.writeMessageError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// streamMessages 是只給 USER 前端的 SSE 推播；第三方用 after 輪詢。
func (s *Server) streamMessages(c *gin.Context) {
	events, unsubscribe := s.messages.Subscribe()
	defer unsubscribe()

	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := writeEvent(c, event); err != nil {
				return
			}
		}
		c.Writer.Flush()
	}
}

func writeEvent(c *gin.Context, event message.Event) error {
	var data any = gin.H{}
	switch event.Name {
	case message.EventCreated:
		data = toMessageView(event.Message)
	case message.EventDeleted:
		data = gin.H{"id": event.ID}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event.Name, raw)
	return err
}

func toMessageView(view message.View) messageView {
	return messageView{ID: view.ID, UserName: view.UserName, Content: view.Content, Tags: view.Tags, CreatedAt: view.CreatedAt}
}

func (s *Server) writeMessageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, message.ErrEmptyContent), errors.Is(err, message.ErrContentLong),
		errors.Is(err, message.ErrInvalidSort), errors.Is(err, message.ErrInvalidPage),
		errors.Is(err, message.ErrInvalidTag), errors.Is(err, message.ErrInvalidTags),
		errors.Is(err, message.ErrUnknownTag), errors.Is(err, message.ErrSelfTag), errors.Is(err, message.ErrInvalidAfter),
		errors.Is(err, message.ErrInvalidLimit), errors.Is(err, message.ErrInvalidBefore):
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, message.ErrChatPostDisabled):
		fail(c, http.StatusForbidden, "chat_post_disabled", err.Error())
	case errors.Is(err, message.ErrChatReadDisabled):
		fail(c, http.StatusForbidden, "chat_read_disabled", err.Error())
	case errors.Is(err, message.ErrChatMuted):
		fail(c, http.StatusForbidden, "chat_muted", err.Error())
	case errors.Is(err, message.ErrChatRateLimited):
		fail(c, http.StatusTooManyRequests, "chat_rate_limited", err.Error())
	case errors.Is(err, message.ErrForbidden):
		fail(c, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, database.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "找不到留言")
	default:
		s.log.Errorf(c.Request.Context(), "留言操作失敗: %v", err)
		fail(c, http.StatusInternalServerError, "internal_error", "留言操作失敗")
	}
}
