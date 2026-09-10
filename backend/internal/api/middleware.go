package api

import (
	"errors"
	"net/http"

	"backend/internal/auth"

	"github.com/gin-gonic/gin"
)

// identityKey 是身分在 gin context 中的鍵。
const identityKey = "identity"

// requireUser 只放行 USER Token。
// 沒帶或無效的 token 回 401；帶了合法的 Account Token 回 403。
func (s *Server) requireUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := s.authenticate(c, false)
		if !ok {
			return
		}
		if !id.IsUser() {
			fail(c, http.StatusForbidden, "forbidden", "這個端點只有 USER Token 可以使用")
			return
		}
		c.Next()
	}
}

// requireAny 放行 USER 或 Account Token，但一定要有有效身分。
func (s *Server) requireAny() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := s.authenticate(c, false); !ok {
			return
		}
		c.Next()
	}
}

// optionalAuth 用於公開讀取但可帶身分的端點（規格 §17）：
// 沒帶 token 以匿名身分放行，帶了但無效必須回 401，
// 不可以默默當成匿名，否則呼叫端帶錯 token 自己不會知道。
func (s *Server) optionalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := s.authenticate(c, true); !ok {
			return
		}
		c.Next()
	}
}

// authenticate 解析身分並存進 context。
// allowAnonymous 為真時，未提供 token 視為匿名而非錯誤。
// 回傳的 bool 表示是否應該繼續處理請求。
func (s *Server) authenticate(c *gin.Context, allowAnonymous bool) (auth.Identity, bool) {
	raw := auth.BearerToken(c.GetHeader("Authorization"))

	id, err := s.auth.Resolve(c.Request.Context(), raw)
	switch {
	case err == nil:
		c.Set(identityKey, id)
		return id, true

	case errors.Is(err, auth.ErrNoToken):
		if allowAnonymous {
			return auth.Identity{}, true
		}
		fail(c, http.StatusUnauthorized, "unauthorized", "請以 Authorization: Bearer <token> 提供 token")
		return auth.Identity{}, false

	case errors.Is(err, auth.ErrInvalidToken):
		fail(c, http.StatusUnauthorized, "unauthorized", "token 無效或帳號已停用")
		return auth.Identity{}, false

	default:
		s.log.Errorf(c.Request.Context(), "身分驗證失敗: %v", err)
		fail(c, http.StatusInternalServerError, "internal_error", "身分驗證失敗")
		return auth.Identity{}, false
	}
}

// identityOf 取出目前請求的身分。沒有身分時回傳的 Identity 為零值，
// ok 為 false（只會發生在 optionalAuth 的匿名請求）。
func identityOf(c *gin.Context) (auth.Identity, bool) {
	value, exists := c.Get(identityKey)
	if !exists {
		return auth.Identity{}, false
	}
	id, ok := value.(auth.Identity)
	return id, ok
}
