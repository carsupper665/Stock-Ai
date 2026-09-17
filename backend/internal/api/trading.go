package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"backend/internal/database"
	"backend/internal/market"
	"backend/internal/market/stock"
	"backend/internal/trading"

	"github.com/gin-gonic/gin"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// sourceRequest 是 Agent Server 注入的來源。帳號身分仍由 Token 決定，這裡只驗證欄位完整。
type sourceRequest struct {
	SessionID *string `json:"session_id"`
	RunID     *int64  `json:"run_id"`
}

func parseSource(c *gin.Context, req *sourceRequest) (database.Source, bool) {
	if req == nil {
		return database.Source{}, true
	}
	if req.SessionID == nil || req.RunID == nil {
		fail(c, http.StatusBadRequest, "invalid_request", "source 必須同時提供 session_id 與 run_id")
		return database.Source{}, false
	}
	sessionID := strings.TrimSpace(*req.SessionID)
	if sessionID == "" || len(sessionID) > 64 || *req.RunID <= 0 {
		fail(c, http.StatusBadRequest, "invalid_request", "source.session_id 必須是 1 到 64 字元，run_id 必須是正整數")
		return database.Source{}, false
	}
	return database.Source{SessionID: sessionID, RunID: *req.RunID}, true
}

type placeOrderRequest struct {
	Market     string         `json:"market"`
	Symbol     string         `json:"symbol"`
	Product    string         `json:"product"`
	Side       string         `json:"side"`
	Type       string         `json:"type"`
	Quantity   float64        `json:"quantity"`
	Price      float64        `json:"price"`
	Leverage   float64        `json:"leverage"`
	StopLoss   float64        `json:"stop_loss"`
	TakeProfit float64        `json:"take_profit"`
	Source     *sourceRequest `json:"source"`
}

func (s *Server) placeOrder(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}

	var req placeOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
		return
	}
	source, ok := parseSource(c, req.Source)
	if !ok {
		return
	}

	order, err := s.trading.PlaceOrder(c.Request.Context(), accountID, trading.PlaceOrderInput{
		Market: req.Market, Symbol: req.Symbol, Product: req.Product,
		Side: req.Side, Type: req.Type,
		Quantity: req.Quantity, Price: req.Price, Leverage: req.Leverage,
		StopLoss: req.StopLoss, TakeProfit: req.TakeProfit, Source: source,
	})
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusCreated, viewOrder(order))
}

type closePositionRequest struct {
	Quantity float64        `json:"quantity"`
	Source   *sourceRequest `json:"source"`
}

func (s *Server) closePosition(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}

	var req closePositionRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
			return
		}
	}
	source, ok := parseSource(c, req.Source)
	if !ok {
		return
	}

	order, err := s.trading.ClosePosition(c.Request.Context(), accountID, c.Param("id"), req.Quantity, source)
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewOrder(order))
}

type setStopsRequest struct {
	StopLoss   *float64       `json:"stop_loss"`
	TakeProfit *float64       `json:"take_profit"`
	Source     *sourceRequest `json:"source"`
}

// setStops 設定、更新或移除停損停利：欄位省略不動，0 移除，大於 0 設定。
func (s *Server) setStops(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}

	var req setStopsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
		return
	}
	source, ok := parseSource(c, req.Source)
	if !ok {
		return
	}

	pos, err := s.trading.SetStops(c.Request.Context(), accountID, c.Param("id"), trading.StopInput{
		StopLoss: req.StopLoss, TakeProfit: req.TakeProfit, Source: source,
	})
	if err != nil {
		s.writeTradingError(c, err)
		return
	}

	// stops 已提交，之後只能回 2xx（Agent 把非 500 錯誤當成「未執行」，見 SPEC §21.5）；
	// 現價只是盡力補上 mark_price／unrealized_pnl，取不到就留 0。
	view := trading.OpenPosition{Position: *pos}
	if price, err := s.market.GetPrice(c.Request.Context(), pos.Market, pos.Symbol); err != nil {
		s.log.Warnf(c.Request.Context(), "部位 %s 的停損停利已更新，但取不到 %s %s 現價: %v", pos.ID, pos.Market, pos.Symbol, err)
	} else {
		view.MarkPrice, view.Unrealized = price.Price, trading.Unrealized(pos, price.Price)
	}
	c.JSON(http.StatusOK, viewPosition(view))
}

type cancelOrderRequest struct {
	Source *sourceRequest `json:"source"`
}

func (s *Server) cancelOrder(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}
	var req cancelOrderRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
			return
		}
	}
	source, ok := parseSource(c, req.Source)
	if !ok {
		return
	}
	order, err := s.trading.CancelOrder(c.Request.Context(), accountID, c.Param("id"), source)
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewOrder(order))
}

func (s *Server) listLedger(c *gin.Context) {
	if accountID, ok := s.ownAccount(c); ok {
		s.ledgerFor(c, accountID)
	}
}

func (s *Server) userListLedger(c *gin.Context) {
	s.ledgerFor(c, c.Param("id"))
}

