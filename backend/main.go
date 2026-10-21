package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend/internal/api"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
	"backend/internal/market"
	"backend/internal/market/crypto"
	"backend/internal/trading"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Backend stopped with error: %v", err)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return fmt.Errorf("設定載入失敗: %w", err)
	}

	apiLog, err := logging.New("api", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		return fmt.Errorf("api logger 建立失敗: %w", err)
	}
	defer apiLog.Close()

	dbLog, err := logging.New("db", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		return fmt.Errorf("db logger 建立失敗: %w", err)
	}
	defer dbLog.Close()

	store, err := database.Open(cfg, dbLog)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return err
	}

	marketLog, err := logging.New("market", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		return fmt.Errorf("market logger 建立失敗: %w", err)
	}
	defer marketLog.Close()

	prices := market.New(map[string]market.Source{
		market.Crypto: crypto.NewBinance(),
		market.Stock:  crypto.NewBinanceFutures(),
	}, marketLog, market.Options{
		FreshTTL:    cfg.MarketFreshTTL,
		IdleTimeout: cfg.MarketIdleTimeout,
		WaitTimeout: cfg.MarketWaitTimeout,
	})
	defer prices.Close()

	matchLog, err := logging.New("matching", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		return fmt.Errorf("matching logger 建立失敗: %w", err)
	}
	defer matchLog.Close()

	trader := trading.New(store, prices, cfg.FeeRateMaker, cfg.FeeRateTaker)
	matcher := trading.NewEngine(trader, cfg.MatchingInterval, matchLog)
	matcher.Start()
	defer matcher.Stop()

	address := os.Getenv("BACKEND_ADDR")
	if address == "" {
		address = ":" + cfg.Port
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("服務啟動失敗: %w", err)
	}
	engine := api.New(cfg, store, prices, trader, apiLog)
	server := &http.Server{
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	apiLog.Infof(context.Background(), "服務啟動於 %s", listener.Addr())

	select {
	case serveErr := <-serveResult:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	case <-shutdownSignal.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownContext)
	if shutdownErr != nil {
		_ = server.Close()
	}
	serveErr := <-serveResult
	if shutdownErr != nil {
		return fmt.Errorf("graceful shutdown: %w", shutdownErr)
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
