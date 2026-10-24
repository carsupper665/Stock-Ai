package gateway

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The Windows test package (scripts/build-win-test.sh) runs everything on one machine without
// an IdP. Its middleware treats every request as the signed-in USER, still injects the service
// credentials, and serves the frontend. None of this code runs unless winTest is true.

func loadWinTestConfig() (Config, error) {
	if environment := os.Getenv("STOCK_AI_ENVIRONMENT"); environment != "Test" {
		return Config{}, errors.New("the win-test middleware requires STOCK_AI_ENVIRONMENT=Test")
	}
	tokens := make(map[string]string)
	for name, logical := range map[string]string{
		"AGENT_SERVER_ADMIN_TOKEN": "agent_admin_token", "LLM_SERVER_ADMIN_TOKEN": "llm_admin_token",
		"LLM_SERVER_RUNTIME_TOKEN": "llm_runtime_token", "BACKEND_USER_TOKEN": "backend_middleware_token",
	} {
		var err error
		if tokens[name], err = readSecret(name, logical, "Test", false); err != nil {
			return Config{}, err
		}
	}
	c := Config{
		Environment: "Test", ListenAddress: value("MIDDLEWARE_ADDR", "127.0.0.1:24180"),
		BackendURL: strings.TrimSpace(os.Getenv("BACKEND_URL")), AgentURL: strings.TrimSpace(os.Getenv("AGENT_SERVER_URL")), LLMURL: strings.TrimSpace(os.Getenv("LLM_SERVER_URL")),
		AgentAdminToken: tokens["AGENT_SERVER_ADMIN_TOKEN"], LLMAdminToken: tokens["LLM_SERVER_ADMIN_TOKEN"],
		LLMRuntimeToken: tokens["LLM_SERVER_RUNTIME_TOKEN"], BackendUserToken: tokens["BACKEND_USER_TOKEN"],
		WebDir: strings.TrimSpace(os.Getenv("WEB_DIR")), UserName: value("USER_NAME", "USER"),
		SessionTTL: 24 * time.Hour,
	}
	return c, c.Validate()
}

func (c Config) validateWinTest() error {
	tokens := []string{c.AgentAdminToken, c.LLMAdminToken, c.LLMRuntimeToken, c.BackendUserToken}
	for i, token := range tokens {
		if token == "" {
			return errors.New("middleware credentials are required")
		}
		for _, other := range tokens[:i] {
			if token == other {
				return errors.New("middleware credential roles must be distinct")
			}
		}
	}
	if info, err := os.Stat(filepath.Join(c.WebDir, "index.html")); c.WebDir == "" || err != nil || !info.Mode().IsRegular() {
		return errors.New("WEB_DIR must be the folder holding the built frontend's index.html")
	}
	return nil
}

// winTestSession is the USER every request is signed in as.
func (s *Server) winTestSession() session {
	return session{Subject: "win-test", User: s.cfg.UserName, Expires: s.now().Add(s.cfg.SessionTTL)}
}

// serveWeb serves the built frontend; paths that are not files fall back to index.html, like
// the nginx edge's try_files $uri /index.html.
func (s *Server) serveWeb() http.Handler {
	files := http.FileServer(http.Dir(s.cfg.WebDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		path := filepath.Join(s.cfg.WebDir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(s.cfg.WebDir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
