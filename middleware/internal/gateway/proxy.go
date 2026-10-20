package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const maxPrivateResponseBytes = 16 << 20

type credentialContextKey struct{}

// newProxies builds the upstream reverse proxies. Browser identity is only the
// signed session cookie; it is never forwarded upstream. An explicit Bearer on
// /v1 is instead an opaque Account delegation and is passed only to Backend.
// Browser Authorization is forbidden on /agent and /llm.
func (s *Server) newProxies() error {
	privateCredentials := []string{s.cfg.AgentAdminToken, s.cfg.LLMAdminToken, s.cfg.LLMRuntimeToken}
	var err error
	if s.backend, err = proxy("backend", s.cfg.BackendURL, "", func(request *http.Request) string { return requestCredential(request) }, false, nil); err != nil {
		return err
	}
	if s.agent, err = proxy("agent-server", s.cfg.AgentURL, "/agent", func(*http.Request) string { return "Bearer " + s.cfg.AgentAdminToken }, true, privateCredentials); err != nil {
		return err
	}
	s.llm, err = proxy("llm-provider-server", s.cfg.LLMURL, "/llm", func(request *http.Request) string {
		if request.URL.Path == "/llm/v1/models" && request.Method == http.MethodGet {
			return "Bearer " + s.cfg.LLMRuntimeToken
		}
		return "Bearer " + s.cfg.LLMAdminToken
	}, true, privateCredentials)
	return err
}

func (s *Server) serveBackend(w http.ResponseWriter, request *http.Request) {
	credential, ok := s.backendCredential(w, request)
	if !ok {
		return
	}
	s.backend.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), credentialContextKey{}, credential)))
}

func (s *Server) serveAgent(w http.ResponseWriter, request *http.Request) {
	if !s.privateSession(w, request) || !authorizePrivateRoute(w, request, agentMethods(request.URL.Path)) {
		return
	}
	s.agent.ServeHTTP(w, request)
}

func (s *Server) serveLLM(w http.ResponseWriter, request *http.Request) {
	if !s.privateSession(w, request) || !authorizePrivateRoute(w, request, llmMethods(request.URL.Path)) {
		return
	}
	s.llm.ServeHTTP(w, request)
}

func (s *Server) privateSession(w http.ResponseWriter, request *http.Request) bool {
	if request.Header.Get("Authorization") != "" {
		writeAuthError(w, http.StatusForbidden, "forbidden", "Account delegation is available only on Backend /v1 routes.")
		return false
	}
	if _, ok := s.current(request); !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized", "A valid USER session is required.")
		return false
	}
	return true
}

func authorizePrivateRoute(w http.ResponseWriter, request *http.Request, methods []string) bool {
	if len(methods) == 0 {
		http.NotFound(w, request)
		return false
	}
	for _, method := range methods {
		if request.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

func agentMethods(rawPath string) []string {
	path := strings.TrimSuffix(strings.TrimPrefix(rawPath, "/agent"), "/")
	if path == "/health" {
		return []string{http.MethodGet}
	}
	parts := routeParts(path, "/api/v1/sessions")
	if parts == nil {
		return nil
	}
	switch {
	case len(parts) == 0:
		return []string{http.MethodGet, http.MethodPost}
	case len(parts) == 1:
		return []string{http.MethodGet, http.MethodDelete}
	case len(parts) == 2 && (parts[1] == "start" || parts[1] == "stop"):
		return []string{http.MethodPost}
	case len(parts) == 2 && (parts[1] == "runs" || parts[1] == "events" || parts[1] == "memories" ||
		parts[1] == "ledger" || parts[1] == "positions" || parts[1] == "usage"):
		return []string{http.MethodGet}
	case len(parts) == 3 && parts[1] == "runs":
		return []string{http.MethodGet}
	case len(parts) == 3 && parts[1] == "events":
		return []string{http.MethodDelete}
	case len(parts) == 3 && parts[1] == "memories":
		return []string{http.MethodGet, http.MethodDelete}
	case len(parts) == 3 && parts[1] == "tools" && parts[2] == "stats":
		return []string{http.MethodGet}
	case len(parts) == 4 && parts[1] == "runs" && (parts[3] == "history" || parts[3] == "stream"):
		return []string{http.MethodGet}
	case len(parts) == 4 && parts[1] == "runs" && parts[3] == "retry":
		return []string{http.MethodPost}
	default:
		return nil
	}
}

func llmMethods(rawPath string) []string {
	path := strings.TrimSuffix(strings.TrimPrefix(rawPath, "/llm"), "/")
	switch path {
	case "/healthz", "/v1/models", "/v1/harnesses":
		return []string{http.MethodGet}
	case "/v1/providers":
		return []string{http.MethodGet, http.MethodPost}
	case "/v1/model-catalog":
		return []string{http.MethodPost}
	}
	parts := routeParts(path, "/v1/providers")
	switch {
	case len(parts) == 1:
		return []string{http.MethodGet, http.MethodPatch, http.MethodDelete}
	case len(parts) == 2 && parts[1] == "test":
		return []string{http.MethodPost}
	case len(parts) == 2 && parts[1] == "tokens":
		return []string{http.MethodGet, http.MethodPost}
	case len(parts) == 3 && parts[1] == "tokens":
		return []string{http.MethodPatch, http.MethodDelete}
	}
	parts = routeParts(path, "/v1/model-catalog")
	if len(parts) == 1 {
		return []string{http.MethodPut, http.MethodDelete}
	}
	return nil
}

func routeParts(path, base string) []string {
	if path == base {
		return []string{}
	}
	if !strings.HasPrefix(path, base+"/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(path, base+"/"), "/")
	for _, part := range parts {
		if part == "" {
			return nil
		}
	}
	return parts
}

func (s *Server) backendCredential(w http.ResponseWriter, request *http.Request) (string, bool) {
	if raw := request.Header.Get("Authorization"); raw != "" {
		token, ok := strictBearer(raw)
		if !ok || strings.Count(token, ".") == 2 {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "A valid Account credential is required.")
			return "", false
		}
		return raw, true
	}
	if _, ok := s.current(request); !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized", "A valid USER session is required.")
		return "", false
	}
	return "Bearer " + s.cfg.BackendUserToken, true
}

func strictBearer(raw string) (string, bool) {
	if len(raw) < 8 || !strings.EqualFold(raw[:7], "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(raw[7:])
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n,")
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `","message":"` + message + `"}`))
}

