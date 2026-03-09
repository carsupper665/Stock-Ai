package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"server/domain"
	"server/model/repo"
	"server/model/store"

	"gorm.io/gorm"
)

type DatasetService struct {
	repo   *repo.Repository
	clock  domain.Clock
	events domain.EventPublisher
}

type CreateDatasetInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Source   string `json:"source"`
}

type DatasetSummary struct {
	store.ReplayDataset
	RowCount        int64                   `json:"row_count"`
	LatestImportJob *store.DatasetImportJob `json:"latest_import_job,omitempty"`
}

type parsedDatasetRows struct {
	rows    []store.ReplayKline
	startAt time.Time
	endAt   time.Time
}

func NewDatasetService(repo *repo.Repository, clock domain.Clock, events domain.EventPublisher) *DatasetService {
	return &DatasetService{repo: repo, clock: clock, events: events}
}

func (s *DatasetService) Create(ctx context.Context, input CreateDatasetInput) (*store.ReplayDataset, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, domain.ValidationError("INVALID_DATASET_NAME", "dataset name is required")
	}
	symbol := strings.ToUpper(strings.TrimSpace(input.Symbol))
	if symbol == "" {
		return nil, domain.ValidationError("INVALID_DATASET_SYMBOL", "dataset symbol is required")
	}
	interval := strings.TrimSpace(input.Interval)
	if interval == "" {
		interval = "1m"
	}
	if input.ID == "" {
		input.ID = newID("dts")
	}

	dataset := &store.ReplayDataset{
		ID:        input.ID,
		Name:      strings.TrimSpace(input.Name),
		Symbol:    symbol,
		Interval:  interval,
		Source:    strings.TrimSpace(input.Source),
		CreatedAt: s.clock.Now(),
	}
	if err := s.repo.Create(ctx, dataset); err != nil {
		return nil, err
	}
	return dataset, nil
}

func (s *DatasetService) List(ctx context.Context) ([]DatasetSummary, error) {
	var datasets []store.ReplayDataset
	if err := s.repo.WithContext(ctx).Order("created_at desc").Find(&datasets).Error; err != nil {
		return nil, err
	}
	out := make([]DatasetSummary, 0, len(datasets))
	for _, dataset := range datasets {
		summary, err := s.hydrateSummary(ctx, dataset)
		if err != nil {
			return nil, err
		}
		out = append(out, *summary)
	}
	return out, nil
}

func (s *DatasetService) Get(ctx context.Context, datasetID string) (*DatasetSummary, error) {
	dataset, err := s.repo.FindDataset(ctx, datasetID)
	if err != nil {
		return nil, domain.NotFoundError("DATASET_NOT_FOUND", "replay dataset not found")
	}
	return s.hydrateSummary(ctx, *dataset)
}

func (s *DatasetService) GetJob(ctx context.Context, jobID string) (*store.DatasetImportJob, error) {
	var job store.DatasetImportJob
	if err := s.repo.WithContext(ctx).First(&job, "id = ?", jobID).Error; err != nil {
		return nil, domain.NotFoundError("DATASET_IMPORT_JOB_NOT_FOUND", "dataset import job not found")
	}
	return &job, nil
}

func (s *DatasetService) ImportCSV(ctx context.Context, datasetID, fileName string, content []byte) (*store.DatasetImportJob, error) {
	dataset, err := s.repo.FindDataset(ctx, datasetID)
	if err != nil {
		return nil, domain.NotFoundError("DATASET_NOT_FOUND", "replay dataset not found")
	}

	parsed, parseErr := parseDatasetCSV(content, dataset.ID, dataset.Symbol)
	if parseErr != nil {
		failed := s.newImportJob(datasetID, fileName, store.DatasetImportStatusFailed, 0)
		failed.ErrorSummary = parseErr.Error()
		now := s.clock.Now()
		failed.FinishedAt = &now
		if err := s.repo.Create(ctx, failed); err != nil {
			return nil, err
		}
		_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "dataset.import.failed", AggregateID: failed.ID, Payload: map[string]any{"dataset_id": datasetID, "error": failed.ErrorSummary}})
		return failed, domain.ValidationError("INVALID_DATASET_CSV", parseErr.Error())
	}

	job := s.newImportJob(datasetID, fileName, store.DatasetImportStatusRunning, len(parsed.rows))
	startedAt := s.clock.Now()
	job.StartedAt = &startedAt
	if err := s.repo.Create(ctx, job); err != nil {
		return nil, err
	}

	go s.runImport(job.ID, *dataset, parsed)
	return job, nil
}

