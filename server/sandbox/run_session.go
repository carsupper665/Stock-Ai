package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

type RunStage string

const (
	StageTraining        RunStage = "training"
	StageExecution       RunStage = "execution"
	StageInSampleSummary RunStage = "in_sample_summary"
	StageOOSSummary      RunStage = "oos_summary"
	StagePromotion       RunStage = "promotion"
)

type IndexRange struct {
	Start int
	End   int
}

type RunSession struct {
	RunID                 string
	ScenarioID            string
	DatasetHash           string
	TrainRange            IndexRange
	ValidationRange       IndexRange
	OOSRange              IndexRange
	PromotionRange        *IndexRange
	Stage                 RunStage
	Cursor                int
	ExecutionModelVersion string
	FeeModelVersion       string
	SlippageModelVersion  string
}

func (r IndexRange) Validate(name string) error {
	if r.Start < 0 || r.End < 0 {
		return fmt.Errorf("%s range must be non-negative", name)
	}
	if r.Start > r.End {
		return fmt.Errorf("%s range start must be <= end", name)
	}
	return nil
}

func (s RunSession) Validate() error {
	s.RunID = strings.TrimSpace(s.RunID)
	s.ScenarioID = strings.TrimSpace(s.ScenarioID)
	s.DatasetHash = strings.TrimSpace(s.DatasetHash)
	s.ExecutionModelVersion = strings.TrimSpace(s.ExecutionModelVersion)
	s.FeeModelVersion = strings.TrimSpace(s.FeeModelVersion)
	s.SlippageModelVersion = strings.TrimSpace(s.SlippageModelVersion)

	if s.RunID == "" {
		return errors.New("run_id is required")
	}
	if s.ScenarioID == "" {
		return errors.New("scenario_id is required")
	}
	if s.DatasetHash == "" {
		return errors.New("dataset_hash is required")
	}
	if s.ExecutionModelVersion == "" {
		return errors.New("execution_model_version is required")
	}
	if s.FeeModelVersion == "" {
		return errors.New("fee_model_version is required")
	}
	if s.SlippageModelVersion == "" {
		return errors.New("slippage_model_version is required")
	}
	if err := s.TrainRange.Validate("train"); err != nil {
		return err
	}
	if err := s.ValidationRange.Validate("validation"); err != nil {
		return err
	}
	if err := s.OOSRange.Validate("oos"); err != nil {
		return err
	}
	if s.ValidationRange.Start <= s.TrainRange.End {
		return errors.New("validation range must start after train range")
	}
	if s.OOSRange.Start <= s.ValidationRange.End {
		return errors.New("oos range must start after validation range")
	}
	if s.PromotionRange != nil {
		if err := s.PromotionRange.Validate("promotion"); err != nil {
			return err
		}
		if s.PromotionRange.Start <= s.OOSRange.End {
			return errors.New("promotion range must start after oos range")
		}
	}
	if s.Cursor < 0 {
		return errors.New("cursor must be non-negative")
	}

	switch s.Stage {
	case StageTraining:
		if s.Cursor > s.TrainRange.End {
			return errors.New("training cursor exceeds train range")
		}
	case StageExecution:
		if s.Cursor > s.ValidationRange.End {
			return errors.New("execution cursor exceeds validation range")
		}
	case StageInSampleSummary:
		if s.Cursor > s.ValidationRange.End {
			return errors.New("in-sample summary cursor exceeds validation range")
		}
	case StageOOSSummary:
		if s.Cursor > s.OOSRange.End {
			return errors.New("oos summary cursor exceeds oos range")
		}
	case StagePromotion:
		if s.PromotionRange == nil {
			return errors.New("promotion stage requires promotion range")
		}
		if s.Cursor > s.PromotionRange.End {
			return errors.New("promotion cursor exceeds promotion range")
		}
	default:
		return fmt.Errorf("unsupported run stage: %s", s.Stage)
	}

	return nil
}
