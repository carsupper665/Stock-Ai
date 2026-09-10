package market

import (
	"context"
	"sync"
	"testing"
	"time"
)

// 這個檔案只有測試替身與共用輔助函式，沒有測試案例。

// fakeSource 讓測試自己決定何時送出價格、何時失敗，
// 並記錄每個標的被開了幾次訂閱。
type fakeSource struct {
	mu      sync.Mutex
	streams map[string]int // Stream 被呼叫的次數
	running map[string]int // 目前仍在執行的 Stream 數
	feeds   map[string]chan float64
	err     error         // 非 nil 時 Stream 直接失敗
	delay   time.Duration // 模擬連線建立耗時
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		streams: map[string]int{},
		running: map[string]int{},
		feeds:   map[string]chan float64{},
	}
}

func (f *fakeSource) Name() string { return "fake" }

func (f *fakeSource) Stream(ctx context.Context, symbol string, out chan<- float64) error {
	f.mu.Lock()
	f.streams[symbol]++
	f.running[symbol]++
	err, delay := f.err, f.delay
	feed := f.feedFor(symbol)
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.running[symbol]--
		f.mu.Unlock()
	}()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}

	for {
		select {
		case price := <-feed:
			select {
			case out <- price:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// feedFor 取得（必要時建立）某個標的的餵價通道。呼叫時必須持有 mu。
func (f *fakeSource) feedFor(symbol string) chan float64 {
	if ch, ok := f.feeds[symbol]; ok {
		return ch
	}
	ch := make(chan float64)
	f.feeds[symbol] = ch
	return ch
}

// push 送出一個價格，會等到 Stream 真的收下為止。
func (f *fakeSource) push(t *testing.T, symbol string, price float64) {
	t.Helper()
	f.mu.Lock()
	feed := f.feedFor(symbol)
	f.mu.Unlock()

	select {
	case feed <- price:
	case <-time.After(2 * time.Second):
		t.Fatalf("送出 %s 的價格逾時，訂閱可能沒有啟動", symbol)
	}
}

func (f *fakeSource) streamCount(symbol string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[symbol]
}

func (f *fakeSource) runningCount(symbol string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[symbol]
}

func (f *fakeSource) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// testClock 是可控時鐘，讓新鮮度與閒置逾時不必靠真實等待。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestRuntime(t *testing.T, src Source, clock *testClock) *Runtime {
	t.Helper()
	r := New(map[string]Source{Crypto: src}, nil, Options{
		FreshTTL:    DefaultFreshTTL,
		IdleTimeout: DefaultIdleTimeout,
		WaitTimeout: 2 * time.Second,
		Now:         clock.now,
	})
	t.Cleanup(r.Close)
	return r
}

// waitFor 輪詢直到條件成立或逾時。
func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待逾時: %s", why)
}

// primeCache 啟動訂閱並餵入一筆價格，讓快取進入 active 狀態。
func primeCache(t *testing.T, r *Runtime, src *fakeSource, symbol string, price float64) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.GetPrice(context.Background(), Crypto, symbol); err != nil {
			t.Errorf("預熱 %s 失敗: %v", symbol, err)
		}
	}()

	src.push(t, symbol, price)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("預熱 %s 逾時", symbol)
	}
}
