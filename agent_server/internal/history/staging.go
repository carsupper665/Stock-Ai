package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Stage collects the history shard files written inside a transaction so
// they can be restored atomically if the transaction does not commit.
type Stage struct {
	mu     sync.Locker
	files  []stagedFile
	closed bool
}

type stagedFile struct {
	path     string
	previous []byte
	existed  bool
}

type Target struct {
	SessionID string
	Start     int64
}

// StageRun stages the history shards that contain runID and its related
// runs (events that were created or resolved by the run).
func StageRun(tx *sql.Tx, mu sync.Locker, directory string, sessionID string, runID int64) (*Stage, error) {
	runIDs, err := RelatedRunIDsTx(tx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	return StageRunIDs(tx, mu, directory, sessionID, runIDs...)
}

// StageRange stages the single shard that contains start.
func StageRange(tx *sql.Tx, mu sync.Locker, directory, sessionID string, start int64) (*Stage, error) {
	return StageStarts(tx, mu, directory, sessionID, []int64{start})
}

// RelatedRunIDsTx returns runID plus the runs connected to it through the
// event log and its trigger references.
func RelatedRunIDsTx(tx *sql.Tx, sessionID string, runID int64) ([]int64, error) {
	runIDs := []int64{runID}
	rows, err := tx.Query(`SELECT DISTINCT created_run_id FROM event_log
		WHERE session_id = ? AND outcome_run_id = ?`, sessionID, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var related int64
		if err := rows.Scan(&related); err != nil {
			_ = rows.Close()
			return nil, err
		}
		runIDs = append(runIDs, related)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var triggers sql.NullString
	if err := tx.QueryRow(`SELECT triggers_json FROM runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&triggers); err != nil {
		return nil, err
	}
	if triggers.Valid {
		var values []struct {
			CreatedRunID int64 `json:"created_run_id"`
		}
		if json.Unmarshal([]byte(triggers.String), &values) == nil {
			for _, value := range values {
				runIDs = append(runIDs, value.CreatedRunID)
			}
		}
	}
	return runIDs, nil
}

// StageRunIDs stages every shard that contains one of runIDs.
func StageRunIDs(tx *sql.Tx, mu sync.Locker, directory, sessionID string, runIDs ...int64) (*Stage, error) {
	starts := make([]int64, 0, len(runIDs))
	seen := make(map[int64]bool, len(runIDs))
	for _, runID := range runIDs {
		if runID <= 0 {
			continue
		}
		start, _ := Range(runID)
		if !seen[start] {
			seen[start] = true
			starts = append(starts, start)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	return StageStarts(tx, mu, directory, sessionID, starts)
}

// StageStarts stages the shards that contain each start.
func StageStarts(tx *sql.Tx, mu sync.Locker, directory, sessionID string, starts []int64) (*Stage, error) {
	targets := make([]Target, 0, len(starts))
	for _, start := range starts {
		targets = append(targets, Target{SessionID: sessionID, Start: start})
	}
	return StageTargets(tx, mu, directory, targets)
}

// StageTargets rewrites the history shards named by targets and records the
// previous file contents for rollback.
func StageTargets(tx *sql.Tx, mu sync.Locker, directory string, targets []Target) (*Stage, error) {
	type projection struct {
		sessionID string
		start     int64
		end       int64
		name      string
		path      string
		body      []byte
	}
	deduplicated := make([]Target, 0, len(targets))
	seen := make(map[Target]bool, len(targets))
	for _, target := range targets {
		if target.SessionID == "" || target.Start <= 0 || seen[target] {
			continue
		}
		seen[target] = true
		deduplicated = append(deduplicated, target)
	}
	sort.Slice(deduplicated, func(i, j int) bool {
		if deduplicated[i].SessionID == deduplicated[j].SessionID {
			return deduplicated[i].Start < deduplicated[j].Start
		}
		return deduplicated[i].SessionID < deduplicated[j].SessionID
	})
	projections := make([]projection, 0, len(deduplicated))
	for _, target := range deduplicated {
		end := target.Start + ShardSize - 1
		runs, err := RunsInTx(tx, target.SessionID, target.Start, end)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			continue
		}
		encoded, err := json.Marshal(Shard{SessionID: target.SessionID, StartRunID: target.Start, EndRunID: end, Runs: runs})
		if err != nil {
			return nil, err
		}
		name := FileName(target.SessionID, target.Start, end)
		projections = append(projections, projection{
			sessionID: target.SessionID, start: target.Start, end: end, name: name,
			path: filepath.Join(directory, name), body: append(encoded, '\n'),
		})
	}
	stage := &Stage{mu: mu, files: make([]stagedFile, 0, len(projections))}
	if len(projections) == 0 {
		stage.closed = true
		return stage, nil
	}
	mu.Lock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, projection := range projections {
		previous, readErr := os.ReadFile(projection.path)
		existed := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, errors.Join(readErr, stage.Finish(false))
		}
		if err := atomicWrite(projection.path, projection.body); err != nil {
			return nil, errors.Join(err, stage.Finish(false))
		}
		stage.files = append(stage.files, stagedFile{path: projection.path, previous: previous, existed: existed})
		if _, err := tx.Exec(`INSERT INTO run_history_files
			(session_id, start_run_id, end_run_id, file_path, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id, start_run_id) DO UPDATE SET
			end_run_id = excluded.end_run_id, file_path = excluded.file_path, updated_at = excluded.updated_at`,
			projection.sessionID, projection.start, projection.end, projection.name, now, now); err != nil {
			return nil, errors.Join(err, stage.Finish(false))
		}
	}
	return stage, nil
}

// LatestEventLogIDTx returns the highest event log id, or zero when empty.
func LatestEventLogIDTx(tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM event_log`).Scan(&id)
	return id, err
}

// StageEventSince stages the shards of events written after afterID.
func StageEventSince(tx *sql.Tx, mu sync.Locker, directory string, afterID int64) (*Stage, error) {
	rows, err := tx.Query(`SELECT session_id, created_run_id, outcome_run_id FROM event_log WHERE id > ? ORDER BY id`, afterID)
	if err != nil {
		return nil, err
	}
	targets := make([]Target, 0)
	for rows.Next() {
		var sessionID string
		var createdRunID int64
		var outcomeRunID sql.NullInt64
		if err := rows.Scan(&sessionID, &createdRunID, &outcomeRunID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		start, _ := Range(createdRunID)
		targets = append(targets, Target{SessionID: sessionID, Start: start})
		if outcomeRunID.Valid {
			start, _ = Range(outcomeRunID.Int64)
			targets = append(targets, Target{SessionID: sessionID, Start: start})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return StageTargets(tx, mu, directory, targets)
}

// Finish releases the history directory lock and restores the staged files
// when committed is false.
func (s *Stage) Finish(committed bool) error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	defer s.mu.Unlock()
	if committed {
		return nil
	}
	var restoreErr error
	for index := len(s.files) - 1; index >= 0; index-- {
		file := s.files[index]
		if !file.existed {
			if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				restoreErr = errors.Join(restoreErr, err)
			}
			continue
		}
		restoreErr = errors.Join(restoreErr, atomicWrite(file.path, file.previous))
	}
	return restoreErr
}

// RemoveTemps deletes history staging temporary files left by a crash.
func RemoveTemps(directory string) error {
	matches, err := filepath.Glob(filepath.Join(directory, ".history-*.tmp"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// atomicWrite writes body to path through a temporary file in the same
// directory so readers never observe a partial shard.
func atomicWrite(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".history-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return nil
}
