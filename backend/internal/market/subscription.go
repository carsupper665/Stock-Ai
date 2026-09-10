package market

import (
	"context"
	"errors"
	"time"
)

// 這個檔案是訂閱的生命週期：啟動、餵價、結束、閒置回收（規格 §10、§11）。
// 取價入口與快取判斷在 runtime.go。

// activateLocked 啟動訂閱。呼叫時必須持有 mu，且 entry 必須是 inactive，
// 因此同一個標的不會有第二個啟動流程（規格 §10）。
func (r *Runtime) activateLocked(e *entry) {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.state = StateActivating
	e.lastErr = nil

	src := r.sources[e.market]
	r.wg.Add(1)
	go r.stream(ctx, e, src)
}

// deactivateLocked 停止訂閱但保留快取內容（規格 §11 只要求停訂閱）。
// 狀態要等 stream 收尾後才會變成 inactive。呼叫時必須持有 mu。
func (r *Runtime) deactivateLocked(e *entry) {
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
}

// stream 把來源送出的報價寫進快取，直到來源結束。
func (r *Runtime) stream(ctx context.Context, e *entry, src Source) {
	defer r.wg.Done()

	prices := make(chan float64)
	done := make(chan error, 1)
	go func() { done <- src.Stream(ctx, e.symbol, prices) }()

	// 這裡沒有 ctx.Done() 分支是刻意的：取消之後仍要繼續把 prices 讀掉，
	// 直到 Stream 自己回傳，否則對方可能卡在送值上而洩漏 goroutine。
	for {
		select {
		case price := <-prices:
			r.publish(e, price)
		case err := <-done:
			r.finish(e, err)
			return
		}
	}
}

func (r *Runtime) publish(e *entry, price float64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e.price = price
	e.updatedAt = r.now()
	e.hasPrice = true
	e.lastErr = nil
	e.state = StateActive
	e.broadcast()
}

// finish 在訂閱結束時把狀態收回 inactive，並喚醒等待者。
func (r *Runtime) finish(e *entry, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e.state = StateInactive
	e.cancel = nil
	// 取消是我們自己造成的（閒置回收或關機），不算錯誤。
	if err != nil && !ctxCanceled(err) {
		e.lastErr = err
		if r.log != nil {
			r.log.Warnf(context.Background(), "%s %s 行情訂閱結束: %v", e.market, e.symbol, err)
		}
	}
	e.broadcast()
}

// sweepLoop 定期檢查閒置的訂閱。
func (r *Runtime) sweepLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.sweep(r.now())
		case <-r.stopped:
			return
		}
	}
}

// sweep 停掉超過閒置時限、沒有人再要價的訂閱（規格 §11）。
func (r *Runtime) sweep(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, e := range r.entries {
		// cancel 為 nil 表示沒有訂閱在跑，或已經取消但還沒收尾。
		// 少了這個判斷，收尾期間每次掃描都會重複記一次閒置逾時。
		if e.state == StateInactive || e.cancel == nil {
			continue
		}
		if now.Sub(e.lastRequestedAt) <= r.idleTimeout {
			continue
		}
		if r.log != nil {
			r.log.Infof(context.Background(), "%s %s 閒置逾時，停止訂閱", e.market, e.symbol)
		}
		r.deactivateLocked(e)
	}
}

func ctxCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