func (s *DatasetService) RecoverInterruptedImports(ctx context.Context) error {
	now := s.clock.Now()
	return s.repo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []store.DatasetImportJob
		if err := tx.Where("status = ?", store.DatasetImportStatusRunning).Find(&jobs).Error; err != nil {
			return err
		}
		for i := range jobs {
			jobs[i].Status = store.DatasetImportStatusFailed
			jobs[i].ErrorSummary = "interrupted by restart"
			jobs[i].FinishedAt = &now
			if err := tx.Save(&jobs[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *DatasetService) hydrateSummary(ctx context.Context, dataset store.ReplayDataset) (*DatasetSummary, error) {
	var rowCount int64
	if err := s.repo.WithContext(ctx).Model(&store.ReplayKline{}).Where("dataset_id = ?", dataset.ID).Count(&rowCount).Error; err != nil {
		return nil, err
	}

	var latestJob store.DatasetImportJob
	err := s.repo.WithContext(ctx).Where("dataset_id = ?", dataset.ID).Order("created_at desc").First(&latestJob).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	summary := &DatasetSummary{ReplayDataset: dataset, RowCount: rowCount}
	if err == nil {
		summary.LatestImportJob = &latestJob
	}
	return summary, nil
}

func (s *DatasetService) newImportJob(datasetID, fileName, status string, rowsTotal int) *store.DatasetImportJob {
	now := s.clock.Now()
	return &store.DatasetImportJob{
		ID:        newID("dij"),
		DatasetID: datasetID,
		FileName:  strings.TrimSpace(fileName),
		Status:    status,
		RowsTotal: rowsTotal,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (s *DatasetService) runImport(jobID string, dataset store.ReplayDataset, parsed parsedDatasetRows) {
	ctx := context.Background()
	err := s.repo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("dataset_id = ?", dataset.ID).Delete(&store.ReplayKline{}).Error; err != nil {
			return err
		}
		if len(parsed.rows) > 0 {
			if err := tx.CreateInBatches(parsed.rows, 200).Error; err != nil {
				return err
			}
		}

		dataset.StartAt = parsed.startAt
		dataset.EndAt = parsed.endAt
		if err := tx.Save(&dataset).Error; err != nil {
			return err
		}

		var job store.DatasetImportJob
		if err := tx.First(&job, "id = ?", jobID).Error; err != nil {
			return err
		}
		now := s.clock.Now()
		job.Status = store.DatasetImportStatusCompleted
		job.RowsImported = len(parsed.rows)
		job.FinishedAt = &now
		return tx.Save(&job).Error
	})
	if err != nil {
		_ = s.failImport(jobID, err)
		_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "dataset.import.failed", AggregateID: jobID, Payload: map[string]any{"dataset_id": dataset.ID, "error": err.Error()}})
		return
	}
	_ = s.events.Publish(ctx, domain.DomainEvent{Topic: "dataset.import.completed", AggregateID: jobID, Payload: map[string]any{"dataset_id": dataset.ID, "rows_imported": len(parsed.rows)}})
}

func (s *DatasetService) failImport(jobID string, cause error) error {
	ctx := context.Background()
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	job.Status = store.DatasetImportStatusFailed
	job.ErrorSummary = cause.Error()
	job.FinishedAt = &now
	return s.repo.Save(ctx, job)
}

func parseDatasetCSV(content []byte, datasetID, symbol string) (parsedDatasetRows, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return parsedDatasetRows{}, fmt.Errorf("csv is empty")
		}
		return parsedDatasetRows{}, fmt.Errorf("read csv header: %w", err)
	}
	expected := []string{"timestamp", "open", "high", "low", "close", "volume"}
	if len(header) != len(expected) {
		return parsedDatasetRows{}, fmt.Errorf("invalid csv header")
	}
	for i := range expected {
		if strings.TrimSpace(strings.ToLower(header[i])) != expected[i] {
			return parsedDatasetRows{}, fmt.Errorf("invalid csv header")
		}
	}

	out := parsedDatasetRows{}
	var previous time.Time
	line := 1
	for {
		line++
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("read csv row %d: %w", line, err)
		}
		if len(record) != len(expected) {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid column count", line)
		}

		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(record[0]))
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid timestamp", line)
		}
		if !previous.IsZero() && !ts.After(previous) {
			return parsedDatasetRows{}, fmt.Errorf("timestamps must be strictly increasing")
		}

		open, err := strconv.ParseFloat(strings.TrimSpace(record[1]), 64)
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid open", line)
		}
		high, err := strconv.ParseFloat(strings.TrimSpace(record[2]), 64)
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid high", line)
		}
		low, err := strconv.ParseFloat(strings.TrimSpace(record[3]), 64)
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid low", line)
		}
		closeValue, err := strconv.ParseFloat(strings.TrimSpace(record[4]), 64)
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid close", line)
		}
		volume, err := strconv.ParseFloat(strings.TrimSpace(record[5]), 64)
		if err != nil {
			return parsedDatasetRows{}, fmt.Errorf("row %d has invalid volume", line)
		}

		row := store.ReplayKline{
			DatasetID: datasetID,
			Symbol:    symbol,
			Ts:        ts.UTC(),
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closeValue,
			Volume:    volume,
		}
		out.rows = append(out.rows, row)
		if out.startAt.IsZero() {
			out.startAt = row.Ts
		}
		out.endAt = row.Ts
		previous = row.Ts
	}

	if len(out.rows) == 0 {
		return parsedDatasetRows{}, fmt.Errorf("csv must contain at least one row")
	}
	return out, nil
}
