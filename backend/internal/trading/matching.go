package trading

import (
	"context"
	"errors"
	"sync"
	"time"

	"backend/internal/database"
	"backend/internal/logging"
)

// Engine 是規格 §6 的撮合迴圈：定期掃描掛單與停損停利，觸發就成交。
// 刻意不做撮合簿、不做事件驅動，規格明說不需要為大量帳號最佳化。
type Engine struct {
	svc      *Service
	interval time.Duration
	log      *logging.Logger

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func NewEngine(svc *Service, interval time.Duration, log *logging.Logger) *Engine {
	return &Engine{
		svc:      svc,
		interval: interval,
		log:      log,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (e *Engine) Start() {
	go e.run()
}

// Stop 結束迴圈並等正在進行的那一輪做完。
func (e *Engine) Stop() {
	e.once.Do(func() { close(e.stop) })
	<-e.done
}

func (e *Engine) run() {
	defer close(e.done)

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.Tick(context.Background())
		case <-e.stop:
			return
		}
	}
}

// TickResult 記錄一輪撮合做了什麼，供日誌與測試使用。
type TickResult struct {
	Filled   int
	Rejected int
	Closed   int
	Errors   int
}

// Tick 執行一輪撮合。每個標的只取一次價格；取不到價格的標的整輪跳過。
func (e *Engine) Tick(ctx context.Context) TickResult {
	var result TickResult

	orders, err := e.svc.store.OpenLimitOrders(ctx)
	if err != nil {
		e.warn(ctx, "載入掛單失敗: %v", err)
		result.Errors++
		return result
	}
	positions, err := e.svc.store.PositionsWithStops(ctx)
	if err != nil {
		e.warn(ctx, "載入停損停利部位失敗: %v", err)
		result.Errors++
		return result
	}

	prices := e.collectPrices(ctx, orders, positions)

	for i := range orders {
		order := &orders[i]
		price, ok := prices[key(order.Market, order.Symbol)]
		if !ok || !limitTriggered(order, price) {
			continue
		}
		filled, err := e.svc.fillOpenOrder(ctx, order.AccountID, order.ID, order.Price)
		switch {
		case err != nil:
			e.warn(ctx, "限價單 %s 成交失敗: %v", order.ID, err)
			result.Errors++
		case filled == nil:
			// 載入後已不是 open，這輪不處理。
		case filled.Status == database.OrderRejected:
			e.info(ctx, "限價單 %s 觸發但被拒絕: %s", order.ID, filled.RejectReason)
			result.Rejected++
		default:
			e.info(ctx, "限價單 %s 於 %v 成交 %s %v %s", order.ID, order.Price, order.Side, order.Quantity, order.Symbol)
			result.Filled++
		}
	}

	for i := range positions {
		pos := &positions[i]
		price, ok := prices[key(pos.Market, pos.Symbol)]
		if !ok {
			continue
		}
		reason, hit := stopTriggered(pos, price)
		if !hit {
			continue
		}
		// 快照只決定要不要嘗試；數量與來源由 closeAt 在交易內以持久化部位為準。
		if _, err := e.svc.closeAt(ctx, pos.AccountID, pos.ID, 0, price, database.Source{}, reason); err != nil {
			if isBusinessRejection(err) || errors.Is(err, ErrStopInactive) {
				// 部位在這一輪之間被平掉、stop 被改掉，或帳號處於不可交易狀態：
				// 都是帳號自己的狀態，不是引擎故障，下一輪重新評估。
				continue
			}
			e.warn(ctx, "部位 %s 觸發 %s 但平倉失敗: %v", pos.ID, reason, err)
			result.Errors++
			continue
		}
		e.info(ctx, "部位 %s 觸發 %s，於 %v 平倉 %s %v", pos.ID, reason, price, pos.Symbol, pos.Quantity)
		result.Closed++
	}

	return result
}

// collectPrices 對這一輪涉及的每個標的取一次價格。
func (e *Engine) collectPrices(ctx context.Context, orders []database.Order, positions []database.Position) map[string]float64 {
	wanted := map[string][2]string{}
	for _, o := range orders {
		wanted[key(o.Market, o.Symbol)] = [2]string{o.Market, o.Symbol}
	}
	for _, p := range positions {
		wanted[key(p.Market, p.Symbol)] = [2]string{p.Market, p.Symbol}
	}

	prices := make(map[string]float64, len(wanted))
	for k, ms := range wanted {
		price, err := e.svc.prices.GetPrice(ctx, ms[0], ms[1])
		if err != nil {
			e.warn(ctx, "%s %s 取價失敗，本輪跳過: %v", ms[0], ms[1], err)
			continue
		}
		prices[k] = price.Price
	}
	return prices
}

func key(market, symbol string) string {
	return market + ":" + symbol
}

func (e *Engine) info(ctx context.Context, format string, args ...any) {
	if e.log != nil {
		e.log.Infof(ctx, format, args...)
	}
}

func (e *Engine) warn(ctx context.Context, format string, args ...any) {
	if e.log != nil {
		e.log.Warnf(ctx, format, args...)
	}
}
