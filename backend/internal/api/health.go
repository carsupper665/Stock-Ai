package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// health 回報服務與資料庫是否可用。
func (s *Server) health(c *gin.Context) {
	if err := s.store.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "database": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "database": s.cfg.DBDriver})
}
