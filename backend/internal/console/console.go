package console

import (
	"crypto/subtle"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
)

type Config struct {
	UserToken           string
	AgentToken          string
	GatewayAdminToken   string
	GatewayRuntimeToken string
	BackendURL          string
	AgentURL            string
	GatewayURL          string
	Public              fs.FS
}

// The console owns the browser-facing authorization boundary. Only an authenticated
// USER request can be upgraded to the private Agent/Gateway service credentials.
func New(config Config) (http.Handler, error) {
	if config.UserToken == "" || config.AgentToken == "" || config.GatewayAdminToken == "" || config.GatewayRuntimeToken == "" || config.Public == nil {
		return nil, errors.New("console credentials and public filesystem are required")
	}
	backend, err := proxy(config.BackendURL, "", nil)
	if err != nil {
		return nil, err
	}
	agent, err := proxy(config.AgentURL, "/agent", func(*http.Request) string { return config.AgentToken })
	if err != nil {
		return nil, err
	}
	gateway, err := proxy(config.GatewayURL, "/llm", func(request *http.Request) string {
		if request.URL.Path == "/llm/v1/models" && request.Method == http.MethodGet {
			return config.GatewayRuntimeToken
		}
		return config.GatewayAdminToken
	})
	if err != nil {
		return nil, err
	}
	files := http.FileServerFS(config.Public)
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		p := request.URL.Path
		for _, part := range strings.Split(p, "/") {
			if strings.HasPrefix(part, ".") || strings.ContainsAny(part, `\`) {
				http.NotFound(w, request)
				return
			}
		}
		switch {
		case p == "/v1" || strings.HasPrefix(p, "/v1/"):
			w.Header().Set("Cache-Control", "no-store")
			backend.ServeHTTP(w, request)
		case strings.HasPrefix(p, "/agent/") || strings.HasPrefix(p, "/llm/"):
			w.Header().Set("Cache-Control", "no-store")
			expected := "Bearer " + config.UserToken
			if subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte(expected)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized","message":"Valid USER credentials are required."}`))
				return
			}
			if strings.HasPrefix(p, "/agent/api/v1/") || (p == "/agent/health" && request.Method == http.MethodGet) {
				agent.ServeHTTP(w, request)
				return
			}
			if strings.HasPrefix(p, "/llm/v1/") || (p == "/llm/healthz" && request.Method == http.MethodGet) {
				gateway.ServeHTTP(w, request)
				return
			}
			http.NotFound(w, request)
		default:
			if request.Method != http.MethodGet && request.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimPrefix(path.Clean(p), "/")
			if name == "" {
				name = "index.html"
			}
			info, err := fs.Stat(config.Public, name)
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
	}), nil
}

func proxy(address, prefix string, credential func(*http.Request) string) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("console upstream must be an HTTP(S) URL without credentials, query or fragment")
	}
	return &httputil.ReverseProxy{
		ModifyResponse: func(response *http.Response) error {
			if credential == nil || (response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden) {
				return nil
			}
			response.Body.Close()
			body := `{"error":"upstream_authentication_failed","message":"Service credentials were rejected."}`
			response.StatusCode = http.StatusBadGateway
			response.Status = "502 Bad Gateway"
			response.Body = io.NopCloser(strings.NewReader(body))
			response.ContentLength = int64(len(body))
			response.Header = make(http.Header)
			response.Header.Set("Content-Type", "application/json")
			return nil
		},
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(u)
			request.Out.URL.Path = strings.TrimRight(u.Path, "/") + strings.TrimPrefix(request.In.URL.Path, prefix)
			request.Out.URL.RawPath = ""
			if credential != nil {
				request.Out.Header.Set("Authorization", "Bearer "+credential(request.In))
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream_unavailable","message":"Service is unavailable."}`))
		},
	}, nil
}
