package service

import (
	"context"
	"testing"
	"time"

	"server/model/store"
)

func TestDatasetServiceImportCSVCreatesDatasetAndRows(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	dataset, err := app.Datasets.Create(ctx, CreateDatasetInput{
		Name:     "btc-1m",
		Symbol:   "BTCUSDT",
		Interval: "1m",
		Source:   "test",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	job, err := app.Datasets.ImportCSV(ctx, dataset.ID, "btc.csv", []byte("timestamp,open,high,low,close,volume\n2025-01-01T00:00:00Z,100,101,99,100,10\n2025-01-01T00:01:00Z,101,102,100,101,11\n"))
	if err != nil {
		t.Fatalf("import csv: %v", err)
	}

	job = waitForDatasetJob(t, ctx, app, job.ID, store.DatasetImportStatusCompleted)
	if job.RowsImported != 2 {
		t.Fatalf("unexpected imported row count: %+v", job)
	}

	summary, err := app.Datasets.Get(ctx, dataset.ID)
	if err != nil {
		t.Fatalf("get dataset summary: %v", err)
	}
	if summary.RowCount != 2 {
		t.Fatalf("expected 2 rows, got %+v", summary)
	}
	if summary.LatestImportJob == nil || summary.LatestImportJob.Status != store.DatasetImportStatusCompleted {
		t.Fatalf("unexpected latest import job: %+v", summary.LatestImportJob)
	}
}

func TestDatasetServiceRejectsBrokenCSV(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	dataset, err := app.Datasets.Create(ctx, CreateDatasetInput{
		Name:     "broken",
		Symbol:   "BTCUSDT",
		Interval: "1m",
		Source:   "test",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	job, err := app.Datasets.ImportCSV(ctx, dataset.ID, "broken.csv", []byte("bad,data"))
	if err == nil {
		t.Fatalf("expected import failure")
	}
	if job == nil || job.Status != store.DatasetImportStatusFailed {
		t.Fatalf("expected failed import job, got %+v err=%v", job, err)
	}
}

func TestDatasetServiceMarksRunningJobsFailedOnStartup(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	dataset, err := app.Datasets.Create(ctx, CreateDatasetInput{
		ID:       "dataset-recover",
		Name:     "recover",
		Symbol:   "BTCUSDT",
		Interval: "1m",
		Source:   "test",
	})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	startedAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	running := store.DatasetImportJob{
		ID:        "job-running",
		DatasetID: dataset.ID,
		FileName:  "recover.csv",
		Status:    store.DatasetImportStatusRunning,
		RowsTotal: 2,
		StartedAt: &startedAt,
		CreatedAt: startedAt,
		UpdatedAt: startedAt,
	}
	if err := app.DB.Create(&running).Error; err != nil {
		t.Fatalf("create running job: %v", err)
	}

	restarted, err := NewApp(AppConfig{
		DB:            app.DB,
		SessionSecret: "test-secret",
		FrontendURL:   "http://localhost:3000",
		Now: func() time.Time {
			return time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("restart app: %v", err)
	}

	recovered, err := restarted.Datasets.GetJob(ctx, running.ID)
	if err != nil {
		t.Fatalf("get recovered job: %v", err)
	}
	if recovered.Status != store.DatasetImportStatusFailed {
		t.Fatalf("expected failed status, got %+v", recovered)
	}
	if recovered.ErrorSummary != "interrupted by restart" {
		t.Fatalf("unexpected recovery error summary: %+v", recovered)
	}
}

func waitForDatasetJob(t *testing.T, ctx context.Context, app *App, jobID, wantStatus string) *store.DatasetImportJob {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := app.Datasets.GetJob(ctx, jobID)
		if err == nil && job.Status == wantStatus {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}

	job, err := app.Datasets.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("get job after timeout: %v", err)
	}
	t.Fatalf("job %s did not reach %s: %+v", jobID, wantStatus, job)
	return nil
}
