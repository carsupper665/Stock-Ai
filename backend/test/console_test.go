package test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"backend/internal/console"
)

func TestConsoleAuthenticatesBeforeInjectingServiceCredentials(t *testing.T) {
	requests := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.RequestURI() + " " + r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	handler, err := console.New(console.Config{UserToken: "user", AgentToken: "agent-private", GatewayAdminToken: "admin-private", GatewayRuntimeToken: "runtime-private", BackendURL: upstream.URL, AgentURL: upstream.URL, GatewayURL: upstream.URL, Public: fstest.MapFS{"index.html": {Data: []byte("<main>console</main>")}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/agent/api/v1/sessions", "/llm/v1/providers", "/llm/v1/model-catalog"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("POST", route, strings.NewReader("{}")))
		if recorder.Code != 401 {
			t.Fatalf("unauthenticated %s=%d", route, recorder.Code)
		}
	}
	select {
	case value := <-requests:
		t.Fatal("unauthorized upstream call", value)
	default:
	}
	for _, test := range []struct{ path, method, want string }{
		{"/agent/health", "GET", "GET /health Bearer agent-private"},
		{"/llm/healthz", "GET", "GET /healthz Bearer admin-private"},
		{"/agent/api/v1/sessions?page=2", "GET", "GET /api/v1/sessions?page=2 Bearer agent-private"},
		{"/llm/v1/models", "GET", "GET /v1/models Bearer runtime-private"},
		{"/llm/v1/model-catalog", "POST", "POST /v1/model-catalog Bearer admin-private"},
		{"/v1/account", "GET", "GET /v1/account Bearer user"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer user")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != 200 {
			t.Fatal(recorder.Code, recorder.Body)
		}
		if got := <-requests; got != test.want {
			t.Fatalf("wire=%s want%s", got, test.want)
		}
	}
	for _, path := range []string{"/.env", "/../.env", "/assets/missing.js"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != 404 {
			t.Fatalf("%s=%d", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/sessions/s_1", nil))
	bytes, _ := io.ReadAll(recorder.Result().Body)
	if recorder.Code != 200 || !strings.Contains(string(bytes), "console") {
		t.Fatal(recorder.Code, string(bytes))
	}
}

func TestConsolePrivateUpstreamAuthFailureDoesNotInvalidateUser(t *testing.T) {
	for _, status := range []int{401,403} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.WriteHeader(status);w.Write([]byte(`{"error":"private credential rejected"}`))}))
		handler,err:=console.New(console.Config{UserToken:"user",AgentToken:"agent",GatewayAdminToken:"admin",GatewayRuntimeToken:"runtime",BackendURL:upstream.URL,AgentURL:upstream.URL,GatewayURL:upstream.URL,Public:fstest.MapFS{"index.html":{Data:[]byte("console")}}})
		if err!=nil{t.Fatal(err)}
		for _, path:=range []string{"/agent/health","/llm/healthz","/agent/api/v1/sessions","/llm/v1/models"}{
			r:=httptest.NewRequest("GET",path,nil);r.Header.Set("Authorization","Bearer user")
			w:=httptest.NewRecorder();handler.ServeHTTP(w,r)
			if w.Code!=502 || !strings.Contains(w.Body.String(),"upstream_authentication_failed"){t.Fatalf("%s returned %d %s",path,w.Code,w.Body)}
		}
		upstream.Close()
	}
}
