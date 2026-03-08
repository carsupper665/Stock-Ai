package test

import (
	"context"
	"crypto/tls"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"server/utils"
)

func TestPriceStartWs_EmptySymbol(t *testing.T) {
	var p utils.Price
	if err := p.StartWs(" "); err == nil {
		t.Fatal("expected error for empty symbol")
	}
}

func TestPriceStopWs_NoStart(t *testing.T) {
	var p utils.Price
	if err := p.StopWs(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestPriceGet_UpdatesLastUsed(t *testing.T) {
	var p utils.Price
	if !p.LastUsed.IsZero() {
		t.Fatalf("expected zero LastUsed, got %v", p.LastUsed)
	}

	if got := p.Get(); got != 0 {
		t.Fatalf("expected default price 0, got %v", got)
	}

	if p.LastUsed.IsZero() {
		t.Fatal("expected LastUsed to be updated")
	}
}

func TestPriceCacheGet_EmptySymbol(t *testing.T) {
	pc := utils.NewPriceCache()
	if _, err := pc.Get(" "); err == nil {
		t.Fatal("expected error for empty symbol")
	}
}

func TestPriceCacheGet_HighConcurrency(t *testing.T) {
	server := newTLSServer(t, []byte(`{"c":"42.5"}`))
	defer server.Close()

	patchDefaultDialer(t, server.Listener.Addr().String())

	pc := utils.NewPriceCache()

	const workers = 100
	const iterations = 50

	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if _, err := pc.Get("BTC"); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
	}
}

func TestPriceStartWs_ReceivesPrice(t *testing.T) {
	const want = 123.45
	server := newTLSServer(t, []byte(`{"c":"123.45"}`))
	defer server.Close()

	patchDefaultDialer(t, server.Listener.Addr().String())

	p := &utils.Price{
		ExpireTime: 2 * time.Second,
		LastUsed:   time.Now(),
	}
	if err := p.StartWs("BTC"); err != nil {
		t.Fatalf("StartWs failed: %v", err)
	}
	t.Cleanup(func() { _ = p.StopWs() })

	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for price update, got %v", p.Get())
		case <-ticker.C:
			if got := p.Get(); math.Abs(got-want) < 1e-9 {
				return
			}
		}
	}
}

func newTLSServer(t *testing.T, msg []byte) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, msg)
		time.Sleep(25 * time.Millisecond)
	}))
}

func patchDefaultDialer(t *testing.T, tlsAddr string) {
	t.Helper()

	orig := *websocket.DefaultDialer
	websocket.DefaultDialer.Proxy = nil
	websocket.DefaultDialer.NetDialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{}
		return tls.DialWithDialer(dialer, network, tlsAddr, &tls.Config{
			InsecureSkipVerify: true,
		})
	}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}

	t.Cleanup(func() {
		*websocket.DefaultDialer = orig
	})
}
