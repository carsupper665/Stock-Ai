package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"server/utils"

	"github.com/gin-gonic/gin"
)

func TestNewHTTPServer_Healthz(t *testing.T) {
	server := newTestHTTPServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["message"] != "ok" {
		t.Fatalf("expected message ok, got %q", body["message"])
	}
}

func TestNewHTTPServer_Time(t *testing.T) {
	server := newTestHTTPServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/time", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["time"] == "" {
		t.Fatal("expected non-empty time field")
	}
}

func newTestHTTPServer(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)

	origSessionSecret := utils.SessionSecret
	origFrontendURL := utils.FrontEndUrl

	utils.SessionSecret = "test-session-secret"
	utils.FrontEndUrl = "http://localhost:3000"

	t.Cleanup(func() {
		utils.SessionSecret = origSessionSecret
		utils.FrontEndUrl = origFrontendURL
	})

	return newHTTPServer()
}
