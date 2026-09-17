package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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

func (s *Server) marketOHLCV(c *gin.Context) {
	start, err := time.Parse(time.RFC3339, c.Query("start_time"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "start_time 必須是 RFC3339 時間")
		return
	}
	end, err := time.Parse(time.RFC3339, c.Query("end_time"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "end_time 必須是 RFC3339 時間")
		return
	}
	limit := market.DefaultOHLCVLimit
	if raw, provided := c.GetQuery("limit"); provided {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			fail(c, http.StatusBadRequest, "invalid_request", "limit 必須是整數")
			return
		}
	}
	result, err := s.market.GetOHLCV(c.Request.Context(), c.DefaultQuery("market", market.Crypto), c.Query("symbol"),
		strings.TrimSpace(c.Query("interval")), start, end, limit)
	if err != nil {
		s.writeMarketError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) marketInfo(c *gin.Context) {
	result, err := s.market.GetMarketInfo(c.Request.Context(), c.DefaultQuery("market", market.Crypto), c.Query("symbol"))
	if err != nil {
		s.writeMarketError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) writeMarketError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, market.ErrEmptySymbol):
		fail(c, http.StatusBadRequest, "invalid_request", "symbol 不可為空")
	case errors.Is(err, market.ErrUnknownMarket):
		fail(c, http.StatusBadRequest, "invalid_request", "未支援的 market，目前只有 crypto 與 stock")
	case errors.Is(err, market.ErrUnsupportedInterval):
		fail(c, http.StatusBadRequest, "unsupported_interval", "interval 只支援 1m、5m、15m、1h、4h、1d")
	case errors.Is(err, market.ErrInvalidTimeRange):
		fail(c, http.StatusBadRequest, "invalid_request", "start_time 必須早於 end_time")
	case errors.Is(err, market.ErrInvalidLimit):
		fail(c, http.StatusBadRequest, "invalid_request", "limit 必須介於 1 到 500")
	case errors.Is(err, market.ErrReadUnsupported):
		fail(c, http.StatusNotImplemented, "not_implemented", "此行情來源不支援這項查詢")
	case errors.Is(err, market.ErrUnknownSymbol):
		fail(c, http.StatusNotFound, "symbol_not_found", "行情來源找不到 symbol")
	case errors.Is(err, stock.ErrNotImplemented):
		fail(c, http.StatusNotImplemented, "not_implemented", "美股行情來源尚未接上")
	case errors.Is(err, market.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		fail(c, http.StatusGatewayTimeout, "market_timeout", "等不到行情，請稍後再試")
	case errors.Is(err, market.ErrClosed):
		fail(c, http.StatusServiceUnavailable, "shutting_down", "服務正在關閉")
	case errors.Is(err, market.ErrInvalidSourceData):
		fail(c, http.StatusBadGateway, "market_response_invalid", "行情來源回傳無效資料")
	case errors.Is(err, c.Request.Context().Err()) && c.Request.Context().Err() != nil:
		// 呼叫端自己斷線，不需要回應也不必記成錯誤。
		c.Abort()
	default:
		s.log.Errorf(c.Request.Context(), "取得行情失敗: %v", err)
		fail(c, http.StatusBadGateway, "market_unavailable", "行情來源暫時無法使用")
	}
}
