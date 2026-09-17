package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTicket35MemoryDeleteRejectsFaultedAndClosedRuntimeWithoutMutation(t *testing.T) {
	t.Run("faulted", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "agent.db")
		fixture := newRuntimeFixture()
		fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
				map[string]any{"type": "fact", "summary": "keep", "content": "keep", "importance": 1},
			}, nil))
		}
		runtime, _, _ := fixture.open(t, databasePath)
		session := createStoppedSession(t, runtime, nil)
		startRunForHistoryTest(t, runtime, session.ID, 1)
		memoryID, _ := listMemoryDetails(t, runtime.Handler(), session.ID, false)[0]["id"].(string)
		if err := os.Remove(filepath.Join(databasePath+".history", session.ID+"_1-20.json")); err != nil {
			t.Fatalf("remove History: %v", err)
		}
		status, _ := getHistoryShard(t, runtime.Handler(), session.ID, 1)
		if status != http.StatusServiceUnavailable || runtime.HealthError() == nil {
			t.Fatalf("History fault = status %d health %v", status, runtime.HealthError())
		}
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+memoryID, agentAdminToken, nil)
		if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
			t.Fatalf("DELETE on faulted Runtime = %d %s", status, raw)
		}
		if memory := getMemoryDetail(t, runtime.Handler(), session.ID, memoryID); memory["is_expired"] != false {
			t.Fatalf("faulted Runtime mutated Memory = %+v", memory)
		}
	})

	t.Run("closed", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "agent.db")
		fixture := newRuntimeFixture()
		fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
				map[string]any{"type": "fact", "summary": "keep", "content": "keep", "importance": 1},
			}, nil))
		}
		runtime, _, _ := fixture.open(t, databasePath)
		session := createStoppedSession(t, runtime, nil)
		startRunForHistoryTest(t, runtime, session.ID, 1)
		memoryID, _ := listMemoryDetails(t, runtime.Handler(), session.ID, false)[0]["id"].(string)
		if err := runtime.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+memoryID, agentAdminToken, nil)
		if status != http.StatusServiceUnavailable || !strings.Contains(string(raw), "PERSISTENCE_UNAVAILABLE") {
			t.Fatalf("DELETE on closed Runtime = %d %s", status, raw)
		}
		reopenedFixture := newRuntimeFixture()
		reopened, _, _ := reopenedFixture.open(t, databasePath)
		if memory := getMemoryDetail(t, reopened.Handler(), session.ID, memoryID); memory["is_expired"] != false {
			t.Fatalf("closed Runtime mutated Memory = %+v", memory)
		}
	})
}

func TestTicket35MemoryDeleteRacingCloseHasOnlyCommittedOrRejectedOutcome(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		t.Run(fmt.Sprintf("race-%02d", iteration), func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "agent.db")
			fixture := newRuntimeFixture()
			fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
				writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
					map[string]any{"type": "fact", "summary": "race", "content": "race", "importance": 1},
				}, nil))
			}
			runtime, _, _ := fixture.open(t, databasePath)
			session := createStoppedSession(t, runtime, nil)
			startRunForHistoryTest(t, runtime, session.ID, 1)
			memoryID, _ := listMemoryDetails(t, runtime.Handler(), session.ID, false)[0]["id"].(string)
			start := make(chan struct{})
			var wait sync.WaitGroup
			wait.Add(2)
			var deleteStatus int
			var deleteBody string
			go func() {
				defer wait.Done()
				<-start
				request := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+memoryID, nil)
				request.Header.Set("Authorization", "Bearer "+agentAdminToken)
				recorder := httptest.NewRecorder()
				runtime.Handler().ServeHTTP(recorder, request)
				deleteStatus, deleteBody = recorder.Code, recorder.Body.String()
			}()
			go func() {
				defer wait.Done()
				<-start
				_ = runtime.Close()
			}()
			close(start)
			wait.Wait()
			if deleteStatus != http.StatusOK && (deleteStatus != http.StatusServiceUnavailable || !strings.Contains(deleteBody, "PERSISTENCE_UNAVAILABLE")) {
				t.Fatalf("DELETE/Close race response = %d %s", deleteStatus, deleteBody)
			}
			reopenedFixture := newRuntimeFixture()
			reopened, _, _ := reopenedFixture.open(t, databasePath)
			memory := getMemoryDetail(t, reopened.Handler(), session.ID, memoryID)
			if (deleteStatus == http.StatusOK) != (memory["is_expired"] == true) {
				t.Fatalf("DELETE/Close outcome mismatch: status=%d Memory=%+v", deleteStatus, memory)
			}
		})
	}
}

