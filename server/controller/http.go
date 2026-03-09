package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"server/domain"
	"server/service"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	ctxAccountID = "account_id"
	ctxTokenID   = "token_id"
	ctxAdminID   = "admin_id"
)

type Handler struct {
	App      *service.App
	Upgrader websocket.Upgrader
}

func NewHandler(app *service.App) *Handler {
	return &Handler{
		App: app,
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (h *Handler) AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		userID := session.Get("user_id")
		if userID == nil {
			writeError(c, domain.UnauthorizedError("admin login required"))
			return
		}
		c.Set(ctxAdminID, userID)
		c.Next()
	}
}

func (h *Handler) AgentRequired(requiredScopes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if rawToken == "" {
			writeError(c, domain.UnauthorizedError("missing bearer token"))
			return
		}
		token, account, err := h.App.Tokens.Verify(c.Request.Context(), rawToken, requiredScopes)
		if err != nil {
			writeError(c, err)
			return
		}
		c.Set(ctxAccountID, account.ID)
		c.Set(ctxTokenID, token.ID)
		c.Next()
	}
}

func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) AdminLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid login payload"))
		return
	}
	user, err := h.App.Auth.LoginAdmin(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		writeError(c, err)
		return
	}
	session := sessions.Default(c)
	session.Set("user_id", user.ID)
	session.Set("role", user.Role)
	_ = session.Save()
	c.JSON(http.StatusOK, gin.H{"id": user.ID, "username": user.Username, "role": user.Role})
}

func (h *Handler) TokenLogin(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid token login payload"))
		return
	}
	token, account, err := h.App.Auth.LoginToken(c.Request.Context(), req.Token)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"token_id": token.ID, "account_id": account.ID, "sandbox_id": account.SandboxID, "scopes": token.Scopes()})
}

func (h *Handler) CreateReplayDataset(c *gin.Context) {
	var req service.CreateDatasetInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid replay dataset payload"))
		return
	}
	dataset, err := h.App.Datasets.Create(c.Request.Context(), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, dataset)
}

func (h *Handler) ListReplayDatasets(c *gin.Context) {
	datasets, err := h.App.Datasets.List(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": datasets})
}

func (h *Handler) GetReplayDataset(c *gin.Context) {
	dataset, err := h.App.Datasets.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, dataset)
}

func (h *Handler) ImportReplayDataset(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "dataset file is required"))
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeError(c, domain.NewError(http.StatusBadRequest, "INVALID_DATASET_FILE", "unable to open dataset file", nil))
		return
	}
	defer opened.Close()

	content, err := io.ReadAll(opened)
	if err != nil {
		writeError(c, domain.NewError(http.StatusBadRequest, "INVALID_DATASET_FILE", "unable to read dataset file", nil))
		return
	}
	job, err := h.App.Datasets.ImportCSV(c.Request.Context(), c.Param("id"), file.Filename, content)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, job)
}

func (h *Handler) CreateSandbox(c *gin.Context) {
	var req service.CreateSandboxInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid sandbox payload"))
		return
	}
	sandbox, err := h.App.Sandboxes.Create(c.Request.Context(), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, sandbox)
}

func (h *Handler) ListSandboxes(c *gin.Context) {
	sandboxes, err := h.App.Sandboxes.List(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": sandboxes})
}

func (h *Handler) GetSandbox(c *gin.Context) {
	sandbox, err := h.App.Sandboxes.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, sandbox)
}

func (h *Handler) UpdateSandbox(c *gin.Context) {
	var req service.UpdateSandboxInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid sandbox payload"))
		return
	}
	sandbox, err := h.App.Sandboxes.Update(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, sandbox)
}

func (h *Handler) StartSandbox(c *gin.Context) {
	sandbox, err := h.App.Sandboxes.Start(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, sandbox)
}

