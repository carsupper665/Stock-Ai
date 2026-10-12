package test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityGateRejectsOversizedAgentRequest(t *testing.T) {
	fixture := newRuntimeFixture()
	runtime, _, _ := fixture.open(t, "")
	body := `{"name":"` + strings.Repeat("x", (1<<20)+1) + `","account_id":"acc_fixture","model_name":"fixture-model"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+agentAdminToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	runtime.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "at most 1 MiB") {
		t.Fatalf("oversized request status=%d body=%s", response.Code, response.Body.String())
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.modelRequests != 0 || len(fixture.backendRequests) != 0 {
		t.Fatalf("oversized request reached dependencies: models=%d backend=%v", fixture.modelRequests, fixture.backendRequests)
	}
}
