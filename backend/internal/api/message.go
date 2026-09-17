package api

import (
	"errors"
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
		if (key != "page" && key != "sort" && key != "tag") || len(values) != 1 {
			fail(c, http.StatusBadRequest, "invalid_request", "只接受 page、sort、tag 查詢參數")
			return
		}
	}

	q := message.Query{Page: 1, Sort: c.Query("sort"), Tag: c.Query("tag")}
	if raw, present := c.GetQuery("page"); present {
		page, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || page <= 0 {
			fail(c, http.StatusBadRequest, "invalid_request", "page 必須是正整數")
			return
		}
		q.Page = page
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
		out = append(out, messageView{ID: view.ID, UserName: view.UserName, Content: view.Content, Tags: view.Tags, CreatedAt: view.CreatedAt})
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

func (s *Server) writeMessageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, message.ErrEmptyContent), errors.Is(err, message.ErrContentLong),
		errors.Is(err, message.ErrInvalidSort), errors.Is(err, message.ErrInvalidPage),
		errors.Is(err, message.ErrInvalidTag), errors.Is(err, message.ErrInvalidTags):
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, message.ErrForbidden):
		fail(c, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, database.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "找不到留言")
	default:
		s.log.Errorf(c.Request.Context(), "留言操作失敗: %v", err)
		fail(c, http.StatusInternalServerError, "internal_error", "留言操作失敗")
	}
}
