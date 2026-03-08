package test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	storepkg "server/model/store"
	"server/sandbox"
	"server/service"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRunServiceRejectsFutureBarsDuringExecution(t *testing.T) {
	runService, _, ctx := newRunService(t)
	if err := runService.CreateRun(ctx, sampleRunSession()); err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	visibleBars, err := runService.GetVisibleBars(ctx, "run-1", 0, 2)
	if err != nil {
		t.Fatalf("GetVisibleBars failed: %v", err)
	}
	if len(visibleBars) != 2 {
		t.Fatalf("expected 2 visible bars, got %d", len(visibleBars))
	}
	if visibleBars[1].Close != 102.25 {
		t.Fatalf("expected second visible close 102.25, got %v", visibleBars[1].Close)
	}

	if _, err := runService.GetVisibleBars(ctx, "run-1", 0, 3); !errors.Is(err, sandbox.ErrFutureDataLocked) {
		t.Fatalf("expected ErrFutureDataLocked, got %v", err)
	}
}

func TestRunServiceLocksSummariesUntilStageAllows(t *testing.T) {
	runService, _, ctx := newRunService(t)
	if err := runService.CreateRun(ctx, sampleRunSession()); err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	if err := runService.CanAccessSummary(ctx, "run-1", sandbox.SummaryInSample); !errors.Is(err, sandbox.ErrSummaryLocked) {
		t.Fatalf("expected in-sample summary to stay locked, got %v", err)
	}
	if err := runService.CanAccessSummary(ctx, "run-1", sandbox.SummaryOOS); !errors.Is(err, sandbox.ErrSummaryLocked) {
		t.Fatalf("expected oos summary to stay locked, got %v", err)
	}

	if err := runService.UpdateStage(ctx, "run-1", sandbox.StageInSampleSummary, 1); err != nil {
		t.Fatalf("UpdateStage to in-sample failed: %v", err)
	}
	if err := runService.CanAccessSummary(ctx, "run-1", sandbox.SummaryInSample); err != nil {
		t.Fatalf("expected in-sample summary to be unlocked, got %v", err)
	}
	if err := runService.CanAccessSummary(ctx, "run-1", sandbox.SummaryOOS); !errors.Is(err, sandbox.ErrSummaryLocked) {
		t.Fatalf("expected oos summary to remain locked, got %v", err)
	}

	if err := runService.UpdateStage(ctx, "run-1", sandbox.StageOOSSummary, 2); err != nil {
		t.Fatalf("UpdateStage to oos failed: %v", err)
	}
	if err := runService.CanAccessSummary(ctx, "run-1", sandbox.SummaryOOS); err != nil {
		t.Fatalf("expected oos summary to be unlocked, got %v", err)
	}
}

func TestRunServicePersistsVersionMetadata(t *testing.T) {
	runService, runStore, ctx := newRunService(t)
	want := sampleRunSession()

	if err := runService.CreateRun(ctx, want); err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	record, err := runStore.GetByRunID(ctx, want.RunID)
	if err != nil {
		t.Fatalf("GetByRunID failed: %v", err)
	}

	if record.ScenarioID != want.ScenarioID {
		t.Fatalf("expected scenario id %s, got %s", want.ScenarioID, record.ScenarioID)
	}
	if record.DatasetHash != want.DatasetHash {
		t.Fatalf("expected dataset hash %s, got %s", want.DatasetHash, record.DatasetHash)
	}
	if record.ExecutionModelVersion != want.ExecutionModelVersion {
		t.Fatalf("expected execution version %s, got %s", want.ExecutionModelVersion, record.ExecutionModelVersion)
	}
	if record.FeeModelVersion != want.FeeModelVersion {
		t.Fatalf("expected fee version %s, got %s", want.FeeModelVersion, record.FeeModelVersion)
	}
	if record.SlippageModelVersion != want.SlippageModelVersion {
		t.Fatalf("expected slippage version %s, got %s", want.SlippageModelVersion, record.SlippageModelVersion)
	}
}

func newRunService(t *testing.T) (*service.RunService, *storepkg.RunSessionStore, context.Context) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "run-service.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB failed: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	if err := db.AutoMigrate(&storepkg.RunSession{}); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}

	runStore := storepkg.NewRunSessionStore(db)
	runService := service.NewRunService(runStore)

	feed := sandbox.NewReplayFeed()
	scenarioPath := filepath.Join("..", "testdata", "replay", "btcusdt_1m.json")
	if err := feed.LoadScenarioFile(scenarioPath); err != nil {
		t.Fatalf("LoadScenarioFile failed: %v", err)
	}
	if err := runService.RegisterScenario(feed); err != nil {
		t.Fatalf("RegisterScenario failed: %v", err)
	}

	return runService, runStore, context.Background()
}

func sampleRunSession() sandbox.RunSession {
	return sandbox.RunSession{
		RunID:       "run-1",
		ScenarioID:  "btc-usdt-1m-sample",
		DatasetHash: "dataset-hash-v1",
		TrainRange: sandbox.IndexRange{
			Start: 0,
			End:   0,
		},
		ValidationRange: sandbox.IndexRange{
			Start: 1,
			End:   1,
		},
		OOSRange: sandbox.IndexRange{
			Start: 2,
			End:   2,
		},
		Stage:                 sandbox.StageExecution,
		Cursor:                1,
		ExecutionModelVersion: "exec-v1",
		FeeModelVersion:       "fee-v1",
		SlippageModelVersion:  "slippage-v1",
	}
}
