package market

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentGetPriceSharesOneActivation(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	src.delay = 50 * time.Millisecond
	r := newTestRuntime(t, src, clock)

	const callers = 20
	var wg sync.WaitGroup
	prices := make([]float64, callers)
	errs := make([]error, callers)

	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer wg.Done()
			p, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
			prices[i], errs[i] = p.Price, err
		}(i)
	}

	waitFor(t, "訂閱啟動", func() bool { return src.streamCount("BTCUSDT") == 1 })
	src.push(t, "BTCUSDT", 60000)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("第 %d 個呼叫失敗: %v", i, errs[i])
		}
		if prices[i] != 60000 {
			t.Fatalf("第 %d 個呼叫拿到 %v，預期 60000", i, prices[i])
		}
	}
	if got := src.streamCount("BTCUSDT"); got != 1 {
		t.Fatalf("%d 個併發呼叫只該啟動一次訂閱，實際 %d 次", callers, got)
	}
}

func TestSourceFailureReturnsErrorInsteadOfHanging(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	src.setError(errors.New("上游掛了"))
	r := newTestRuntime(t, src, clock)

	start := time.Now()
	_, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
	if err == nil {
		t.Fatal("來源失敗時應回傳錯誤")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("來源失敗應立刻回報，卻等了 %v", elapsed)
	}
	if !strings.Contains(err.Error(), "上游掛了") {
		t.Fatalf("錯誤訊息應包含來源的原因: %v", err)
	}
}

func TestRuntimeRecoversAfterSourceFailure(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	src.setError(errors.New("暫時性失敗"))
	r := newTestRuntime(t, src, clock)

	if _, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT"); err == nil {
		t.Fatal("第一次應該失敗")
	}

	src.setError(nil)
	primeCache(t, r, src, "BTCUSDT", 60000)

	price, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
	if err != nil {
		t.Fatalf("來源恢復後仍失敗: %v", err)
	}
	if price.Price != 60000 {
		t.Fatalf("恢復後價格不符: %v", price.Price)
	}
}

func TestIdleSubscriptionIsDeactivated(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	primeCache(t, r, src, "BTCUSDT", 60000)
	if state := r.Snapshot()[key(Crypto, "BTCUSDT")]; state != StateActive {
		t.Fatalf("餵價後狀態應為 active，實際 %q", state)
	}

	r.sweep(clock.now().Add(DefaultIdleTimeout))
	if state := r.Snapshot()[key(Crypto, "BTCUSDT")]; state != StateActive {
		t.Fatalf("未超過閒置時限就被停掉了: %q", state)
	}

	r.sweep(clock.now().Add(DefaultIdleTimeout + time.Second))
	waitFor(t, "訂閱回到 inactive", func() bool {
		return r.Snapshot()[key(Crypto, "BTCUSDT")] == StateInactive
	})
	waitFor(t, "來源的 Stream 已結束", func() bool { return src.runningCount("BTCUSDT") == 0 })
}

func TestGetPriceKeepsSubscriptionAlive(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	primeCache(t, r, src, "BTCUSDT", 60000)

	for i := 0; i < 3; i++ {
		clock.advance(DefaultIdleTimeout / 2)
		primeCache(t, r, src, "BTCUSDT", 60000+float64(i))
		r.sweep(clock.now())

		if state := r.Snapshot()[key(Crypto, "BTCUSDT")]; state == StateInactive {
			t.Fatalf("第 %d 輪：持續有人要價的標的被誤判為閒置", i+1)
		}
	}

	if got := src.streamCount("BTCUSDT"); got != 1 {
		t.Fatalf("期間不該重新訂閱，實際啟動 %d 次", got)
	}
}

func TestDeactivatedSymbolReactivatesOnNextRequest(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	primeCache(t, r, src, "BTCUSDT", 60000)
	r.sweep(clock.now().Add(DefaultIdleTimeout + time.Second))
	waitFor(t, "訂閱停止", func() bool {
		return r.Snapshot()[key(Crypto, "BTCUSDT")] == StateInactive
	})

	clock.advance(time.Millisecond)
	price, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
	if err != nil {
		t.Fatalf("停用後仍在新鮮期內應能命中快取: %v", err)
	}
	if price.Price != 60000 {
		t.Fatalf("快取內容遺失: %v", price.Price)
	}

	clock.advance(DefaultFreshTTL)
	primeCache(t, r, src, "BTCUSDT", 62000)

	if got := src.streamCount("BTCUSDT"); got != 2 {
		t.Fatalf("停用後再要價應重新訂閱，實際啟動 %d 次", got)
	}
	if state := r.Snapshot()[key(Crypto, "BTCUSDT")]; state != StateActive {
		t.Fatalf("重新啟動後狀態應為 active，實際 %q", state)
	}
}

func TestCloseStopsAllSubscriptions(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := New(map[string]Source{Crypto: src}, nil, Options{Now: clock.now, WaitTimeout: time.Second})

	primeCache(t, r, src, "BTCUSDT", 60000)
	primeCache(t, r, src, "ETHUSDT", 3000)

	r.Close()

	if src.runningCount("BTCUSDT") != 0 || src.runningCount("ETHUSDT") != 0 {
		t.Fatalf("關閉後仍有訂閱在跑: btc=%d eth=%d",
			src.runningCount("BTCUSDT"), src.runningCount("ETHUSDT"))
	}
	if _, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT"); !errors.Is(err, ErrClosed) {
		t.Fatalf("關閉後取價應回 ErrClosed，得到 %v", err)
	}
	r.Close()
}
