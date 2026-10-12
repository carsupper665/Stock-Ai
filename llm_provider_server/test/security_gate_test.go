package test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSecurityGateRejectsOversizedProviderRequest(t *testing.T) {
	server := openTestServer(t, time.Second)
	defer server.Close()
	body := []byte(`{"id":"fixture","name":"` + strings.Repeat("x", (1<<20)+1) + `"}`)

	response := request(t, server.Handler(), http.MethodPost, "/v1/providers", adminToken, body)

	assertError(t, response, http.StatusBadRequest, "REQUEST_INVALID")
}
