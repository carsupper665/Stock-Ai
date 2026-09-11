package api

import (
	"backend/internal/account"
	"backend/internal/auth"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
	"backend/internal/market"
	"backend/internal/message"
	"backend/internal/trading"

	"github.com/gin-gonic/gin"
)

// Server 持有 handler 需要的相依。各模組的 handler 都掛在這上面。
type Server struct {
	cfg      *config.Config
	log      *logging.Logger
	store    *database.Store
	auth     *auth.Authenticator
	accounts *account.Service
	market   *market.Runtime
	trading  *trading.Service
	messages *message.Service
}

// New 組出 gin engine：套上共用 middleware，然後掛路由。
func New(cfg *config.Config, store *database.Store, prices *market.Runtime, trader *trading.Service, log *logging.Logger) *gin.Engine {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	s := &Server{
		cfg:      cfg,
		log:      log,
		store:    store,
		auth:     auth.New(cfg.UserToken, cfg.UserName, store),
		accounts: account.New(store),
		market:   prices,
		trading:  trader,
		messages: message.New(store, cfg.UserName),
	}

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(logging.RequestID())
	engine.Use(logging.AccessLog(log))

	registerRoutes(engine, s)
	return engine
}

// registerRoutes 是全專案唯一掛路由的地方。
// 新增端點一律加在這裡，handler 本身放到各自的檔案。
func registerRoutes(engine *gin.Engine, s *Server) {
	v1 := engine.Group("/v1")

	// 健康檢查：不需要身分
	v1.GET("/health", s.health)

	// 帳號管理：只有 USER Token
	admin := v1.Group("/accounts", s.requireUser())
	{
		admin.POST("", s.createAccount)
		admin.GET("", s.listAccounts)
		admin.GET("/:id", s.getAccount)
		admin.PATCH("/:id", s.updateAccount)
		admin.DELETE("/:id", s.deleteAccount)
		admin.POST("/:id/token/reset", s.resetAccountToken)

		admin.GET("/:id/positions", s.userListPositions)
		admin.GET("/:id/orders", s.userListOrders)
		admin.GET("/:id/trades", s.userListTrades)
	}

	// 帳號自查：Account Token 查自己
	v1.GET("/account", s.requireAny(), s.selfAccount)

	// 行情：USER 與 Account 都可以讀
	v1.GET("/market/price", s.requireAny(), s.marketPrice)
	// 訂閱狀態是營運資訊，只給 USER
	v1.GET("/market/subscriptions", s.requireUser(), s.marketSubscriptions)

	// 交易：身分一律由 token 決定，不接受 client 指定帳號（規格 §4）
	trade := v1.Group("", s.requireAny())
	{
		trade.POST("/orders", s.placeOrder)
		trade.GET("/orders", s.listOrders)
		trade.GET("/orders/:id", s.getOrder)
		trade.POST("/orders/:id/cancel", s.cancelOrder)
		trade.GET("/positions", s.listPositions)
		trade.POST("/positions/:id/close", s.closePosition)
		trade.PATCH("/positions/:id", s.setStops)
		trade.GET("/trades", s.listTrades)
	}

	// 留言板：讀取公開，帶了 token 就解析身分、帶錯就 401（規格 §17）；發布需要 token
	v1.GET("/messages", s.optionalAuth(), s.listMessages)
	v1.POST("/messages", s.requireAny(), s.postMessage)
}
