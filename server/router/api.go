package router

import (
	"net/http"

	"server/controller"
	"server/middleware"
	"server/service"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func New(app *service.App) *gin.Engine {
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(middleware.RequestId())
	middleware.SetUpLogger(engine)
	engine.Use(middleware.CORS())

	store := cookie.NewStore([]byte(app.Config.SessionSecret))
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   2592000,
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})
	engine.Use(sessions.Sessions("session", store))

	handler := controller.NewHandler(app)
	register(engine, handler)
	return engine
}

func register(engine *gin.Engine, h *controller.Handler) {
	engine.GET("/healthz", h.Health)
	engine.POST("/admin/login", h.AdminLogin)
	engine.POST("/auth/token/login", h.TokenLogin)

	admin := engine.Group("/admin")
	admin.Use(h.AdminRequired())
	{
		admin.POST("/replay-datasets", h.CreateReplayDataset)
		admin.GET("/replay-datasets", h.ListReplayDatasets)
		admin.GET("/replay-datasets/:id", h.GetReplayDataset)
		admin.POST("/replay-datasets/:id/import", h.ImportReplayDataset)
		admin.POST("/live-accounts", h.CreateLiveAccount)
		admin.GET("/live-accounts", h.ListLiveAccounts)
		admin.GET("/live-accounts/:id", h.GetLiveAccount)
		admin.PATCH("/live-accounts/:id", h.UpdateLiveAccount)
		admin.DELETE("/live-accounts/:id", h.DeleteLiveAccount)
		admin.POST("/sandboxes", h.CreateSandbox)
		admin.GET("/sandboxes", h.ListSandboxes)
		admin.GET("/sandboxes/:id", h.GetSandbox)
		admin.PATCH("/sandboxes/:id", h.UpdateSandbox)
		admin.DELETE("/sandboxes/:id", h.DeleteSandbox)
		admin.POST("/sandboxes/:id/start", h.StartSandbox)
		admin.POST("/sandboxes/:id/pause", h.PauseSandbox)
		admin.POST("/sandboxes/:id/stop", h.StopSandbox)
		admin.POST("/sandboxes/:id/replay/seek", h.SeekSandboxReplay)
		admin.POST("/sandboxes/:id/replay/speed", h.SetSandboxReplaySpeed)
		admin.POST("/sandboxes/:id/replay/resume", h.ResumeSandboxReplay)
		admin.POST("/sandboxes/:id/accounts", h.CreateSandboxAccount)
		admin.GET("/sandboxes/:id/accounts", h.ListSandboxAccounts)
		admin.GET("/accounts/:id", h.GetAdminAccount)
		admin.PATCH("/accounts/:id", h.UpdateAccount)
		admin.DELETE("/accounts/:id", h.DeleteAccount)
		admin.GET("/monitor/sandboxes/:id/snapshot", h.SandboxSnapshot)
		admin.GET("/monitor/live-symbols", h.LiveSymbolsSnapshot)

		// Admin trading console (T3)
		admin.GET("/accounts", h.AdminListAccounts)
		admin.POST("/accounts/:id/orders", h.AdminPlaceOrder)
		admin.GET("/accounts/:id/orders", h.AdminListOrders)
		admin.GET("/accounts/:id/orders/:oid", h.AdminGetOrder)
		admin.POST("/accounts/:id/orders/:oid/cancel", h.AdminCancelOrder)
		admin.GET("/accounts/:id/trades", h.AdminListTrades)
		admin.GET("/accounts/:id/positions", h.AdminListPositions)
		admin.GET("/accounts/:id/summary", h.AdminGetAccountSummary)
		admin.GET("/accounts/:id/tokens", h.AdminListTokens)
		admin.POST("/accounts/:id/tokens", h.AdminCreateToken)
		admin.DELETE("/accounts/:id/tokens/:tid", h.AdminRevokeToken)

		// Admin live-market data (T4)
		admin.GET("/live/price", h.AdminLivePrice)
		admin.GET("/live/klines", h.AdminLiveKlines)
		admin.GET("/live/indicators", h.AdminLiveIndicators)
	}

	engine.POST("/tokens", h.AdminRequired(), h.CreateToken)
	engine.POST("/tokens/:id/rotate", h.AdminRequired(), h.RotateToken)
	engine.DELETE("/tokens/:id", h.AdminRequired(), h.RevokeToken)
	engine.GET("/ws/admin/monitor", h.AdminRequired(), h.AdminMonitorWS)

	engine.GET("/market/price", h.AgentRequired("market:read"), h.MarketPrice)
	engine.GET("/market/ticker", h.AgentRequired("market:read"), h.MarketTicker)
	engine.GET("/market/klines", h.AgentRequired("market:read"), h.MarketKlines)
	engine.GET("/sandbox/time", h.AgentRequired("market:read"), h.SandboxTime)

	engine.POST("/orders", h.AgentRequired("trade:write"), h.CreateOrder)
	engine.GET("/orders", h.AgentRequired("trade:read"), h.ListOrders)
	engine.GET("/orders/:id", h.AgentRequired("trade:read"), h.GetOrder)
	engine.POST("/orders/:id/cancel", h.AgentRequired("trade:write"), h.CancelOrder)
	engine.GET("/trades", h.AgentRequired("trade:read"), h.ListTrades)
	engine.GET("/positions", h.AgentRequired("trade:read"), h.ListPositions)
	engine.GET("/account", h.AgentRequired("account:read"), h.GetAccount)
	engine.GET("/account/performance", h.AgentRequired("account:read"), h.GetAccountPerformance)
	engine.GET("/ws/account", h.AgentRequired("account:read"), h.AccountWS)
}
