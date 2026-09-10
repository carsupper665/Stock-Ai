package trading

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/market"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// fixedSource 持續送出目前設定的價格，讓測試可以隨時改盤。
type fixedSource struct {
	mu    sync.Mutex
	price float64
}

func (f *fixedSource) Name() string { return "fixed" }

func (f *fixedSource) set(price float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.price = price
}

func (f *fixedSource) Stream(ctx context.Context, _ string, out chan<- float64) error {
	for {
		f.mu.Lock()
		price := f.price
		f.mu.Unlock()

		select {
		case out <- price:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type fixture struct {
	svc       *Service
	store     *database.Store
	source    *fixedSource
	accountID string
}

func newFixture(t *testing.T, balance, price float64) *fixture {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{
		TranslateError: true,
		Logger:         gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("開啟測試資料庫: %v", err)
	}
	store := database.NewStore(db)
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("建表: %v", err)
	}

	source := &fixedSource{price: price}
	prices := market.New(map[string]market.Source{market.Crypto: source}, nil, market.Options{
		FreshTTL:    time.Millisecond,
		WaitTimeout: 2 * time.Second,
	})
	t.Cleanup(prices.Close)

	account := &database.Account{
		ID: "acc_test", UserName: "tester", Token: "at_test",
		InitialBalance: balance, Balance: balance, Status: database.AccountActive,
	}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		t.Fatalf("建立測試帳號: %v", err)
	}

	return &fixture{
		svc:       New(store, prices, makerFee, takerFee),
		store:     store,
		source:    source,
		accountID: account.ID,
	}
}

func (f *fixture) place(t *testing.T, in PlaceOrderInput) *database.Order {
	t.Helper()
	order, err := f.svc.PlaceOrder(context.Background(), f.accountID, in)
	if err != nil {
		t.Fatalf("下單失敗: %v", err)
	}
	return order
}

// setPrice 改盤並等到新價真的進了快取才回。
// 不能只靠縮短新鮮期：這台機器的時鐘粒度約 0.5ms，
// 連續兩次 time.Now() 可能完全相等，導致過期判斷失效。

// setPrice 改盤並等到新價真的進了快取才回。
// 不能只靠縮短新鮮期：這台機器的時鐘粒度約 0.5ms，
// 連續兩次 time.Now() 可能完全相等，導致過期判斷失效。
func (f *fixture) setPrice(t *testing.T, price float64) {
	t.Helper()
	f.source.set(price)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := f.svc.prices.GetPrice(context.Background(), market.Crypto, "BTCUSDT")
		if err == nil && near(got.Price, price) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("價格未更新為 %v", price)
}

func (f *fixture) summary(t *testing.T) Summary {
	t.Helper()
	s, err := f.svc.Summary(context.Background(), f.accountID)
	if err != nil {
		t.Fatalf("取得帳務總覽: %v", err)
	}
	return s
}

func futuresBuy(qty, leverage float64) PlaceOrderInput {
	return PlaceOrderInput{
		Symbol: "BTCUSDT", Product: database.ProductFutures,
		Side: database.SideBuy, Type: database.OrderMarket,
		Quantity: qty, Leverage: leverage,
	}
}
