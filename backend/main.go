package main

import (
	"context"
	"log"
	"os"

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
	cfg, err := config.Load(".env")
	if err != nil {
		log.Fatalf("設定載入失敗: %v", err)
	}

	apiLog, err := logging.New("api", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		log.Fatalf("api logger 建立失敗: %v", err)
	}
	defer apiLog.Close()

	dbLog, err := logging.New("db", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		apiLog.Fatalf("db logger 建立失敗: %v", err)
	}
	defer dbLog.Close()

	store, err := database.Open(cfg, dbLog)
	if err != nil {
		apiLog.Fatalf("%v", err)
	}
	if err := store.Migrate(); err != nil {
		apiLog.Fatalf("%v", err)
	}

	marketLog, err := logging.New("market", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		apiLog.Fatalf("market logger 建立失敗: %v", err)
	}
	defer marketLog.Close()

	prices := market.New(map[string]market.Source{
		market.Crypto: crypto.NewBinance(),
		market.Stock:  stock.New(),
	}, marketLog, market.Options{
		FreshTTL:    cfg.MarketFreshTTL,
		IdleTimeout: cfg.MarketIdleTimeout,
		WaitTimeout: cfg.MarketWaitTimeout,
	})
	defer prices.Close()

	matchLog, err := logging.New("matching", cfg.LogDir, cfg.LogMaxLines, cfg.Debug)
	if err != nil {
		apiLog.Fatalf("matching logger 建立失敗: %v", err)
	}
	defer matchLog.Close()

	trader := trading.New(store, prices, cfg.FeeRateMaker, cfg.FeeRateTaker)
	matcher := trading.NewEngine(trader, cfg.MatchingInterval, matchLog)
	matcher.Start()
	defer matcher.Stop()

	engine := api.New(cfg, store, prices, trader, apiLog)
	apiLog.Infof(context.Background(), "服務啟動於 :%s", cfg.Port)
	address := os.Getenv("BACKEND_ADDR")
	if address == "" {
		address = ":" + cfg.Port
	}
	if err := engine.Run(address); err != nil {
		apiLog.Fatalf("服務啟動失敗: %v", err)
	}
}