func TestTicket35CanceledUserDeleteDoesNotExpireMemoryOrLatchPersistence(t *testing.T) {
	fixture := newRuntimeFixture()
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
			map[string]any{"type": "fact", "summary": "keep", "content": "keep", "importance": 1},
		}, nil))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	memoryID, _ := listMemoryDetails(t, runtime.Handler(), session.ID, false)[0]["id"].(string)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+memoryID, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	recorder := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestTimeout || !strings.Contains(recorder.Body.String(), "REQUEST_CANCELED") {
		t.Fatalf("canceled DELETE = %d %s", recorder.Code, recorder.Body.String())
	}
	if runtime.HealthError() != nil {
		t.Fatalf("canceled DELETE latched persistence fault: %v", runtime.HealthError())
	}
	if memories := listMemoryDetails(t, runtime.Handler(), session.ID, false); len(memories) != 1 || memories[0]["id"] != memoryID {
		t.Fatalf("canceled DELETE changed Memory = %+v", memories)
	}
}

func TestTicket35AgentAndUserExpirationAreAuditedIdempotentAndExcludedFromAgentReads(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	var agentExpiredID, userExpiredID string
	fixture.generate = func(w http.ResponseWriter, request *http.Request) {
		switch round.Add(1) {
		case 1:
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
				map[string]any{"type": "fact", "summary": "agent expires", "content": "agent-expired-content", "importance": 3},
				map[string]any{"type": "fact", "summary": "user expires", "content": "user-expired-content", "importance": 2},
			}, nil))
		case 2:
			writeFixtureJSON(w, http.StatusOK, finalResponse("agent expiration", "agent expiration", nil, []any{
				map[string]any{"id": agentExpiredID, "reason": "superseded by newer evidence"},
			}))
		case 3:
			writeToolCall(w, "list-after-expiration", "memory_list", map[string]any{"limit": 100})
		case 4:
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			result := lastToolResult(t, body)
			raw, _ := json.Marshal(result)
			if !strings.Contains(string(raw), `"memories":[]`) || strings.Contains(string(raw), "expired-content") {
				t.Errorf("Memory Tool exposed expired data: %s", raw)
			}
			writeFixtureJSON(w, http.StatusOK, finalResponse("retry user expiration", "retry user expiration", nil, []any{
				map[string]any{"id": userExpiredID, "reason": "agent also considers it stale"},
			}))
		default:
			t.Fatal("unexpected Generate request")
		}
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	memories := listMemoryDetails(t, runtime.Handler(), session.ID, false)
	for _, memory := range memories {
		switch memory["summary"] {
		case "agent expires":
			agentExpiredID, _ = memory["id"].(string)
		case "user expires":
			userExpiredID, _ = memory["id"].(string)
		}
	}
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	startRunForHistoryTest(t, runtime, session.ID, 2)
	active := listMemoryDetails(t, runtime.Handler(), session.ID, false)
	if len(active) != 1 || active[0]["id"] != userExpiredID {
		t.Fatalf("active Memories after agent expiration = %+v", active)
	}
	agentAudit := getMemoryDetail(t, runtime.Handler(), session.ID, agentExpiredID)
	if agentAudit["expired_source"] != "agent" || agentAudit["expired_run_id"] != float64(2) || agentAudit["expiration_reason"] != "superseded by newer evidence" {
		t.Fatalf("agent expiration audit = %+v", agentAudit)
	}
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+agentExpiredID, agentAdminToken, nil)
	afterAgentFirstDelete := decodeMap(t, raw)
	if status != http.StatusOK || afterAgentFirstDelete["expired_source"] != "agent" ||
		afterAgentFirstDelete["expired_at"] != agentAudit["expired_at"] || afterAgentFirstDelete["expired_run_id"] != float64(2) {
		t.Fatalf("user retry rewrote agent-first audit = %d %+v", status, afterAgentFirstDelete)
	}
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+userExpiredID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE Memory = %d %s", status, raw)
	}
	firstDelete := decodeMap(t, raw)
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+userExpiredID, agentAdminToken, nil)
	secondDelete := decodeMap(t, raw)
	if status != http.StatusOK || firstDelete["expired_at"] != secondDelete["expired_at"] || secondDelete["expired_source"] != "user" {
		t.Fatalf("idempotent DELETE = %d first=%+v second=%+v", status, firstDelete, secondDelete)
	}
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	startRunForHistoryTest(t, runtime, session.ID, 3)
	afterRetry := getMemoryDetail(t, runtime.Handler(), session.ID, userExpiredID)
	if afterRetry["expired_source"] != "user" || afterRetry["expired_at"] != firstDelete["expired_at"] || afterRetry["expired_run_id"] != nil {
		t.Fatalf("agent retry rewrote user audit = %+v", afterRetry)
	}
	expired := listMemoryDetails(t, runtime.Handler(), session.ID, true)
	if len(expired) != 2 {
		t.Fatalf("include_expired Memories = %+v", expired)
	}
}

