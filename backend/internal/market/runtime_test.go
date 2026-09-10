package market

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGetPriceActivatesAndReturnsFirstPrice(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	if len(r.Snapshot()) != 0 {
		t.Fatalf("啟動時不該有任何訂閱: %v", r.Snapshot())
	}

	result := make(chan Price, 1)
	go func() {
		p, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
		if err != nil {
			t.Errorf("取得報價: %v", err)
			return
		}
		result <- p
	}()

	src.push(t, "BTCUSDT", 60000)

	select {
	case p := <-result:
		if p.Price != 60000 || p.Symbol != "BTCUSDT" || p.Market != Crypto {
			t.Fatalf("報價內容不符: %+v", p)
		}
		if !p.UpdatedAt.Equal(clock.now()) {
			t.Fatalf("更新時間應為當下: %v", p.UpdatedAt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等不到第一筆報價")
	}

	if src.streamCount("BTCUSDT") != 1 {
		t.Fatalf("預期只啟動一次訂閱，實際 %d", src.streamCount("BTCUSDT"))
	}
}

func TestFreshCacheDoesNotWaitForNewPrice(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	primeCache(t, r, src, "BTCUSDT", 60000)

	clock.advance(DefaultFreshTTL - time.Millisecond)
	price, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
	if err != nil {
		t.Fatalf("快取命中不該失敗: %v", err)
	}
	if price.Price != 60000 {
		t.Fatalf("應回快取價格，得到 %v", price.Price)
	}
	if src.streamCount("BTCUSDT") != 1 {
		t.Fatalf("命中快取不該重新訂閱，實際啟動 %d 次", src.streamCount("BTCUSDT"))
	}
}

func TestStalePriceWaitsForANewerPrice(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	primeCache(t, r, src, "BTCUSDT", 60000)
	clock.advance(DefaultFreshTTL)

	result := make(chan Price, 1)
	go func() {
		p, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
		if err != nil {
			t.Errorf("取得報價: %v", err)
			return
		}
		result <- p
	}()

	select {
	case p := <-result:
		t.Fatalf("過期的價格不該立刻回傳: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}

	src.push(t, "BTCUSDT", 61000)

	select {
	case p := <-result:
		if p.Price != 61000 {
			t.Fatalf("應取得新價 61000，得到 %v", p.Price)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("送出新價後仍未回傳")
	}
}

func TestDifferentSymbolsGetSeparateSubscriptions(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	primeCache(t, r, src, "BTCUSDT", 60000)
	primeCache(t, r, src, "ETHUSDT", 3000)

	if src.streamCount("BTCUSDT") != 1 || src.streamCount("ETHUSDT") != 1 {
		t.Fatal("兩個標的各自應有一個訂閱")
	}

	btc, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT")
	if err != nil {
		t.Fatalf("取得 BTC 報價: %v", err)
	}
	eth, err := r.GetPrice(context.Background(), Crypto, "ETHUSDT")
	if err != nil {
		t.Fatalf("取得 ETH 報價: %v", err)
	}
	if btc.Price != 60000 || eth.Price != 3000 {
		t.Fatalf("兩個標的的快取互相污染: btc=%v eth=%v", btc.Price, eth.Price)
	}
}

func TestGetPriceTimesOutWhenSourceNeverSends(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := New(map[string]Source{Crypto: src}, nil, Options{
		WaitTimeout: 80 * time.Millisecond,
		Now:         clock.now,
	})
	t.Cleanup(r.Close)

	if _, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("來源不送價時應逾時，得到 %v", err)
	}
}

func TestGetPriceRespectsCallerContext(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	if _, err := r.GetPrice(ctx, Crypto, "BTCUSDT"); !errors.Is(err, context.Canceled) {
		t.Fatalf("呼叫端取消時應回 context.Canceled，得到 %v", err)
	}
}

func TestUnknownMarketAndEmptySymbolAreRejected(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)
	ctx := context.Background()

	if _, err := r.GetPrice(ctx, "forex", "EURUSD"); !errors.Is(err, ErrUnknownMarket) {
		t.Fatalf("未註冊的市場應被拒絕，得到 %v", err)
	}
	if _, err := r.GetPrice(ctx, Crypto, "   "); !errors.Is(err, ErrEmptySymbol) {
		t.Fatalf("空 symbol 應被拒絕，得到 %v", err)
	}
	if len(r.Snapshot()) != 0 {
		t.Fatalf("被拒絕的請求不該留下任何訂閱: %v", r.Snapshot())
	}
}

func TestSymbolAndMarketAreNormalized(t *testing.T) {
	src, clock := newFakeSource(), newTestClock()
	r := newTestRuntime(t, src, clock)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.GetPrice(context.Background(), "  CRYPTO ", "  btcusdt  "); err != nil {
			t.Errorf("正規化後應可取價: %v", err)
		}
	}()
	src.push(t, "BTCUSDT", 60000)
	<-done

	if _, ok := r.Snapshot()[key(Crypto, "BTCUSDT")]; !ok {
		t.Fatalf("快取應以正規化後的鍵建立: %v", r.Snapshot())
	}
	if _, err := r.GetPrice(context.Background(), Crypto, "BTCUSDT"); err != nil {
		t.Fatalf("取價失敗: %v", err)
	}
	if got := src.streamCount("BTCUSDT"); got != 1 {
		t.Fatalf("大小寫不同的寫法應共用訂閱，實際啟動 %d 次", got)
	}
}
