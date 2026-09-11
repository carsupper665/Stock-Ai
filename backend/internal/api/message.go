package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"backend/internal/auth"
	"backend/internal/message"

	"github.com/gin-gonic/gin"
)

type messageView struct {
	ID        string    `json:"id"`
	UserName  string    `json:"user_name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// postMessageRequest 的作者欄位用指標接住，client 一旦傳了就拒絕（規格 §14）。
type postMessageRequest struct {
	Content    string  `json:"content"`
	AuthorID   *string `json:"author_id"`
	AuthorName *string `json:"author_name"`
	AuthorType *string `json:"author_type"`
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

	posted, err := s.messages.Post(c.Request.Context(), identity, req.Content)
	if err != nil {
		s.writeMessageError(c, err)
		return
	}
	c.JSON(http.StatusCreated, messageView{
		ID: posted.ID, UserName: message.You, Content: posted.Content, CreatedAt: posted.CreatedAt.UTC(),
	})
}

func (s *Server) listMessages(c *gin.Context) {
	q := message.Query{Order: c.Query("order")}

	if raw := c.Query("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			fail(c, http.StatusBadRequest, "invalid_request", "limit 必須是正整數")
			return
		}
		q.Limit = limit
	}
	for name, target := range map[string]**time.Time{"after": &q.After, "before": &q.Before} {
		raw := c.Query(name)
		if raw == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_request", name+" 必須是 RFC3339 時間，例如 2026-09-10T17:10:00Z")
			return
		}
		*target = &at
	}

	var requester *auth.Identity
	if identity, ok := identityOf(c); ok {
		requester = &identity
	}

	views, err := s.messages.List(c.Request.Context(), q, requester)
	if err != nil {
		s.writeMessageError(c, err)
		return
	}

	out := make([]messageView, 0, len(views))
	for _, v := range views {
		out = append(out, messageView{ID: v.ID, UserName: v.UserName, Content: v.Content, CreatedAt: v.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"messages": out})
}

func (s *Server) writeMessageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, message.ErrEmptyContent), errors.Is(err, message.ErrContentLong),
		errors.Is(err, message.ErrInvalidOrder):
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.log.Errorf(c.Request.Context(), "留言操作失敗: %v", err)
		fail(c, http.StatusInternalServerError, "internal_error", "留言操作失敗")
	}
}
