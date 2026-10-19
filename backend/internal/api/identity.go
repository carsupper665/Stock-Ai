package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type identityView struct {
	Kind      string `json:"kind"`
	UserName  string `json:"user_name"`
	AccountID string `json:"account_id,omitempty"`
}

func (s *Server) me(c *gin.Context) {
	identity, ok := identityOf(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	c.JSON(http.StatusOK, identityView{
		Kind: string(identity.Kind), UserName: identity.UserName,
		AccountID: identity.AccountID,
	})
}