func TestTicket35CrossSessionExpirationRejectsWholeFinalization(t *testing.T) {
	fixture := newRuntimeFixture()
	var round atomic.Int32
	var foreignID, ownID string
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		switch round.Add(1) {
		case 1:
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
				map[string]any{"type": "fact", "summary": "foreign", "content": "foreign", "importance": 1},
			}, nil))
			return
		case 2:
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed own", "seed own", []any{
				map[string]any{"type": "fact", "summary": "own", "content": "own", "importance": 1},
			}, nil))
			return
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("forged", "forged", []any{
			map[string]any{"type": "fact", "summary": "must roll back", "content": "must roll back", "importance": 1},
		}, []any{
			map[string]any{"id": ownID, "reason": "valid but transaction must roll back"},
			map[string]any{"id": foreignID, "reason": "not mine"},
		}))
	}
	runtime, _, _ := fixture.open(t, "")
	owner := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_owner", "name": "owner"})
	startRunForHistoryTest(t, runtime, owner.ID, 1)
	foreignID, _ = listMemoryDetails(t, runtime.Handler(), owner.ID, false)[0]["id"].(string)
	other := createStoppedSession(t, runtime, map[string]any{"account_id": "acc_other", "name": "other"})
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+other.ID+"/memories/"+foreignID, agentAdminToken, nil)
	if status != http.StatusNotFound || !strings.Contains(string(raw), "MEMORY_NOT_FOUND") {
		t.Fatalf("cross-Session DELETE = %d %s", status, raw)
	}
	startRunForHistoryTest(t, runtime, other.ID, 1)
	ownID, _ = listMemoryDetails(t, runtime.Handler(), other.ID, false)[0]["id"].(string)
	stopSessionForMemoryTest(t, runtime.Handler(), other.ID)
	status, _ = runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+other.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start forged Run = %d", status)
	}
	run := waitForRunStatus(t, runtime, other.ID, 2, "failed")
	if run.Error == nil || !strings.Contains(*run.Error, "FINALIZATION_INVALID") {
		t.Fatalf("forged expiration Run = %+v", run)
	}
	if memories := listMemoryDetails(t, runtime.Handler(), other.ID, true); len(memories) != 1 || memories[0]["id"] != ownID || memories[0]["is_expired"] != false {
		t.Fatalf("forged finalization partially changed Memories: %+v", memories)
	}
	if ownerMemory := getMemoryDetail(t, runtime.Handler(), owner.ID, foreignID); ownerMemory["is_expired"] != false {
		t.Fatalf("foreign Memory changed = %+v", ownerMemory)
	}
}

