package test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/api"
	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"
)

func TestAgentAccountLookupWorksWithStaticUSERDisabled(t *testing.T) {
	t.Setenv("ALLOW_STATIC_USER_TOKEN", "false")
	t.Setenv("OIDC_ISSUER", oidcIssuer)
	t.Setenv("OIDC_AUDIENCE", oidcAudience)
	t.Setenv("OIDC_USER_SUBJECT", oidcSubject)
	t.Setenv("OIDC_JWKS_URL", "https://idp.example/jwks")
	t.Setenv("AGENT_SERVICE_TOKEN", "dedicated-agent-account-reader")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	fixture := newTradingAPI(t, "")
	err = fixture.store.CreateAccount(context.Background(), &database.Account{
		ID: "acc_service", UserName: "service", Token: "account-only-token",
		InitialBalance: 100, Balance: 100, Status: database.AccountActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger, err := logging.New("service-auth", t.TempDir(), 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	logger.SetConsole(io.Discard)
	defer logger.Close()
	handler := api.New(cfg, fixture.store, fixture.prices, fixture.trader, logger)
	cfg.AgentServiceToken = "invalid.claims.signature"
	request := httptest.NewRequest("GET", "/v1/accounts/acc_service", nil)
	request.Header.Set("Authorization", "Bearer invalid.claims.signature")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("JWT-shaped credential bypassed OIDC")
	}
	cfg.AgentServiceToken = "dedicated-agent-account-reader"
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/v1/accounts/acc_service", "dedicated-agent-account-reader", 200},
		{"GET", "/v1/accounts/acc_service", "wrong-token", 401},
		{"GET", "/v1/accounts/acc_service", "", 401},
		{"GET", "/v1/accounts", "dedicated-agent-account-reader", 401},
		{"DELETE", "/v1/accounts/acc_service", "dedicated-agent-account-reader", 401},
		{"POST", "/v1/orders", "dedicated-agent-account-reader", 401},
		{"GET", "/v1/me", "dedicated-agent-account-reader", 401},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, out.Code, tc.want)
		}
		if out.Code == http.StatusOK {
			var account struct{ ID, Token string }
			if json.Unmarshal(out.Body.Bytes(), &account) != nil || account.ID != "acc_service" || account.Token != "account-only-token" {
				t.Fatal("Agent lookup did not return the bound Account runtime token")
			}
		}
	}
}
