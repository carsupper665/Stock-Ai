package market

import (
	"context"
	"fmt"
	"sync"
	"time"

	"backend/internal/logging"
)

// 預設參數，對應規格 §8 與 §11。
const (
	DefaultFreshTTL    = 1500 * time.Millisecond
	DefaultIdleTimeout = 60 * time.Second
	DefaultWaitTimeout = 5 * time.Second
	sweepInterval      = time.Second
)

// entry 同時是規格 §8 的 Price Cache 與 §10 的 Subscription。
// 兩者一對一綁在同一個 market+symbol 上，拆開反而要多一層同步。
// 所有欄位都由 Runtime.mu 保護。
type entry struct {
	market string
	symbol string

	price     float64
	updatedAt time.Time
	hasPrice  bool

	lastRequestedAt time.Time

	state   State
	cancel  context.CancelFunc
	lastErr error

	// updated 是廣播用的通道：有新價或訂閱結束時 close 並換一個新的，
	// 所有等待者會同時醒來。
	updated chan struct{}
}

func (e *entry) snapshot() Price {
	return Price{Market: e.market, Symbol: e.symbol, Price: e.price, UpdatedAt: e.updatedAt}
}

// broadcast 喚醒所有等待者。呼叫時必須持有 Runtime.mu。
func (e *entry) broadcast() {
	close(e.updated)
	e.updated = make(chan struct{})
}

// Options 可調整 Runtime 的時間參數，零值表示採用預設。
type Options struct {
	FreshTTL    time.Duration
	IdleTimeout time.Duration
	WaitTimeout time.Duration
	Now         func() time.Time
}

// Runtime 是全後端取得行情的唯一入口（規格 §7）。
// Trading 與 Matching Engine 一律透過 GetPrice，不可以自己連行情來源。
//
// 這個檔案負責取價與快取判斷；訂閱的生命週期在 subscription.go。
type Runtime struct {
	mu      sync.Mutex
	entries map[string]*entry
	sources map[string]Source
	closed  bool

	freshTTL    time.Duration
	idleTimeout time.Duration
	waitTimeout time.Duration
	now         func() time.Time

	log      *logging.Logger
	wg       sync.WaitGroup
	stopped  chan struct{}
	stopOnce sync.Once
}

// New 建立 Runtime。sources 以市場名稱為鍵，例如 {"crypto": binanceSource}。
// 啟動時不會有任何訂閱，第一次有人要價才會建立（規格 §10）。
func New(sources map[string]Source, log *logging.Logger, opts Options) *Runtime {
	r := &Runtime{
		entries:     make(map[string]*entry),
		sources:     make(map[string]Source, len(sources)),
		freshTTL:    firstDuration(opts.FreshTTL, DefaultFreshTTL),
		idleTimeout: firstDuration(opts.IdleTimeout, DefaultIdleTimeout),
		waitTimeout: firstDuration(opts.WaitTimeout, DefaultWaitTimeout),
		now:         opts.Now,
		log:         log,
		stopped:     make(chan struct{}),
	}
	if r.now == nil {
		r.now = time.Now
	}
	for name, src := range sources {
		r.sources[normalizeMarket(name)] = src
	}

	r.wg.Add(1)
	go r.sweepLoop()
	return r
}

// GetPrice 取得最新報價，是規格 §9 的完整流程：
// 快取夠新就直接回；否則啟動訂閱並等新價進來。
func (r *Runtime) GetPrice(ctx context.Context, market, symbol string) (Price, error) {
	market, symbol, err := normalize(market, symbol)
	if err != nil {
		return Price{}, err
	}

	deadline := time.NewTimer(r.waitTimeout)
	defer deadline.Stop()

	waited := false
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return Price{}, ErrClosed
		}
		e, err := r.entryLocked(market, symbol)
		if err != nil {
			r.mu.Unlock()
			return Price{}, err
		}

		// 規格 §11：任何一次 GetPrice 都要更新最後請求時間。
		e.lastRequestedAt = r.now()

		if e.hasPrice && r.now().Sub(e.updatedAt) < r.freshTTL {
			price := e.snapshot()
			r.mu.Unlock()
			return price, nil
		}

		// 等過一輪之後才看錯誤：這樣每次 GetPrice 至少會促成一次嘗試，
		// 來源恢復時不需要額外的重試機制。
		if waited && e.lastErr != nil {
			failure := e.lastErr
			r.mu.Unlock()
			return Price{}, fmt.Errorf("%s %s 取得報價失敗: %w", market, symbol, failure)
		}

		if e.state == StateInactive {
			r.activateLocked(e)
		}
		wait := e.updated
		r.mu.Unlock()

		select {
		case <-wait:
			waited = true
		case <-deadline.C:
			return Price{}, fmt.Errorf("%s %s: %w", market, symbol, ErrTimeout)
		case <-ctx.Done():
			return Price{}, ctx.Err()
		}
	}
}

// Snapshot 回傳目前所有標的的訂閱狀態，供監控與測試使用。
func (r *Runtime) Snapshot() map[string]State {
	r.mu.Lock()
	defer r.mu.Unlock()

	states := make(map[string]State, len(r.entries))
	for k, e := range r.entries {
		states[k] = e.state
	}
	return states
}

// Close 停掉所有訂閱並等背景工作結束。
func (r *Runtime) Close() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		for _, e := range r.entries {
			r.deactivateLocked(e)
		}
		r.mu.Unlock()
		close(r.stopped)
	})
	r.wg.Wait()
}

// entryLocked 取得或建立快取項目。呼叫時必須持有 mu。
func (r *Runtime) entryLocked(market, symbol string) (*entry, error) {
	if _, ok := r.sources[market]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownMarket, market)
	}
	k := key(market, symbol)
	if e, ok := r.entries[k]; ok {
		return e, nil
	}
	// 規格 §10：一個 market+symbol 只會有一份快取與一個訂閱。
	e := &entry{
		market:  market,
		symbol:  symbol,
		state:   StateInactive,
		updated: make(chan struct{}),
	}
	r.entries[k] = e
	return e, nil
}

func firstDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func normalizeMarket(name string) string {
	m, _, err := normalize(name, "X")
	if err != nil {
		return name
	}
	return m
}
