package crypto

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newFakeBinance 啟一個假的 Binance，handler 決定每次請求的回應。
func newFakeBinance(t *testing.T, handler http.HandlerFunc) *Binance {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	b := NewBinance()
	b.BaseURL = server.URL
	b.Interval = 5 * time.Millisecond
	return b
}

func okPrice(price string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"` + r.URL.Query().Get("symbol") + `","price":"` + price + `"}`))
	}
}

func TestStreamSendsFirstPriceImmediately(t *testing.T) {
	b := newFakeBinance(t, okPrice("60123.45"))
	b.Interval = time.Hour // 確認第一筆不是等到 tick 才送

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan float64, 1)
	go func() { _ = b.Stream(ctx, "BTCUSDT", out) }()

	select {
	case price := <-out:
		if price != 60123.45 {
			t.Fatalf("價格解析錯誤: %v", price)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("第一筆報價應該立刻送出，不該等到下一個輪詢週期")
	}
}

func TestStreamKeepsPolling(t *testing.T) {
	var calls atomic.Int32
	b := newFakeBinance(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		okPrice("100")(w, r)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan float64)
	go func() { _ = b.Stream(ctx, "BTCUSDT", out) }()

	for i := 0; i < 3; i++ {
		select {
		case <-out:
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 次輪詢沒有送出報價", i+1)
		}
	}
	if calls.Load() < 3 {
		t.Fatalf("預期至少查詢 3 次，實際 %d", calls.Load())
	}
}

func TestStreamStopsOnContextCancel(t *testing.T) {
	b := newFakeBinance(t, okPrice("100"))

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan float64, 8)
	done := make(chan error, 1)
	go func() { done <- b.Stream(ctx, "BTCUSDT", out) }()

	<-out
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消時應回 context.Canceled，得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消後 Stream 沒有結束")
	}
}

func TestStreamReportsSourceErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name:    "非 200 回應",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) },
			want:    "429",
		},
		{
			name:    "回應不是 JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) },
			want:    "無法解析",
		},
		{
			name:    "價格不是數字",
			handler: okPrice("abc"),
			want:    "價格格式錯誤",
		},
		{
			name:    "價格為零",
			handler: okPrice("0"),
			want:    "非正數",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBinance(t, tc.handler)

			out := make(chan float64, 1)
			err := b.Stream(context.Background(), "BTCUSDT", out)
			if err == nil {
				t.Fatal("預期回傳錯誤")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("錯誤訊息應包含 %q，實際 %v", tc.want, err)
			}
		})
	}
}

func TestStreamPassesSymbolThrough(t *testing.T) {
	got := make(chan string, 1)
	b := newFakeBinance(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.URL.Query().Get("symbol"):
		default:
		}
		okPrice("100")(w, r)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan float64, 1)
	go func() { _ = b.Stream(ctx, "ETHUSDT", out) }()

	select {
	case symbol := <-got:
		if symbol != "ETHUSDT" {
			t.Fatalf("送出的 symbol 錯誤: %q", symbol)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("沒有收到請求")
	}
}