func (h *Handler) SeekSandboxReplay(c *gin.Context) {
	var req struct {
		ReplayCurrentTime time.Time `json:"replay_current_time"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid replay seek payload"))
		return
	}
	status, err := h.App.Sandboxes.Seek(c.Request.Context(), c.Param("id"), req.ReplayCurrentTime)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (h *Handler) SetSandboxReplaySpeed(c *gin.Context) {
	var req struct {
		ReplaySpeed float64 `json:"replay_speed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid replay speed payload"))
		return
	}
	status, err := h.App.Sandboxes.SetReplaySpeed(c.Request.Context(), c.Param("id"), req.ReplaySpeed)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func (h *Handler) ResumeSandboxReplay(c *gin.Context) {
	status, err := h.App.Sandboxes.Resume(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}
func (h *Handler) PauseSandbox(c *gin.Context) {
	sandbox, err := h.App.Sandboxes.Pause(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, sandbox)
}

func (h *Handler) StopSandbox(c *gin.Context) {
	sandbox, err := h.App.Sandboxes.Stop(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, sandbox)
}

func (h *Handler) DeleteSandbox(c *gin.Context) {
	if err := h.App.Sandboxes.Delete(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) CreateSandboxAccount(c *gin.Context) {
	var req struct {
		Name           string  `json:"name"`
		InitialBalance float64 `json:"initial_balance"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid account payload"))
		return
	}
	account, err := h.App.Accounts.Create(c.Request.Context(), service.CreateAccountInput{SandboxID: c.Param("id"), Name: req.Name, InitialBalance: req.InitialBalance})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, account)
}

func (h *Handler) ListSandboxAccounts(c *gin.Context) {
	accounts, err := h.App.Accounts.ListBySandbox(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": accounts})
}

func (h *Handler) CreateLiveAccount(c *gin.Context) {
	var req service.CreateAccountInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid live account payload"))
		return
	}
	req.Type = "live"
	account, err := h.App.Accounts.Create(c.Request.Context(), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, account)
}

func (h *Handler) ListLiveAccounts(c *gin.Context) {
	accounts, err := h.App.Accounts.ListLive(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": accounts})
}

func (h *Handler) GetLiveAccount(c *gin.Context) {
	account, err := h.App.Accounts.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	if account.Type != "live" {
		writeError(c, domain.NotFoundError("LIVE_ACCOUNT_NOT_FOUND", "live account not found"))
		return
	}
	c.JSON(http.StatusOK, account)
}

func (h *Handler) UpdateLiveAccount(c *gin.Context) {
	account, err := h.App.Accounts.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	if account.Type != "live" {
		writeError(c, domain.NotFoundError("LIVE_ACCOUNT_NOT_FOUND", "live account not found"))
		return
	}
	var req service.UpdateAccountInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid live account payload"))
		return
	}
	updated, err := h.App.Accounts.Update(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *Handler) DeleteLiveAccount(c *gin.Context) {
	account, err := h.App.Accounts.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	if account.Type != "live" {
		writeError(c, domain.NotFoundError("LIVE_ACCOUNT_NOT_FOUND", "live account not found"))
		return
	}
	if err := h.App.Accounts.Delete(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *Handler) GetAdminAccount(c *gin.Context) {
	account, err := h.App.Accounts.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, account)
}

func (h *Handler) UpdateAccount(c *gin.Context) {
	var req service.UpdateAccountInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid account payload"))
		return
	}
	account, err := h.App.Accounts.Update(c.Request.Context(), c.Param("id"), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, account)
}

func (h *Handler) DeleteAccount(c *gin.Context) {
	if err := h.App.Accounts.Delete(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) CreateToken(c *gin.Context) {
	var req service.CreateTokenInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid token payload"))
		return
	}
	plaintext, token, err := h.App.Tokens.Create(c.Request.Context(), req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": token.ID, "account_id": token.AccountID, "scopes": token.Scopes(), "token": plaintext})
}

func (h *Handler) RotateToken(c *gin.Context) {
	plaintext, token, err := h.App.Tokens.Rotate(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": token.ID, "account_id": token.AccountID, "scopes": token.Scopes(), "token": plaintext})
}

func (h *Handler) RevokeToken(c *gin.Context) {
	if err := h.App.Tokens.Revoke(c.Request.Context(), c.Param("id")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) MarketPrice(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	account, err := h.App.Accounts.Get(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	if account.Type == "live" {
		ticker, err := h.App.LiveMarket.GetTicker(c.Request.Context(), "", c.Query("symbol"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, ticker)
		return
	}
	if account.SandboxID == nil {
		writeError(c, domain.ValidationError("ACCOUNT_SANDBOX_REQUIRED", "account is not bound to sandbox"))
		return
	}
	if err := h.App.Trading.ProcessSandbox(c.Request.Context(), *account.SandboxID); err != nil && !isNotFound(err) {
		writeError(c, err)
		return
	}
	ticker, err := h.App.Market.GetTicker(c.Request.Context(), *account.SandboxID, c.Query("symbol"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, ticker)
}

func (h *Handler) MarketTicker(c *gin.Context) {
	h.MarketPrice(c)
}

func (h *Handler) MarketKlines(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	account, err := h.App.Accounts.Get(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	from, err := time.Parse(time.RFC3339, c.Query("from"))
	if err != nil {
		from = time.Now().Add(-time.Hour)
	}
	to, err := time.Parse(time.RFC3339, c.Query("to"))
	if err != nil {
		to = time.Now()
	}
	if account.Type == "live" {
		klines, err := h.App.LiveMarket.GetKlines(c.Request.Context(), "", c.Query("symbol"), c.DefaultQuery("interval", "1m"), from, to)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": klines})
		return
	}
	if account.SandboxID == nil {
		writeError(c, domain.ValidationError("ACCOUNT_SANDBOX_REQUIRED", "account is not bound to sandbox"))
		return
	}
	klines, err := h.App.Market.GetKlines(c.Request.Context(), *account.SandboxID, c.Query("symbol"), c.DefaultQuery("interval", "1m"), from, to)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": klines})
}

func (h *Handler) CreateOrder(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	var req service.PlaceOrderInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, domain.ValidationError("INVALID_REQUEST", "invalid order payload"))
		return
	}
	order, err := h.App.Trading.PlaceOrder(c.Request.Context(), accountID, req)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, order)
}

func (h *Handler) ListOrders(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	orders, err := h.App.Trading.ListOrders(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": orders})
}

func (h *Handler) GetOrder(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	order, err := h.App.Trading.GetOrder(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	if order.AccountID != accountID {
		writeError(c, domain.ForbiddenError("order does not belong to account"))
		return
	}
	c.JSON(http.StatusOK, order)
}

func (h *Handler) CancelOrder(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	order, err := h.App.Trading.CancelOrder(c.Request.Context(), accountID, c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, order)
}

func (h *Handler) ListTrades(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	trades, err := h.App.Trading.ListTrades(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": trades})
}

func (h *Handler) ListPositions(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	positions, err := h.App.Trading.ListPositions(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": positions})
}

func (h *Handler) GetAccount(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	account, err := h.App.Trading.GetAccountSummary(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, account)
}

func (h *Handler) GetAccountPerformance(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	perf, err := h.App.Trading.GetPerformance(c.Request.Context(), accountID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, perf)
}

func (h *Handler) LiveSymbolsSnapshot(c *gin.Context) {
	snapshot, err := h.App.Monitor.LiveSymbols(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}
func (h *Handler) SandboxSnapshot(c *gin.Context) {
	snapshot, err := h.App.Monitor.Snapshot(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) AdminMonitorWS(c *gin.Context) {
	conn, err := h.Upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sandboxID := c.Query("sandbox_id")
	ch, unsubscribe := h.App.Events.Subscribe(16, func(evt domain.DomainEvent) bool {
		return sandboxID == "" || evt.SandboxID == sandboxID
	})
	defer unsubscribe()

	if sandboxID != "" {
		if snapshot, err := h.App.Monitor.Snapshot(c.Request.Context(), sandboxID); err == nil {
			_ = conn.WriteJSON(gin.H{"type": "snapshot", "payload": snapshot})
		}
	}
	h.streamEvents(c.Request.Context(), conn, ch)
}

func (h *Handler) AccountWS(c *gin.Context) {
	accountID := c.MustGet(ctxAccountID).(string)
	conn, err := h.Upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch, unsubscribe := h.App.Events.Subscribe(16, func(evt domain.DomainEvent) bool {
		return evt.AccountID == accountID
	})
	defer unsubscribe()

	if account, err := h.App.Trading.GetAccountSummary(c.Request.Context(), accountID); err == nil {
		_ = conn.WriteJSON(gin.H{"type": "snapshot", "payload": account})
	}
	h.streamEvents(c.Request.Context(), conn, ch)
}

func (h *Handler) streamEvents(ctx context.Context, conn *websocket.Conn, ch <-chan domain.DomainEvent) {
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(evt); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func writeError(c *gin.Context, err error) {
	appErr := service.MapError(err)
	c.AbortWithStatusJSON(appErr.Status, gin.H{
		"code":    appErr.Code,
		"message": appErr.Message,
		"details": appErr.Details,
	})
}

func isNotFound(err error) bool {
	var appErr *domain.AppError
	if errors.As(err, &appErr) {
		return appErr.Status == http.StatusNotFound
	}
	return false
}
