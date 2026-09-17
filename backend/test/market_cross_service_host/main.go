package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"backend/internal/api"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
	"backend/internal/market"
	"backend/internal/market/crypto"
	"backend/internal/market/stock"
	"backend/internal/trading"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	sourceURL := strings.TrimRight(os.Getenv("MARKET_CROSS_SERVICE_SOURCE_URL"), "/")
	parsedSource, err := url.Parse(sourceURL)
	if err != nil || parsedSource.Scheme != "http" || parsedSource.Host == "" {
		return errors.New("MARKET_CROSS_SERVICE_SOURCE_URL must be an HTTP origin")
	}
	cfg, err := config.Load(".env")
	if err != nil {
		return fmt.Errorf("load Backend config: %w", err)
	}
	logger, err := logging.New("market-cross-service", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		return fmt.Errorf("open Backend logger: %w", err)
	}
	logger.SetConsole(io.Discard)
	defer logger.Close()
	store, err := database.Open(cfg, logger)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return err
	}
	binance := crypto.NewBinance()
	binance.BaseURL = sourceURL
	prices := market.New(map[string]market.Source{
		market.Crypto: binance,
		market.Stock:  stock.New(),
	}, logger, market.Options{
		FreshTTL: cfg.MarketFreshTTL, IdleTimeout: cfg.MarketIdleTimeout, WaitTimeout: cfg.MarketWaitTimeout,
	})
	defer prices.Close()
	trader := trading.New(store, prices, cfg.FeeRateMaker, cfg.FeeRateTaker)
	matcher := trading.NewEngine(trader, cfg.MatchingInterval, logger)
	matcher.Start()
	defer matcher.Stop()
	handler := api.New(cfg, store, prices, trader, logger)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: handler}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	fmt.Printf("READY http://%s\n", listener.Addr())

	stdinClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdinClosed)
	}()
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	select {
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Backend: %w", err)
		}
		return nil
	case <-stdinClosed:
	case <-signalContext.Done():
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownContext)
	serveErr := <-serveDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}
