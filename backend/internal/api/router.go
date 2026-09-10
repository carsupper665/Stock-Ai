package api

import (
	"backend/internal/config"
	"backend/internal/logging"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Server 持有 handler 需要的相依。各模組的 handler 都掛在這上面。
type Server struct {
	cfg *config.Config
	db  *gorm.DB
	log *logging.Logger
}

// New 組出 gin engine：套上共用 middleware，然後掛路由。
func New(cfg *config.Config, db *gorm.DB, log *logging.Logger) *gin.Engine {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(logging.RequestID())
	engine.Use(logging.AccessLog(log))

	registerRoutes(engine, &Server{cfg: cfg, db: db, log: log})
	return engine
}

// registerRoutes 是全專案唯一掛路由的地方。
// 新增端點一律加在這裡，handler 本身放到各自的檔案。
func registerRoutes(engine *gin.Engine, s *Server) {
	v1 := engine.Group("/v1")

	// 健康檢查
	v1.GET("/health", s.health)
}