func TestTicket35UserDeleteRacingAgentFinalizationKeepsFirstExpirationProvenance(t *testing.T) {
	fixture := newRuntimeFixture()
	finalizationStarted := make(chan struct{})
	releaseFinalization := make(chan struct{})
	var round atomic.Int32
	var memoryID string
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if round.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
				map[string]any{"type": "fact", "summary": "race", "content": "race", "importance": 1},
			}, nil))
			return
		}
		close(finalizationStarted)
		<-releaseFinalization
		writeFixtureJSON(w, http.StatusOK, finalResponse("complete", "complete", nil, []any{
			map[string]any{"id": memoryID, "reason": "agent race"},
		}))
	}
	runtime, _, _ := fixture.open(t, "")
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	memoryID, _ = listMemoryDetails(t, runtime.Handler(), session.ID, false)[0]["id"].(string)
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	status, raw := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start racing Run = %d %s", status, raw)
	}
	waitSignal(t, finalizationStarted, "agent finalization")
	status, raw = runtimeRequest(t, runtime.Handler(), http.MethodDelete, "/api/v1/sessions/"+session.ID+"/memories/"+memoryID, agentAdminToken, nil)
	if status != http.StatusOK {
		t.Fatalf("racing DELETE = %d %s", status, raw)
	}
	deleted := decodeMap(t, raw)
	close(releaseFinalization)
	waitForRunStatus(t, runtime, session.ID, 2, "completed")
	audit := getMemoryDetail(t, runtime.Handler(), session.ID, memoryID)
	if audit["expired_source"] != "user" || audit["expired_run_id"] != nil || audit["expired_at"] != deleted["expired_at"] {
		t.Fatalf("race rewrote first expiration provenance = %+v", audit)
	}
}

func TestTicket35ExpirationWriteFailureRollsBackCandidateExpirationAndCompletion(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.db")
	fixture := newRuntimeFixture()
	var round atomic.Int32
	var memoryID string
	fixture.generate = func(w http.ResponseWriter, _ *http.Request) {
		if round.Add(1) == 1 {
			writeFixtureJSON(w, http.StatusOK, finalResponse("seed", "seed", []any{
				map[string]any{"type": "fact", "summary": "durable", "content": "durable", "importance": 1},
			}, nil))
			return
		}
		writeFixtureJSON(w, http.StatusOK, finalResponse("would complete", "would complete", []any{
			map[string]any{"type": "fact", "summary": "must roll back", "content": "must roll back", "importance": 1},
		}, []any{map[string]any{"id": memoryID, "reason": "replace"}}))
	}
	runtime, _, _ := fixture.open(t, databasePath)
	session := createStoppedSession(t, runtime, nil)
	startRunForHistoryTest(t, runtime, session.ID, 1)
	memoryID, _ = listMemoryDetails(t, runtime.Handler(), session.ID, false)[0]["id"].(string)
	stopSessionForMemoryTest(t, runtime.Handler(), session.ID)
	installRejectingTrigger(t, databasePath, "reject_agent_expiration", `
		BEFORE UPDATE OF is_expired ON memories WHEN NEW.expired_source = 'agent'
		BEGIN SELECT RAISE(FAIL, 'reject expiration'); END`)
	status, _ := runtimeRequest(t, runtime.Handler(), http.MethodPost, "/api/v1/sessions/"+session.ID+"/start", agentAdminToken, nil)
	if status != http.StatusAccepted {
		t.Fatalf("start failing Run = %d", status)
	}
	run := waitForRunStatus(t, runtime, session.ID, 2, "failed")
	if run.Error == nil || !strings.Contains(*run.Error, "PERSISTENCE_ERROR") {
		t.Fatalf("expiration failure Run = %+v", run)
	}
	memories := listMemoryDetails(t, runtime.Handler(), session.ID, true)
	if len(memories) != 1 || memories[0]["id"] != memoryID || memories[0]["is_expired"] != false {
		t.Fatalf("expiration transaction was partial = %+v", memories)
	}
}
