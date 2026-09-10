package api

import (
	"errors"
	"net/http"
	"strconv"

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

// round 在輸出邊界收斂浮點尾數，避免 0.1+0.2 那類雜訊外流。

type placeOrderRequest struct {
	Market     string  `json:"market"`
	Symbol     string  `json:"symbol"`
	Product    string  `json:"product"`
	Side       string  `json:"side"`
	Type       string  `json:"type"`
	Quantity   float64 `json:"quantity"`
	Price      float64 `json:"price"`
	Leverage   float64 `json:"leverage"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
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

	order, err := s.trading.PlaceOrder(c.Request.Context(), accountID, trading.PlaceOrderInput{
		Market: req.Market, Symbol: req.Symbol, Product: req.Product,
		Side: req.Side, Type: req.Type,
		Quantity: req.Quantity, Price: req.Price, Leverage: req.Leverage,
		StopLoss: req.StopLoss, TakeProfit: req.TakeProfit,
	})
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusCreated, viewOrder(order))
}

type closePositionRequest struct {
	Quantity float64 `json:"quantity"`
}

func (s *Server) closePosition(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}

	var req closePositionRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
			return
		}
	}

	order, err := s.trading.ClosePosition(c.Request.Context(), accountID, c.Param("id"), req.Quantity)
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewOrder(order))
}

func (s *Server) cancelOrder(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}
	order, err := s.trading.CancelOrder(c.Request.Context(), accountID, c.Param("id"))
	if err != nil {
		s.writeTradingError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewOrder(order))
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
		trading.ErrCloseTooMuch, trading.ErrLeverageLocked,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// ownAccount 取出呼叫者自己的帳號。交易與自查都以 token 決定身分，
// USER 沒有可交易的帳號（規格 §4）。

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
