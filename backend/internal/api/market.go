package api

import (
	"errors"
	"net/http"

	"backend/internal/market"
	"backend/internal/market/stock"

	"github.com/gin-gonic/gin"
)

// marketPrice 回傳最新報價。規格 §7：所有取價都必須經過 Market Runtime。
func (s *Server) marketPrice(c *gin.Context) {
	name := c.DefaultQuery("market", market.Crypto)
	symbol := c.Query("symbol")

	price, err := s.market.GetPrice(c.Request.Context(), name, symbol)
	if err != nil {
		s.writeMarketError(c, err)
		return
	}
	c.JSON(http.StatusOK, price)
}

// marketSubscriptions 回傳目前每個標的的訂閱狀態，供 USER 觀察
// 規格 §10、§11 的啟動與閒置回收行為。
func (s *Server) marketSubscriptions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"subscriptions": s.market.Snapshot()})
}

func (s *Server) writeMarketError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, market.ErrEmptySymbol):
		fail(c, http.StatusBadRequest, "invalid_request", "symbol 不可為空")
	case errors.Is(err, market.ErrUnknownMarket):
		fail(c, http.StatusBadRequest, "invalid_request", "未支援的 market，目前只有 crypto 與 stock")
	case errors.Is(err, stock.ErrNotImplemented):
		fail(c, http.StatusNotImplemented, "not_implemented", "美股行情來源尚未接上")
	case errors.Is(err, market.ErrTimeout):
		fail(c, http.StatusGatewayTimeout, "market_timeout", "等不到行情，請稍後再試")
	case errors.Is(err, market.ErrClosed):
		fail(c, http.StatusServiceUnavailable, "shutting_down", "服務正在關閉")
	case errors.Is(err, c.Request.Context().Err()) && c.Request.Context().Err() != nil:
		// 呼叫端自己斷線，不需要回應也不必記成錯誤。
		c.Abort()
	default:
		s.log.Errorf(c.Request.Context(), "取得行情失敗: %v", err)
		fail(c, http.StatusBadGateway, "market_unavailable", "行情來源暫時無法使用")
	}
}
