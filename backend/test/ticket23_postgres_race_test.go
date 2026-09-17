package test

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
)

// Postgres is the documented production driver and the only one where mutations of one
// Account can run on separate connections; there the per-Account row lock (Store.LockAccount)
// is what serializes cancel-versus-fill and close-versus-close. The test needs a disposable
// database in BACKEND_TEST_POSTGRES_DSN; without it the Postgres guarantee is unverified.
func TestTicket23PostgresMutationsAreSerializedPerAccount(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_POSTGRES_DSN is not set; Postgres row locking is unverified on this host")
	}
	logger, err := logging.New("ticket-postgres", t.TempDir(), 1000, false)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	logger.SetConsole(io.Discard)
	t.Cleanup(func() { _ = logger.Close() })
	store, err := database.Open(&config.Config{DBDriver: "postgres", DBDSN: dsn}, logger)
	if err != nil {
		t.Fatalf("open Postgres: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	backend := newTradingAPIWithStore(t, store, "")
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	_, token := backend.createAccount(t, "ticket-23-pg-"+suffix, 1000000)
	backend.setPrice(t, 49000)

	fills, cancels := 0, 0
	for round := 0; round < 10; round++ {
		order := placeOrder(t, backend, token, map[string]any{
			"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "limit", "quantity": 0.01, "price": 50000, "leverage": 10,
			"source": agentSource("s_pg", 1),
		})
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var cancelStatus int
		go func() {
			defer wait.Done()
			<-start
			backend.engine.Tick(context.Background())
		}()
		go func() {
			defer wait.Done()
			<-start
			cancelStatus, _ = backend.request(t, http.MethodPost, "/v1/orders/"+order.ID+"/cancel", token, map[string]any{"source": agentSource("s_pg", 2)})
		}()
		close(start)
		wait.Wait()

		status, raw := backend.request(t, http.MethodGet, "/v1/orders/"+order.ID, token, nil)
		final := decodeJSON[orderView](t, raw)
		events := map[string]int{}
		for _, entry := range backend.ledger(t, token, "?limit=3") {
			if entry.OrderID == order.ID {
				events[entry.Event]++
			}
		}
		switch {
		case status == http.StatusOK && final.Status == "filled" && cancelStatus == http.StatusConflict && events["fill"] == 1 && events["order_canceled"] == 0 && events["order_placed"] == 1:
			fills++
		case status == http.StatusOK && final.Status == "canceled" && cancelStatus == http.StatusOK && events["order_canceled"] == 1 && events["fill"] == 0 && events["order_placed"] == 1:
			cancels++
		default:
			t.Fatalf("round %d: order=%+v cancel_status=%d events=%v", round, final, cancelStatus, events)
		}
	}
	positions := listPositions(t, backend, token)
	quantity := 0.0
	if len(positions.Positions) == 1 {
		quantity = positions.Positions[0].Quantity
	}
	if fills+cancels != 10 || quantity != float64(fills)*0.01 {
		t.Fatalf("fills=%d cancels=%d position=%v", fills, cancels, quantity)
	}

	// Two full closes of the same position started together: exactly one settles.
	if quantity > 0 {
		if status, raw := backend.request(t, http.MethodPost, "/v1/positions/"+positions.Positions[0].ID+"/close", token, nil); status != http.StatusOK {
			t.Fatalf("flatten status=%d body=%s", status, raw)
		}
	}
	placeOrder(t, backend, token, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.1, "leverage": 10, "source": agentSource("s_pg", 3),
	})
	positionID := listPositions(t, backend, token).Positions[0].ID
	start := make(chan struct{})
	statuses := make([]int, 2)
	var wait sync.WaitGroup
	wait.Add(len(statuses))
	for i := range statuses {
		go func(i int) {
			defer wait.Done()
			<-start
			statuses[i], _ = backend.request(t, http.MethodPost, "/v1/positions/"+positionID+"/close", token, map[string]any{"source": agentSource("s_pg", 4)})
		}(i)
	}
	close(start)
	wait.Wait()
	if statuses[0]+statuses[1] != http.StatusOK+http.StatusNotFound {
		t.Fatalf("double close statuses = %v", statuses)
	}
	if remaining := listPositions(t, backend, token); len(remaining.Positions) != 0 {
		t.Fatalf("position survived the close: %+v", remaining.Positions)
	}
	positionFills := 0 // the opening fill plus exactly one closing fill
	for _, entry := range backend.ledger(t, token, "?limit=5") {
		if entry.Event == "fill" && entry.PositionID == positionID {
			positionFills++
		}
	}
	newest := backend.ledger(t, token, "?limit=1")
	status, raw := backend.request(t, http.MethodGet, "/v1/account", token, nil)
	account := decodeJSON[struct {
		Balance float64 `json:"balance"`
	}](t, raw)
	if positionFills != 2 || status != http.StatusOK || len(newest) != 1 || newest[0].Event != "fill" || newest[0].PositionID != positionID || newest[0].BalanceAfter != account.Balance {
		t.Fatalf("position fills=%d account=%d %s newest=%+v", positionFills, status, raw, newest)
	}
}
