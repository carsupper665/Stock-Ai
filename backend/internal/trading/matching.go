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

	// max_dd 風控候選（規格 §6）：設定過 max_dd 的帳號。它們的部位（含沒有
	// 停損停利的）一併納入取價；風控評估先於限價／停損停利撮合，讓停止標記
	// 先於本輪任何成交生效。未設定 max_dd 的帳號完全不走這條路徑。
	candidates, err := e.svc.store.ListMaxDDAccounts(ctx)
	if err != nil {
		e.warn(ctx, "載入風控帳號失敗: %v", err)
		result.Errors++
		return result
	}
	pricePositions := positions
	for i := range candidates {
		candidatePositions, err := e.svc.store.ListPositions(ctx, candidates[i].ID, "")
		if err != nil {
			e.warn(ctx, "載入風控帳號 %s 的部位失敗: %v", candidates[i].ID, err)
			result.Errors++
			continue
		}
		pricePositions = append(pricePositions, candidatePositions...)
	}

	prices := e.collectPrices(ctx, orders, pricePositions)

	for i := range candidates {
		closed, err := e.svc.enforceMaxDD(ctx, candidates[i].ID, prices)
		result.Closed += closed
		if err != nil {
			if errors.Is(err, ErrRiskPriceUnavailable) {
				// 缺現價（E1）：跳過這輪、下輪重試，不擋其他帳號。
				e.warn(ctx, "風控帳號 %s 缺少現價，本輪跳過風控評估", candidates[i].ID)
				continue
			}
			e.warn(ctx, "風控帳號 %s 評估失敗: %v", candidates[i].ID, err)
			result.Errors++
		}
	}

	// 記錄 sweep 後的停止狀態（規格 §6.3 本輪末清理用）：sweep 已處理（含已
	// 停止或已嘗試清倉）的帳號，限價成交階段之後不再重做，避免重複清倉
	// 嘗試與重複計數錯誤。
	stoppedAfterSweep := make(map[string]bool, len(candidates))
	for i := range candidates {
		acc, err := e.svc.store.AccountByID(ctx, candidates[i].ID)
		if err != nil {
			continue
		}
		stoppedAfterSweep[candidates[i].ID] = acc.MaxDDStoppedAt != nil
	}

	for i := range orders {
		order := &orders[i]
		price, ok := prices[key(order.Market, order.Symbol)]
		if !ok || !limitTriggered(order, price) {
			continue
		}
		filled, err := e.svc.fillOpenOrder(ctx, order.AccountID, order.ID, order.Price, prices)
		switch {
		case err != nil:
			if errors.Is(err, ErrRiskPriceUnavailable) {
				// 持倉缺價（E1）：本輪無法完整評估風險，跳過成交、下輪重試；
				// 與風控 sweep 同樣視為帳號自己的狀態，不是引擎故障。
				e.warn(ctx, "限價單 %s 缺現價無法完成風險評估，本輪跳過", order.ID)
				continue
			}
			e.warn(ctx, "限價單 %s 成交失敗: %v", order.ID, err)
			result.Errors++
		case filled == nil:
			// 載入後已不是 open，這輪不處理。
		case filled.Status == database.OrderCanceled:
			// 停止標記在快照載入後寫入、precheck 已撤單：不得算成 Filled（規格 §6）。
			e.info(ctx, "限價單 %s 被風控撤單，不算成交", order.ID)
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
			if isBusinessRejection(err) || errors.Is(err, ErrStopInactive) || errors.Is(err, ErrMaxDDStopped) {
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

	// 本輪末風控清理（規格 §6.3）：限價成交的 postcheck 可能在 sweep 之後才
	// 提交停止標記（成交費用造成越線）；只對「sweep 後仍未停止、現在已停止」
	// 的帳號在本輪完成強制清倉。
	for i := range candidates {
		if stoppedAfterSweep[candidates[i].ID] {
			continue
		}
		acc, err := e.svc.store.AccountByID(ctx, candidates[i].ID)
		if err != nil {
			e.warn(ctx, "風控帳號 %s 本輪末清理載入失敗: %v", candidates[i].ID, err)
			result.Errors++
			continue
		}
		if acc.MaxDDStoppedAt == nil {
			continue // 本輪沒有新停止
		}
		closed, err := e.svc.liquidateMaxDD(ctx, candidates[i].ID, prices)
		result.Closed += closed
		if err != nil {
			e.warn(ctx, "風控帳號 %s 本輪末清理失敗: %v", candidates[i].ID, err)
			result.Errors++
		}
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
