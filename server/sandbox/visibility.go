package sandbox

import "fmt"

var (
	ErrFutureDataLocked = fmt.Errorf("future data locked")
	ErrSummaryLocked    = fmt.Errorf("summary locked")
	ErrInvalidBarRange  = fmt.Errorf("invalid bar range")
)

type SummaryKind string

const (
	SummaryInSample SummaryKind = "in_sample"
	SummaryOOS      SummaryKind = "oos"
)

func VisibleBarLimit(session RunSession) (int, error) {
	switch session.Stage {
	case StageTraining:
		return session.TrainRange.End, nil
	case StageExecution:
		return session.Cursor, nil
	case StageInSampleSummary:
		return session.ValidationRange.End, nil
	case StageOOSSummary:
		return session.OOSRange.End, nil
	case StagePromotion:
		if session.PromotionRange == nil {
			return 0, fmt.Errorf("promotion range is required for promotion stage")
		}
		return session.PromotionRange.End, nil
	default:
		return 0, fmt.Errorf("unsupported run stage: %s", session.Stage)
	}
}

func GuardBarRange(session RunSession, start, end, totalBars int) error {
	if start < 0 || end < 0 || start > end {
		return fmt.Errorf("%w: start=%d end=%d", ErrInvalidBarRange, start, end)
	}
	if end > totalBars {
		return fmt.Errorf("%w: end=%d total=%d", ErrInvalidBarRange, end, totalBars)
	}

	limit, err := VisibleBarLimit(session)
	if err != nil {
		return err
	}
	if end > limit+1 {
		return fmt.Errorf("%w: requested_end=%d visible_limit=%d", ErrFutureDataLocked, end-1, limit)
	}
	return nil
}

func CheckSummaryAccess(session RunSession, summary SummaryKind) error {
	switch summary {
	case SummaryInSample:
		switch session.Stage {
		case StageInSampleSummary, StageOOSSummary, StagePromotion:
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrSummaryLocked, summary)
		}
	case SummaryOOS:
		switch session.Stage {
		case StageOOSSummary, StagePromotion:
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrSummaryLocked, summary)
		}
	default:
		return fmt.Errorf("unknown summary kind: %s", summary)
	}
}
