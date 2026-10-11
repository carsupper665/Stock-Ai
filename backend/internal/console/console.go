package console

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"

	"backend/internal/auth"
)

const maxPrivateResponseBytes = 16 << 20

type tokenVerifier interface {
	Verify(context.Context, string) (string, error)
}

type Config struct {
	OIDC                *auth.OIDCSettings
	AgentToken          string
	GatewayAdminToken   string
	GatewayRuntimeToken string
	BackendURL          string
	AgentURL            string
	GatewayURL          string
	Public              fs.FS
}

type credentialContextKey struct{}

// New creates the browser-facing authorization boundary. Gateway identity
// headers are only assertions to verify; they are never forwarded upstream.
// An explicit Bearer on /v1 is instead an opaque Account delegation and is
// passed only to Backend. Browser Authorization is forbidden on /agent and /llm.
func New(config Config) (http.Handler, error) {
	if config.AgentToken == "" || config.GatewayAdminToken == "" || config.GatewayRuntimeToken == "" || config.Public == nil {
		return nil, errors.New("console credentials and public filesystem are required")
	}
	if config.AgentToken == config.GatewayAdminToken || config.AgentToken == config.GatewayRuntimeToken || config.GatewayAdminToken == config.GatewayRuntimeToken {
		return nil, errors.New("console credential roles must be distinct")
	}
	if err := validateOIDC(config.OIDC); err != nil {
		return nil, err
	}
	verifier := auth.NewOIDCVerifier(*config.OIDC)
	privateCredentials := []string{config.AgentToken, config.GatewayAdminToken, config.GatewayRuntimeToken}
	backend, err := proxy(config.BackendURL, "", func(request *http.Request) string { return requestCredential(request) }, false, nil)
	if err != nil {
		return nil, err
	}
	agent, err := proxy(config.AgentURL, "/agent", func(*http.Request) string { return "Bearer " + config.AgentToken }, true, privateCredentials)
	if err != nil {
		return nil, err
	}
	gateway, err := proxy(config.GatewayURL, "/llm", func(request *http.Request) string {
		if (request.URL.Path == "/llm/v1/models" && request.Method == http.MethodGet) ||
			(request.URL.Path == "/llm/v1/generate" && request.Method == http.MethodPost) {
			return "Bearer " + config.GatewayRuntimeToken
		}
		return "Bearer " + config.GatewayAdminToken
	}, true, privateCredentials)
	if err != nil {
		return nil, err
	}
	files := http.FileServerFS(config.Public)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		p := request.URL.Path
		for _, part := range strings.Split(p, "/") {
			if strings.HasPrefix(part, ".") || strings.Contains(part, `\`) {
				http.NotFound(w, request)
				return
			}
		}
		switch {
		case p == "/v1" || strings.HasPrefix(p, "/v1/"):
			w.Header().Set("Cache-Control", "no-store")
			credential, ok := backendCredential(w, request, verifier)
			if !ok {
				return
			}
			backend.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), credentialContextKey{}, credential)))
		case strings.HasPrefix(p, "/agent/") || strings.HasPrefix(p, "/llm/"):
			w.Header().Set("Cache-Control", "no-store")
			if request.Header.Get("Authorization") != "" {
				writeAuthError(w, http.StatusForbidden, "forbidden", "Account delegation is available only on Backend /v1 routes.")
				return
			}
			if _, ok := verifiedGatewayToken(request, verifier); !ok {
				writeAuthError(w, http.StatusUnauthorized, "unauthorized", "A valid USER session is required.")
				return
			}
			if strings.HasPrefix(p, "/agent/") {
				if !authorizePrivateRoute(w, request, agentMethods(p)) {
					return
				}
				agent.ServeHTTP(w, request)
				return
			}
			if strings.HasPrefix(p, "/llm/") {
				if !authorizePrivateRoute(w, request, llmMethods(p)) {
					return
				}
				gateway.ServeHTTP(w, request)
				return
			}
		default:
			servePublic(files, config.Public, w, request)
		}
	}), nil
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
	case len(parts) == 4 && parts[1] == "runs" && parts[3] == "history":
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
	case "/v1/generate":
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

func validateOIDC(settings *auth.OIDCSettings) error {
	if settings == nil || strings.TrimSpace(settings.Issuer) == "" || strings.TrimSpace(settings.Audience) == "" ||
		strings.TrimSpace(settings.Subject) == "" || strings.TrimSpace(settings.JWKSURL) == "" || strings.TrimSpace(settings.TokenType) == "" {
		return errors.New("complete OIDC verification settings are required")
	}
	issuer, issuerErr := url.Parse(settings.Issuer)
	jwks, jwksErr := url.Parse(settings.JWKSURL)
	if issuerErr != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return errors.New("console OIDC issuer must be an exact HTTPS URL")
	}
	if jwksErr != nil || jwks.Scheme != "https" || jwks.Host == "" || jwks.User != nil || jwks.Fragment != "" {
		return errors.New("console OIDC JWKS endpoint must be HTTPS")
	}
	return nil
}

func backendCredential(w http.ResponseWriter, request *http.Request, verifier tokenVerifier) (string, bool) {
	if raw := request.Header.Get("Authorization"); raw != "" {
		token, ok := strictBearer(raw)
		if !ok || strings.Count(token, ".") == 2 {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "A valid Account credential is required.")
			return "", false
		}
		return raw, true
	}
	token, ok := verifiedGatewayToken(request, verifier)
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized", "A valid USER session is required.")
		return "", false
	}
	return "Bearer " + token, true
}

func verifiedGatewayToken(request *http.Request, verifier tokenVerifier) (string, bool) {
	token, tokenOK := singleHeader(request.Header, "X-Auth-Request-Access-Token")
	claimedSubject, subjectOK := singleHeader(request.Header, "X-Auth-Request-Subject")
	if !tokenOK || !subjectOK {
		return "", false
	}
	verifiedSubject, err := verifier.Verify(request.Context(), token)
	if err != nil || verifiedSubject == "" || verifiedSubject != claimedSubject {
		return "", false
	}
	return token, true
}

func singleHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value != ""
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

func servePublic(files http.Handler, public fs.FS, w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	info, err := fs.Stat(public, name)
	if err != nil || info.IsDir() {
		if path.Ext(name) != "" {
			http.NotFound(w, request)
			return
		}
		clone := request.Clone(request.Context())
		clone.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, clone)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	files.ServeHTTP(w, request)
}

func requestCredential(request *http.Request) string {
	value, _ := request.Context().Value(credentialContextKey{}).(string)
	return value
}

func proxy(address, prefix string, credential func(*http.Request) string, private bool, secrets []string) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("console upstream must be an HTTP(S) URL without credentials, query or fragment")
	}
	return &httputil.ReverseProxy{
		ModifyResponse: func(response *http.Response) error {
			if private {
				if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
					replaceResponse(response, http.StatusBadGateway, `{"error":"upstream_authentication_failed","message":"Service credentials were rejected."}`)
					return nil
				}
				if responseDisclosesCredential(response, secrets) {
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
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream_unavailable","message":"Service is unavailable."}`))
		},
	}, nil
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