// ledgerFor 依 seq 由新到舊列出帳戶事件；before_seq 是游標，session_id／run_id 篩選來源。
func (s *Server) ledgerFor(c *gin.Context, accountID string) {
	filter := database.LedgerFilter{SessionID: strings.TrimSpace(c.Query("session_id")), Limit: listLimit(c)}
	var ok bool
	if filter.RunID, ok = optionalPositiveQuery(c, "run_id"); !ok {
		return
	}
	if filter.BeforeSeq, ok = optionalPositiveQuery(c, "before_seq"); !ok {
		return
	}
	entries, err := s.store.ListLedger(c.Request.Context(), accountID, filter)
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	views := make([]ledgerEntryView, 0, len(entries))
	for i := range entries {
		views = append(views, viewLedgerEntry(&entries[i]))
	}
	c.JSON(http.StatusOK, gin.H{"entries": views})
}

func optionalPositiveQuery(c *gin.Context, name string) (int64, bool) {
	raw := c.Query(name)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		fail(c, http.StatusBadRequest, "invalid_request", name+" 必須是正整數")
		return 0, false
	}
	return value, true
}

func (s *Server) listPositions(c *gin.Context) {
	if accountID, ok := s.ownAccount(c); ok {
		s.positionsFor(c, accountID)
	}
}

func (s *Server) userListPositions(c *gin.Context) {
	s.positionsFor(c, c.Param("id"))
}

func (s *Server) positionsFor(c *gin.Context, accountID string) {
	positions, err := s.trading.Positions(c.Request.Context(), accountID, c.Query("product"))
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	views := make([]positionView, 0, len(positions))
	for _, p := range positions {
		views = append(views, viewPosition(p))
	}
	c.JSON(http.StatusOK, gin.H{"positions": views})
}

func (s *Server) listOrders(c *gin.Context) {
	if accountID, ok := s.ownAccount(c); ok {
		s.ordersFor(c, accountID)
	}
}

func (s *Server) userListOrders(c *gin.Context) {
	s.ordersFor(c, c.Param("id"))
}

func (s *Server) ordersFor(c *gin.Context, accountID string) {
	orders, err := s.trading.Orders(c.Request.Context(), accountID, c.Query("status"), listLimit(c))
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	views := make([]orderView, 0, len(orders))
	for i := range orders {
		views = append(views, viewOrder(&orders[i]))
	}
	c.JSON(http.StatusOK, gin.H{"orders": views})
}

func (s *Server) getOrder(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}
	order, err := s.trading.Order(c.Request.Context(), accountID, c.Param("id"))
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewOrder(order))
}

func (s *Server) listTrades(c *gin.Context) {
	if accountID, ok := s.ownAccount(c); ok {
		s.tradesFor(c, accountID)
	}
}

func (s *Server) userListTrades(c *gin.Context) {
	s.tradesFor(c, c.Param("id"))
}

func (s *Server) tradesFor(c *gin.Context, accountID string) {
	trades, err := s.trading.Trades(c.Request.Context(), accountID, listLimit(c))
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	views := make([]tradeView, 0, len(trades))
	for i := range trades {
		views = append(views, viewTrade(&trades[i]))
	}
	c.JSON(http.StatusOK, gin.H{"trades": views})
}

func listLimit(c *gin.Context) int {
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil || limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func (s *Server) writeTradingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, database.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "找不到指定的資料")
	case errors.Is(err, trading.ErrPositionClosed):
		fail(c, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, trading.ErrInsufficient):
		fail(c, http.StatusUnprocessableEntity, "insufficient_balance", err.Error())
	case errors.Is(err, trading.ErrAccountDisabled):
		fail(c, http.StatusForbidden, "account_disabled", err.Error())
	case errors.Is(err, trading.ErrOrderNotOpen):
		fail(c, http.StatusConflict, "order_not_open", err.Error())
	case errors.Is(err, market.ErrEmptySymbol), errors.Is(err, market.ErrUnknownMarket):
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, stock.ErrNotImplemented):
		fail(c, http.StatusNotImplemented, "not_implemented", "美股行情來源尚未接上")
	case errors.Is(err, market.ErrTimeout):
		fail(c, http.StatusGatewayTimeout, "market_timeout", "等不到行情，無法成交")
	case isValidationError(err):
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.log.Errorf(c.Request.Context(), "交易操作失敗: %v", err)
		fail(c, http.StatusInternalServerError, "internal_error", "交易操作失敗")
	}
}

func isValidationError(err error) bool {
	for _, target := range []error{
		trading.ErrInvalidProduct, trading.ErrInvalidSide, trading.ErrInvalidType,
		trading.ErrInvalidQuantity, trading.ErrInvalidPrice, trading.ErrInvalidLeverage,
		trading.ErrSpotLeverage, trading.ErrSpotShort, trading.ErrInvalidStopLoss,
		trading.ErrCloseTooMuch, trading.ErrLeverageLocked, trading.ErrNoStopChange,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// ownAccount 取出呼叫者自己的帳號。交易與自查都以 token 決定身分，
// USER 沒有可交易的帳號（規格 §4）。
func (s *Server) ownAccount(c *gin.Context) (string, bool) {
	identity, ok := identityOf(c)
	if !ok || identity.IsUser() {
		fail(c, http.StatusForbidden, "forbidden", "這個端點只有 Account Token 可以使用")
		return "", false
	}
	return identity.AccountID, true
}