func requestCredential(request *http.Request) string {
	value, _ := request.Context().Value(credentialContextKey{}).(string)
	return value
}

func proxy(name, address, prefix string, credential func(*http.Request) string, private bool, secrets []string) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("middleware upstream must be an HTTP(S) URL without credentials, query or fragment")
	}
	return &httputil.ReverseProxy{
		ModifyResponse: func(response *http.Response) error {
			if private {
				if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
					log.Printf("upstream %s rejected the service credential: %s %s -> %d", name, response.Request.Method, response.Request.URL.Path, response.StatusCode)
					replaceResponse(response, http.StatusBadGateway, `{"error":"upstream_authentication_failed","message":"Service credentials were rejected."}`)
					return nil
				}
				if !liveRunStream(response.Request.URL.Path) && responseDisclosesCredential(response, secrets) {
					log.Printf("upstream %s response failed the credential disclosure check and was withheld: %s %s", name, response.Request.Method, response.Request.URL.Path)
					replaceResponse(response, http.StatusBadGateway, `{"error":"upstream_response_rejected","message":"Service returned an unsafe response."}`)
					return nil
				}
			}
			stripBoundaryHeaders(response.Header)
			return nil
		},
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(u)
			request.Out.URL.Path = strings.TrimRight(u.Path, "/") + strings.TrimPrefix(request.In.URL.Path, prefix)
			request.Out.URL.RawPath = ""
			stripBoundaryHeaders(request.Out.Header)
			request.Out.Trailer = nil
			if value := credential(request.In); value != "" {
				request.Out.Header.Set("Authorization", value)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, request *http.Request, err error) {
			log.Printf("upstream %s unavailable: %s %s: %v", name, request.Method, request.URL.Path, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream_unavailable","message":"Service is unavailable."}`))
		},
	}, nil
}

// liveRunStream is the Agent Server's Run stream (Server-Sent Events of model text). It is passed
// through as it arrives: reading it whole for the disclosure check would hold every event until
// the Run ends. Services are responsible for never putting a credential in a response.
func liveRunStream(path string) bool {
	parts := routeParts(path, "/api/v1/sessions")
	return len(parts) == 4 && parts[1] == "runs" && parts[3] == "stream"
}

func stripBoundaryHeaders(header http.Header) {
	for name := range header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "proxy-authorization" || lower == "proxy-authenticate" || lower == "accept-encoding" ||
			lower == "cookie" || lower == "set-cookie" || lower == "forwarded" || lower == "x-real-ip" ||
			lower == "trailer" || lower == "x-original-uri" || lower == "x-forwarded-client-cert" || lower == "x-remote-user" ||
			lower == "remote-user" || lower == "x-user" || lower == "x-email" ||
			strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-auth-request-") {
			header.Del(name)
		}
	}
}

func responseDisclosesCredential(response *http.Response, secrets []string) bool {
	for _, values := range response.Header {
		for _, value := range values {
			if containsSecret(value, secrets) {
				response.Body.Close()
				return true
			}
		}
	}
	bodyReader := io.Reader(response.Body)
	encoding := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Encoding")))
	var gzipReader *gzip.Reader
	if encoding != "" && encoding != "identity" {
		if encoding != "gzip" {
			response.Body.Close()
			return true
		}
		var err error
		gzipReader, err = gzip.NewReader(response.Body)
		if err != nil {
			response.Body.Close()
			return true
		}
		bodyReader = gzipReader
	}
	body, err := io.ReadAll(io.LimitReader(bodyReader, maxPrivateResponseBytes+1))
	if gzipReader != nil {
		_ = gzipReader.Close()
	}
	response.Body.Close()
	if err != nil || len(body) > maxPrivateResponseBytes || containsSecret(string(body), secrets) {
		return true
	}
	for _, values := range response.Trailer {
		for _, value := range values {
			if containsSecret(value, secrets) {
				return true
			}
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Del("Content-Encoding")
	response.Header.Del("Content-Length")
	response.Header.Del("Trailer")
	response.TransferEncoding = nil
	response.Trailer = nil
	response.Uncompressed = true
	return false
}

func containsSecret(value string, secrets []string) bool {
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func replaceResponse(response *http.Response, status int, body string) {
	if response.Body != nil {
		response.Body.Close()
	}
	response.StatusCode = status
	response.Status = strconv.Itoa(status) + " " + http.StatusText(status)
	response.Body = io.NopCloser(strings.NewReader(body))
	response.ContentLength = int64(len(body))
	response.TransferEncoding = nil
	response.Trailer = nil
	response.Uncompressed = true
	response.Header = make(http.Header)
	response.Header.Set("Content-Type", "application/json")
}
