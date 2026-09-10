package main

import (
	"context"
	"log"

	"backend/internal/api"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
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

	engine := api.New(cfg, store, apiLog)
	apiLog.Infof(context.Background(), "服務啟動於 :%s", cfg.Port)
	if err := engine.Run(":" + cfg.Port); err != nil {
		apiLog.Fatalf("服務啟動失敗: %v", err)
	}
}
