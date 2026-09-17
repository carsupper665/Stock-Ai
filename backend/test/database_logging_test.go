package test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
)

func TestDatabaseLogsDoNotExposeAccountTokens(t *testing.T) {
	dir := t.TempDir()
	logger, err := logging.New("safe-db", dir, 10000, true)
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	logger.SetConsole(io.Discard)
	dbPath := filepath.Join(t.TempDir(), "safe.db")
	store, err := database.Open(&config.Config{DBDriver: "sqlite", DBDSN: dbPath, Debug: true}, logger)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	const sentinelToken = "at_SYNTHETIC_SECRET_MUST_NOT_APPEAR"
	account := &database.Account{ID: "acc_safe_log", UserName: "safe-log-owner", Token: sentinelToken, InitialBalance: 100, Balance: 100, Status: database.AccountActive}
	if err := store.CreateAccount(context.Background(), account); err != nil {
		t.Fatalf("create Account: %v", err)
	}
	if _, err := store.AccountByToken(context.Background(), sentinelToken); err != nil {
		t.Fatalf("query Account token: %v", err)
	}
	backend := newTradingAPIWithStore(t, store, dbPath)
	status, raw := backend.request(t, http.MethodPost, "/v1/orders", sentinelToken, map[string]any{
		"symbol": "BTCUSDT", "product": "futures", "side": "buy", "type": "market", "quantity": 0.001, "leverage": 10,
	})
	if status != http.StatusCreated {
		t.Fatalf("settlement status=%d body=%s", status, raw)
	}
	status, raw = backend.request(t, http.MethodPost, "/v1/accounts/"+account.ID+"/token/reset", tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("token reset status=%d body=%s", status, raw)
	}
	const secondSentinel = "at_SECOND_SYNTHETIC_SECRET_MUST_NOT_APPEAR"
	duplicate := &database.Account{ID: "acc_safe_log_duplicate", UserName: "safe-log-owner", Token: secondSentinel, InitialBalance: 100, Balance: 100, Status: database.AccountActive}
	if err := store.CreateAccount(context.Background(), duplicate); !errors.Is(err, database.ErrDuplicate) {
		t.Fatalf("duplicate Account error=%v, want ErrDuplicate", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("database log files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read database log: %v", err)
	}
	logText := string(data)
	if strings.Contains(logText, sentinelToken) || strings.Contains(logText, secondSentinel) {
		t.Fatalf("database log exposed a bound parameter: %s", logText)
	}
	if !strings.Contains(logText, "FROM `accounts` WHERE token = ?") {
		t.Fatalf("database log lost parameterized query shape: %s", logText)
	}
	if !strings.Contains(logText, "rows=") || !strings.Contains(logText, "error=duplicate") {
		t.Fatalf("database log lost timing/row/error diagnostics: %s", logText)
	}
}
