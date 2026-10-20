package gateway

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sqlite "github.com/glebarez/go-sqlite"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"stock-ai/llm-provider-server/internal/providers"
	"stock-ai/llm-provider-server/internal/webharness"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxRequestBody            = 1 << 20
	sqliteConstraintPrimaryID = 1555
)

var tokenIDPattern = regexp.MustCompile(`^tok_[a-f0-9]{32}$`)

// Level names are the operator's own labels; what each one sends is up to the Provider.
var levelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type Server struct {
	db             *sql.DB
	adminToken     string
	runtimeToken   string
	masterKey      []byte
	modelCatalog   []modelMapping
	catalogMu      sync.RWMutex
	httpClient     *http.Client
	handler        http.Handler
	requestContext context.Context
	cancelRequests context.CancelFunc
	requestMu      sync.Mutex
	requestWG      sync.WaitGroup
	stopping       bool
	closeOnce      sync.Once
	closeErr       error
	claudeSessions *webharness.ClaudeSessions
	codex          *webharness.CodexAppServer
	apis           map[string]providers.Api
}

type providerRequestSnapshot struct {
	Provider   Provider
	Credential string
}

var errProviderCredentialUnavailable = errors.New("provider credential is unavailable")

type Provider struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	BaseURL      string          `json:"base_url"`
	DefaultModel *string         `json:"default_model"`
	Config       json.RawMessage `json:"config"`
	Enabled      bool            `json:"enabled"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

type ProviderToken struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id"`
	Name       string `json:"name"`
	Masked     string `json:"masked"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type createTokenRequest struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

type patchTokenRequest struct {
	Name    json.RawMessage `json:"name"`
	Token   json.RawMessage `json:"token"`
	Enabled json.RawMessage `json:"enabled"`
}

type createProviderRequest struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	BaseURL      string          `json:"base_url"`
	DefaultModel *string         `json:"default_model"`
	Config       json.RawMessage `json:"config"`
	Enabled      *bool           `json:"enabled"`
}

type patchProviderRequest struct {
	Name         json.RawMessage `json:"name"`
	Type         json.RawMessage `json:"type"`
	BaseURL      json.RawMessage `json:"base_url"`
	DefaultModel json.RawMessage `json:"default_model"`
	Config       json.RawMessage `json:"config"`
	Enabled      json.RawMessage `json:"enabled"`
}

type modelMapping struct {
	ModelName  string                                `json:"model_name"`
	ProviderID string                                `json:"provider_id"`
	Model      string                                `json:"model"`
	Options    map[string]json.RawMessage            `json:"options"`
	Levels     map[string]map[string]json.RawMessage `json:"levels"`
	// ContextWindow is the model's token window for the agent's compaction; 0 means unset.
	ContextWindow int64 `json:"context_window,omitempty"`
	Enabled       bool  `json:"enabled"`
	Available     bool  `json:"available"`
}

type modelMappingConfig struct {
	ModelName     string                                `json:"model_name"`
	ProviderID    string                                `json:"provider_id"`
	Model         string                                `json:"model"`
	Options       map[string]json.RawMessage            `json:"options"`
	Levels        map[string]map[string]json.RawMessage `json:"levels"`
	ContextWindow int64                                 `json:"context_window,omitempty"`
	Enabled       json.RawMessage                       `json:"enabled"`
}

func Open(dbPath, adminToken, runtimeToken, encodedMasterKey string, providerTimeout time.Duration) (*Server, error) {
	if dbPath == "" {
		return nil, errors.New("database path is required")
	}
	if adminToken == "" || runtimeToken == "" || adminToken == runtimeToken {
		return nil, errors.New("non-empty, distinct admin and runtime tokens are required")
	}
	if providerTimeout <= 0 {
		return nil, errors.New("provider timeout must be positive")
	}
	harnessSessionTTLValue := os.Getenv("LLM_SERVER_HARNESS_SESSION_TTL")
	if harnessSessionTTLValue == "" {
		harnessSessionTTLValue = "30m"
	}
	harnessSessionTTL, err := time.ParseDuration(harnessSessionTTLValue)
	if err != nil || harnessSessionTTL <= 0 {
		return nil, errors.New("LLM_SERVER_HARNESS_SESSION_TTL must be a positive Go duration")
	}
	masterKey, err := base64.StdEncoding.DecodeString(encodedMasterKey)
	if err != nil || len(masterKey) != 32 || base64.StdEncoding.EncodeToString(masterKey) != encodedMasterKey {
		return nil, errors.New("LLM_SERVER_MASTER_KEY must be standard base64 encoding of exactly 32 bytes")
	}
	modelCatalog, err := parseModelCatalog(os.Getenv("LLM_SERVER_MODEL_CATALOG"))
	if err != nil {
		return nil, fmt.Errorf("LLM_SERVER_MODEL_CATALOG: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		base_url TEXT NOT NULL,
		default_model TEXT,
		config_json TEXT NOT NULL,
		enabled INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS provider_tokens (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL,
		name TEXT NOT NULL,
		secret_encrypted TEXT NOT NULL,
		masked TEXT NOT NULL,
		enabled INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize token database: %w", err)
	}
	if err := ensureProviderTokenStateColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize provider token state: %w", err)
	}
	if err := validateStoredTokens(db, masterKey); err != nil {
		db.Close()
		return nil, errors.New("validate stored provider credentials: token decryption failed")
	}

	requestContext, cancelRequests := context.WithCancel(context.Background())
	server := &Server{
		db: db, adminToken: adminToken, runtimeToken: runtimeToken,
		masterKey: masterKey, modelCatalog: modelCatalog,
		requestContext: requestContext, cancelRequests: cancelRequests,
		claudeSessions: webharness.NewClaudeSessions(webharness.HarnessPath("LLM_SERVER_CLAUDE_EXECUTABLE"), harnessSessionTTL),
		codex:          webharness.NewCodexAppServer(),
		httpClient: &http.Client{
			Timeout: providerTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	// Provider type → wire protocol Api, as in pi-ai; claude_code keeps its CLI session path.
	server.apis = map[string]providers.Api{
		"openai": providers.OpenAICompletions{OpenAI: true}, "openai_compatible": providers.OpenAICompletions{},
		"anthropic": providers.AnthropicMessages{}, "gemini": providers.GoogleGenerativeAI{}, "codex": server.codex,
	}
	if err := server.loadSavedCatalog(); err != nil {
		server.codex.Close()
		_ = db.Close()
		return nil, fmt.Errorf("load model catalog: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/v1/providers", server.providers)
	mux.HandleFunc("/v1/providers/", server.providerByID)
	mux.HandleFunc("/v1/models", server.models)
	mux.HandleFunc("/v1/harnesses", server.harnessStatus)
	mux.HandleFunc("/v1/model-catalog", server.manageModels)
	mux.HandleFunc("/v1/model-catalog/", server.manageModels)
	mux.HandleFunc("/v1/generate", server.generate)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "REQUEST_INVALID", "Endpoint does not exist.")
	})
	server.handler = accessLog(server.trackRequests(mux))
	return server, nil
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, s.runtimeToken) {
		writeError(w, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Valid runtime credentials are required.")
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request providers.GenerateRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	}
	s.dispatchGenerate(w, r, request, true)
}

// With catalog false, request.Options go to the Provider as written; only the admin test call does that.
func (s *Server) dispatchGenerate(w http.ResponseWriter, r *http.Request, request providers.GenerateRequest, catalog bool) {
	if err := providers.ValidateGenerateRequest(&request); err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	}
	snapshot, err := s.resolveProviderRequestSnapshot(r.Context(), request.Provider)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	}
	credentialUnavailable := errors.Is(err, errProviderCredentialUnavailable)
	if err != nil && !credentialUnavailable {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read provider.")
		return
	}
	provider := snapshot.Provider
	if !provider.Enabled {
		writeError(w, http.StatusConflict, "PROVIDER_DISABLED", "Provider is disabled.")
		return
	}
	model := provider.DefaultModel
	if request.Model != nil {
		trimmed := strings.TrimSpace(*request.Model)
		if trimmed != "" {
			model = &trimmed
		}
	}
	if model == nil || *model == "" {
		writeError(w, http.StatusBadRequest, "MODEL_NOT_FOUND", "No model was requested and the provider has no default model.")
		return
	}

	options := request.Options
	if catalog {
		if options, err = s.resolveModelOptions(provider.ID, *model, request.Options); err != nil {
			writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
			return
		}
	}
	if credentialUnavailable {
		writeError(w, http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE", "Provider credential is unavailable.")
		return
	}
	if provider.Type == "claude_code" {
		s.generateHarness(w, r, provider, snapshot.Credential, *model, request, options)
		return
	}
	response, err := s.apis[provider.Type].Complete(r.Context(),
		providers.Model{Provider: provider.Type, ID: *model, BaseURL: provider.BaseURL, Config: provider.Config}, request,
		providers.Options{APIKey: snapshot.Credential, Options: options, Client: s.httpClient})
	if err != nil {
		writeProviderError(w, provider, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// writeProviderError logs only the Provider and error code: Provider prose may echo credentials
// or prompts, and diagnostic model names are chosen by callers.
func writeProviderError(w http.ResponseWriter, provider Provider, err error) {
	status, code, message := http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID", "Provider returned an invalid response."
	var failure providers.ProviderError
	switch {
	case errors.As(err, &failure):
		status, code, message = failure.Status, failure.Code, failure.Message
	case providers.IsTimeout(err):
		status, code, message = http.StatusGatewayTimeout, "PROVIDER_TIMEOUT", "Provider request timed out."
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusBadGateway, "PROVIDER_UNAVAILABLE", "Provider request was cancelled."
	}
	log.Printf("event=provider_error provider=%s type=%s status=%d detail=%s", provider.ID, provider.Type, status, code)
	writeError(w, status, code, message)
}

func parseModelCatalog(value string) ([]modelMapping, error) {
	if strings.TrimSpace(value) == "" {
		return []modelMapping{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var configured *[]modelMappingConfig
	if err := decoder.Decode(&configured); err != nil || configured == nil {
		return nil, errors.New("must be one JSON array of model mappings")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain only one JSON array")
	}
	byName := map[string]bool{}
	byProviderModel := map[string]bool{}
	models := make([]modelMapping, 0, len(*configured))
	for _, entry := range *configured {
		entry.ModelName = strings.TrimSpace(entry.ModelName)
		entry.ProviderID = strings.TrimSpace(entry.ProviderID)
		entry.Model = strings.TrimSpace(entry.Model)
		if !providers.ModelNamePattern.MatchString(entry.ModelName) || !providers.ProviderIDPattern.MatchString(entry.ProviderID) || entry.Model == "" {
			return nil, errors.New("model_name, provider_id, and model must be valid nonempty values")
		}
		if byName[entry.ModelName] {
			return nil, fmt.Errorf("duplicate model_name %q", entry.ModelName)
		}
		providerModel := entry.ProviderID + "\x00" + entry.Model
		if byProviderModel[providerModel] {
			return nil, fmt.Errorf("provider/model mapping %q/%q is ambiguous", entry.ProviderID, entry.Model)
		}
		if entry.Options == nil {
			entry.Options = map[string]json.RawMessage{}
		}
		if entry.Levels == nil {
			entry.Levels = map[string]map[string]json.RawMessage{}
		}
		if err := validateMappedOptions(entry.Options); err != nil {
			return nil, fmt.Errorf("model %q options: %w", entry.ModelName, err)
		}
		for level, options := range entry.Levels {
			if !levelNamePattern.MatchString(level) {
				return nil, fmt.Errorf("model %q has invalid level %q", entry.ModelName, level)
			}
			if options == nil {
				return nil, fmt.Errorf("model %q level %q options must be an object", entry.ModelName, level)
			}
			if err := validateMappedOptions(options); err != nil {
				return nil, fmt.Errorf("model %q level %q: %w", entry.ModelName, level, err)
			}
		}
		if entry.ContextWindow < 0 {
			return nil, fmt.Errorf("model %q context_window must be a positive integer", entry.ModelName)
		}
		enabled := true
		if len(entry.Enabled) != 0 && (providers.IsJSONNull(entry.Enabled) || json.Unmarshal(entry.Enabled, &enabled) != nil) {
			return nil, fmt.Errorf("model %q enabled must be a boolean", entry.ModelName)
		}
		models = append(models, modelMapping{ModelName: entry.ModelName, ProviderID: entry.ProviderID,
			Model: entry.Model, Options: entry.Options, Levels: entry.Levels, ContextWindow: entry.ContextWindow, Enabled: enabled})
		byName[entry.ModelName] = true
		byProviderModel[providerModel] = true
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelName < models[j].ModelName })
	return models, nil
}

func validateMappedOptions(options map[string]json.RawMessage) error {
	for _, reserved := range []string{"model", "messages", "tools", "model_level"} {
		if _, exists := options[reserved]; exists {
			return fmt.Errorf("%s is reserved", reserved)
		}
	}
	return nil
}

func (s *Server) resolveModelOptions(providerID, model string, requested map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	var mapping *modelMapping
	for index := range s.modelCatalog {
		candidate := &s.modelCatalog[index]
		if candidate.Enabled && candidate.ProviderID == providerID && candidate.Model == model {
			mapping = candidate
			break
		}
	}
	levelValue, hasLevel := requested["model_level"]
	if mapping == nil {
		if hasLevel {
			return nil, errors.New("model_level is not supported by this provider/model")
		}
		return requested, nil
	}
	if len(requested) != 0 && (!hasLevel || len(requested) != 1) {
		return nil, errors.New("catalog models accept only model_level in request options")
	}
	options := make(map[string]json.RawMessage, len(mapping.Options))
	for key, value := range mapping.Options {
		options[key] = value
	}
	if !hasLevel {
		return options, nil
	}
	var level string
	if json.Unmarshal(levelValue, &level) != nil || level == "" {
		return nil, errors.New("model_level must be a supported string")
	}
	levelOptions, supported := mapping.Levels[level]
	if !supported {
		return nil, fmt.Errorf("model_level %q is not supported", level)
	}
	for key, value := range levelOptions {
		options[key] = value
	}
	return options, nil
}

// Only structured error identifiers are inspected; Provider prose is never returned.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) trackRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestMu.Lock()
		if s.stopping {
			s.requestMu.Unlock()
			writeError(w, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE", "Gateway is shutting down.")
			return
		}
		s.requestWG.Add(1)
		s.requestMu.Unlock()
		defer s.requestWG.Done()

		requestContext, cancel := context.WithCancel(r.Context())
		stopCancellation := context.AfterFunc(s.requestContext, cancel)
		defer stopCancellation()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(requestContext))
	})
}

// accessLog records every request without its query string. Passing health probes run every few
// seconds and carry no information, so only a failing one is logged.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if r.URL.Path == "/healthz" && recorder.status < 300 {
			return
		}
		log.Printf("event=http_request method=%s path=%s status=%d duration=%s",
			r.Method, r.URL.Path, recorder.status, time.Since(start).Round(time.Microsecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status = status
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(body []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(body)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *Server) BeginShutdown() {
	s.requestMu.Lock()
	s.stopping = true
	s.requestMu.Unlock()
	s.cancelRequests()
}

func (s *Server) WaitForRequests() { s.requestWG.Wait() }

func (s *Server) Close() error {
	s.BeginShutdown()
	s.WaitForRequests()
	s.closeOnce.Do(func() {
		s.claudeSessions.Stop()
		s.codex.Close()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if err := s.db.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "INTERNAL_ERROR", "Service database is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	if !authorized(r, s.runtimeToken) {
		writeError(w, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Valid runtime credentials are required.")
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	availableProviders := map[string]bool{}
	if len(s.modelCatalog) != 0 {
		rows, err := s.db.QueryContext(r.Context(), `SELECT id, enabled FROM providers`)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read model availability.")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var enabled bool
			if err := rows.Scan(&id, &enabled); err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read model availability.")
				return
			}
			availableProviders[id] = enabled
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read model availability.")
			return
		}
	}
	models := make([]modelMapping, len(s.modelCatalog))
	copy(models, s.modelCatalog)
	for index := range models {
		models[index].Available = models[index].Enabled && availableProviders[models[index].ProviderID]
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, s.adminToken) {
		writeError(w, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Valid admin credentials are required.")
		return
	}
	if r.Method == http.MethodGet {
		s.listProviders(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "REQUEST_INVALID", "Method not allowed.")
		return
	}

	var request createProviderRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	}
	provider, err := newProvider(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PROVIDER_INVALID_CONFIG", err.Error())
		return
	}
	_, err = s.db.Exec(`INSERT INTO providers
		(id, name, type, base_url, default_model, config_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, provider.ID, provider.Name, provider.Type,
		provider.BaseURL, provider.DefaultModel, string(provider.Config), provider.Enabled,
		provider.CreatedAt, provider.UpdatedAt)
	if err != nil {
		var sqliteError *sqlite.Error
		if errors.As(err, &sqliteError) && sqliteError.Code() == sqliteConstraintPrimaryID {
			writeError(w, http.StatusConflict, "PROVIDER_INVALID_CONFIG", "Provider ID already exists.")
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to persist provider.")
		return
	}
	writeJSON(w, http.StatusCreated, provider)
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, name, type, base_url, default_model, config_json,
		enabled, created_at, updated_at FROM providers ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to list providers.")
		return
	}
	defer rows.Close()
	providers := make([]Provider, 0)
	for rows.Next() {
		var provider Provider
		var config string
		var enabled int
		if err := rows.Scan(&provider.ID, &provider.Name, &provider.Type, &provider.BaseURL, &provider.DefaultModel,
			&config, &enabled, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to list providers.")
			return
		}
		provider.Config = json.RawMessage(config)
		provider.Enabled = enabled != 0
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to list providers.")
		return
	}
	writeJSON(w, http.StatusOK, providers)
}

func (s *Server) providerByID(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, s.adminToken) {
		writeError(w, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Valid admin credentials are required.")
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, "/v1/providers/")
	parts := strings.Split(resource, "/")
	if len(parts) == 2 && parts[1] == "test" && providers.ProviderIDPattern.MatchString(parts[0]) {
		s.testProvider(w, r, parts[0])
		return
	}
	if len(parts) >= 2 && parts[1] == "tokens" {
		id := parts[0]
		if !providers.ProviderIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
			return
		}
		if len(parts) == 3 {
			s.providerTokenByID(w, r, id, parts[2])
			return
		}
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "Provider token does not exist.")
			return
		}
		s.providerTokens(w, r, id)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPatch+", "+http.MethodDelete)
		writeError(w, http.StatusMethodNotAllowed, "REQUEST_INVALID", "Method not allowed.")
		return
	}
	id := resource
	if !providers.ProviderIDPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	}
	if r.Method == http.MethodPatch {
		s.patchProvider(w, r, id)
		return
	}
	if r.Method == http.MethodDelete {
		s.deleteProvider(w, r, id)
		return
	}
	provider, err := s.getProvider(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read provider.")
		return
	}
	writeJSON(w, http.StatusOK, provider)
}

func (s *Server) providerTokens(w http.ResponseWriter, r *http.Request, providerID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "REQUEST_INVALID", "Method not allowed.")
		return
	}
	if _, err := s.getProvider(providerID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read provider.")
		return
	}
	if r.Method == http.MethodGet {
		s.listProviderTokens(w, r, providerID)
		return
	}
	var request createTokenRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || !validTokenSecret(request.Token) {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Token name is required; token must contain no whitespace or control characters.")
		return
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to create provider token.")
		return
	}
	id := "tok_" + hex.EncodeToString(idBytes)
	encrypted, err := encryptToken(s.masterKey, id, providerID, request.Token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to protect provider token.")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	masked := maskToken(request.Token)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to persist provider token.")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO provider_tokens
		(id, provider_id, name, secret_encrypted, masked, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, id, providerID, request.Name, encrypted, masked, now, now); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to persist provider token.")
		return
	}
	updated, err := tx.ExecContext(r.Context(), `UPDATE providers SET tokens_configured = 1 WHERE id = ?`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to persist provider token.")
		return
	}
	providerRows, err := updated.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to persist provider token.")
		return
	}
	if providerRows == 0 {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to persist provider token.")
		return
	}
	writeJSON(w, http.StatusCreated, ProviderToken{ID: id, ProviderID: providerID, Name: request.Name,
		Masked: masked, Enabled: true, CreatedAt: now, UpdatedAt: now})
}

func (s *Server) providerTokenByID(w http.ResponseWriter, r *http.Request, providerID, tokenID string) {
	if !tokenIDPattern.MatchString(tokenID) {
		writeError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "Provider token does not exist.")
		return
	}
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodPatch+", "+http.MethodDelete)
		writeError(w, http.StatusMethodNotAllowed, "REQUEST_INVALID", "Method not allowed.")
		return
	}
	if r.Method == http.MethodDelete {
		result, err := s.db.ExecContext(r.Context(), `DELETE FROM provider_tokens WHERE id = ? AND provider_id = ?`, tokenID, providerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider token.")
			return
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider token.")
			return
		}
		if deleted == 0 {
			writeError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "Provider token does not exist.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.patchProviderToken(w, r, providerID, tokenID)
}

func (s *Server) patchProviderToken(w http.ResponseWriter, r *http.Request, providerID, tokenID string) {
	var patch *patchTokenRequest
	if err := decodeJSON(w, r, &patch); err != nil || patch == nil {
		message := "Request body must contain one JSON object."
		if err != nil {
			message = err.Error()
		}
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", message)
		return
	}
	if len(patch.Name)+len(patch.Token)+len(patch.Enabled) == 0 {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "At least one token field is required.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider token.")
		return
	}
	defer tx.Rollback()
	var current ProviderToken
	var encrypted string
	var enabled int
	err = tx.QueryRowContext(r.Context(), `SELECT id, provider_id, name, secret_encrypted, masked, enabled, created_at, updated_at
		FROM provider_tokens WHERE id = ? AND provider_id = ?`, tokenID, providerID).Scan(
		&current.ID, &current.ProviderID, &current.Name, &encrypted, &current.Masked, &enabled,
		&current.CreatedAt, &current.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "Provider token does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider token.")
		return
	}
	current.Enabled = enabled != 0
	if len(patch.Name) != 0 {
		if providers.IsJSONNull(patch.Name) || json.Unmarshal(patch.Name, &current.Name) != nil {
			writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "name must be a string.")
			return
		}
		current.Name = strings.TrimSpace(current.Name)
		if current.Name == "" {
			writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Token name is required.")
			return
		}
	}
	if len(patch.Enabled) != 0 && (providers.IsJSONNull(patch.Enabled) || json.Unmarshal(patch.Enabled, &current.Enabled) != nil) {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "enabled must be a boolean.")
		return
	}
	if len(patch.Token) != 0 {
		var secret string
		if providers.IsJSONNull(patch.Token) || json.Unmarshal(patch.Token, &secret) != nil || !validTokenSecret(secret) {
			writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "token must be a nonempty string without whitespace or control characters.")
			return
		}
		encrypted, err = encryptToken(s.masterKey, tokenID, providerID, secret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to protect provider token.")
			return
		}
		current.Masked = maskToken(secret)
	}
	current.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(r.Context(), `UPDATE provider_tokens SET name = ?, secret_encrypted = ?, masked = ?, enabled = ?, updated_at = ?
		WHERE id = ? AND provider_id = ?`, current.Name, encrypted, current.Masked, current.Enabled,
		current.UpdatedAt, tokenID, providerID); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider token.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider token.")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) listProviderTokens(w http.ResponseWriter, r *http.Request, providerID string) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, provider_id, name, masked, enabled, created_at, updated_at
		FROM provider_tokens WHERE provider_id = ? ORDER BY created_at, id`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to list provider tokens.")
		return
	}
	defer rows.Close()
	tokens := make([]ProviderToken, 0)
	for rows.Next() {
		var token ProviderToken
		var enabled int
		if err := rows.Scan(&token.ID, &token.ProviderID, &token.Name, &token.Masked, &enabled,
			&token.CreatedAt, &token.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to list provider tokens.")
			return
		}
		token.Enabled = enabled != 0
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to list provider tokens.")
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func encryptToken(key []byte, tokenID, providerID, secret string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nonce, nonce, []byte(secret), []byte(providerID+"\x00"+tokenID))
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptToken(key []byte, tokenID, providerID, encrypted string) (string, error) {
	value, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", errors.New("token ciphertext is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.New("token key is invalid")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(value) < aead.NonceSize() {
		return "", errors.New("token ciphertext is invalid")
	}
	plaintext, err := aead.Open(nil, value[:aead.NonceSize()], value[aead.NonceSize():], []byte(providerID+"\x00"+tokenID))
	if err != nil {
		return "", errors.New("token decryption failed")
	}
	return string(plaintext), nil
}

func validateStoredTokens(db *sql.DB, key []byte) error {
	rows, err := db.Query(`SELECT id, provider_id, secret_encrypted FROM provider_tokens`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, providerID, encrypted string
		if err := rows.Scan(&id, &providerID, &encrypted); err != nil {
			return err
		}
		if _, err := decryptToken(key, id, providerID, encrypted); err != nil {
			return err
		}
	}
	return rows.Err()
}

func ensureProviderTokenStateColumn(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(providers)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var columnID int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		found = found || name == "tokens_configured"
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !found {
		if _, err := tx.Exec(`ALTER TABLE providers ADD COLUMN tokens_configured INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE providers SET tokens_configured = 1
		WHERE tokens_configured = 0 AND EXISTS (
			SELECT 1 FROM provider_tokens WHERE provider_tokens.provider_id = providers.id
		)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) resolveProviderRequestSnapshot(ctx context.Context, providerID string) (providerRequestSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return providerRequestSnapshot{}, err
	}
	defer tx.Rollback()
	var snapshot providerRequestSnapshot
	var config string
	var enabled, tokensConfigured int
	err = tx.QueryRowContext(ctx, `SELECT id, name, type, base_url, default_model, config_json,
		enabled, created_at, updated_at, tokens_configured FROM providers WHERE id = ?`, providerID).Scan(
		&snapshot.Provider.ID, &snapshot.Provider.Name, &snapshot.Provider.Type, &snapshot.Provider.BaseURL,
		&snapshot.Provider.DefaultModel, &config, &enabled, &snapshot.Provider.CreatedAt,
		&snapshot.Provider.UpdatedAt, &tokensConfigured)
	if err != nil {
		return providerRequestSnapshot{}, err
	}
	snapshot.Provider.Config = json.RawMessage(config)
	snapshot.Provider.Enabled = enabled != 0
	var harnessConfig struct {
		AuthMode string `json:"auth_mode"`
	}
	localLogin := isHarnessType(snapshot.Provider.Type) && json.Unmarshal(snapshot.Provider.Config, &harnessConfig) == nil && harnessConfig.AuthMode == "local_login"
	var credentialErr error
	if !localLogin {
		var tokenID, encrypted string
		err = tx.QueryRowContext(ctx, `SELECT id, secret_encrypted FROM provider_tokens
			WHERE provider_id = ? AND enabled = 1 ORDER BY created_at, id LIMIT 1`, providerID).Scan(&tokenID, &encrypted)
		switch {
		case err == nil:
			snapshot.Credential, err = decryptToken(s.masterKey, tokenID, providerID, encrypted)
			if err != nil {
				credentialErr = errProviderCredentialUnavailable
			}
		case errors.Is(err, sql.ErrNoRows) && tokensConfigured == 0:
		case errors.Is(err, sql.ErrNoRows):
			credentialErr = errProviderCredentialUnavailable
		default:
			credentialErr = errProviderCredentialUnavailable
		}
	}
	if err := tx.Commit(); err != nil {
		return providerRequestSnapshot{}, err
	}
	return snapshot, credentialErr
}

func maskToken(secret string) string {
	characters := []rune(secret)
	if len(characters) <= 8 {
		return "****"
	}
	return string(characters[:3]) + "****" + string(characters[len(characters)-4:])
}

func validTokenSecret(secret string) bool {
	return secret != "" && strings.IndexFunc(secret, unicode.IsSpace) < 0 && strings.IndexFunc(secret, unicode.IsControl) < 0
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, id string) {
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider.")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM provider_tokens WHERE provider_id = ?`, id); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider.")
		return
	}
	result, err := tx.ExecContext(r.Context(), `DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider.")
		return
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider.")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to delete provider.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) patchProvider(w http.ResponseWriter, r *http.Request, id string) {
	var patch *patchProviderRequest
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	}
	if patch == nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Request body must contain one JSON object.")
		return
	}
	if len(patch.Name)+len(patch.Type)+len(patch.BaseURL)+len(patch.DefaultModel)+len(patch.Config)+len(patch.Enabled) == 0 {
		writeError(w, http.StatusBadRequest, "PROVIDER_INVALID_CONFIG", "At least one provider field is required.")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider.")
		return
	}
	defer tx.Rollback()
	var current Provider
	var config string
	var enabled int
	err = tx.QueryRowContext(r.Context(), `SELECT id, name, type, base_url, default_model, config_json,
		enabled, created_at, updated_at FROM providers WHERE id = ?`, id).Scan(
		&current.ID, &current.Name, &current.Type, &current.BaseURL, &current.DefaultModel,
		&config, &enabled, &current.CreatedAt, &current.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider.")
		return
	}

	request := createProviderRequest{ID: current.ID, Name: current.Name, Type: current.Type,
		BaseURL: current.BaseURL, DefaultModel: current.DefaultModel, Config: json.RawMessage(config)}
	currentEnabled := enabled != 0
	request.Enabled = &currentEnabled
	if err := applyProviderPatch(&request, *patch); err != nil {
		writeError(w, http.StatusBadRequest, "PROVIDER_INVALID_CONFIG", err.Error())
		return
	}
	updated, err := newProvider(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PROVIDER_INVALID_CONFIG", err.Error())
		return
	}
	updated.CreatedAt = current.CreatedAt
	_, err = tx.ExecContext(r.Context(), `UPDATE providers SET name = ?, type = ?, base_url = ?,
		default_model = ?, config_json = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		updated.Name, updated.Type, updated.BaseURL, updated.DefaultModel, string(updated.Config),
		updated.Enabled, updated.UpdatedAt, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to update provider.")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func applyProviderPatch(request *createProviderRequest, patch patchProviderRequest) error {
	for _, field := range []struct {
		name  string
		raw   json.RawMessage
		value *string
	}{
		{name: "name", raw: patch.Name, value: &request.Name},
		{name: "type", raw: patch.Type, value: &request.Type},
		{name: "base_url", raw: patch.BaseURL, value: &request.BaseURL},
	} {
		if len(field.raw) == 0 {
			continue
		}
		if providers.IsJSONNull(field.raw) || json.Unmarshal(field.raw, field.value) != nil {
			return fmt.Errorf("%s must be a string", field.name)
		}
	}
	if len(patch.DefaultModel) != 0 {
		if err := json.Unmarshal(patch.DefaultModel, &request.DefaultModel); err != nil {
			return errors.New("default_model must be a string or null")
		}
	}
	if len(patch.Config) != 0 {
		request.Config = patch.Config
	}
	if len(patch.Enabled) != 0 {
		if providers.IsJSONNull(patch.Enabled) || json.Unmarshal(patch.Enabled, request.Enabled) != nil {
			return errors.New("enabled must be a boolean")
		}
	}
	return nil
}

func (s *Server) getProvider(id string) (Provider, error) {
	var provider Provider
	var config string
	var enabled int
	err := s.db.QueryRow(`SELECT id, name, type, base_url, default_model, config_json,
		enabled, created_at, updated_at FROM providers WHERE id = ?`, id).Scan(
		&provider.ID, &provider.Name, &provider.Type, &provider.BaseURL, &provider.DefaultModel,
		&config, &enabled, &provider.CreatedAt, &provider.UpdatedAt)
	provider.Config = json.RawMessage(config)
	provider.Enabled = enabled != 0
	return provider, err
}

func newProvider(request createProviderRequest) (Provider, error) {
	request.ID = strings.TrimSpace(request.ID)
	request.Name = strings.TrimSpace(request.Name)
	request.Type = strings.TrimSpace(request.Type)
	request.BaseURL = strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	if !providers.ProviderIDPattern.MatchString(request.ID) {
		return Provider{}, errors.New("id must be 1-64 letters, numbers, underscores, or hyphens")
	}
	if request.Name == "" {
		return Provider{}, errors.New("name is required")
	}
	if request.Type != "openai" && request.Type != "anthropic" && request.Type != "gemini" && request.Type != "openai_compatible" && !isHarnessType(request.Type) {
		return Provider{}, errors.New("type must be openai, anthropic, gemini, openai_compatible, codex, or claude_code")
	}
	parsedURL, err := url.Parse(request.BaseURL)
	if isHarnessType(request.Type) && request.BaseURL != "" {
		return Provider{}, errors.New("CLI Harness base_url must be empty; its executable is configured on the Server")
	}
	if !isHarnessType(request.Type) && (err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "") {
		return Provider{}, errors.New("base_url must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if request.DefaultModel != nil {
		trimmed := strings.TrimSpace(*request.DefaultModel)
		request.DefaultModel = &trimmed
		if trimmed == "" {
			request.DefaultModel = nil
		}
	}
	if len(request.Config) == 0 || providers.IsJSONNull(request.Config) {
		request.Config = json.RawMessage(`{}`)
	}
	var config map[string]any
	if err := json.Unmarshal(request.Config, &config); err != nil {
		return Provider{}, errors.New("config must be a JSON object")
	}
	if isHarnessType(request.Type) {
		for key := range config {
			if key != "auth_mode" {
				return Provider{}, errors.New("Harness config accepts only auth_mode")
			}
		}
		mode, exists := config["auth_mode"]
		if exists && mode != "api_key" && mode != "local_login" {
			return Provider{}, errors.New("auth_mode must be api_key or local_login")
		}
	} else if len(config) != 0 {
		return Provider{}, errors.New("config must be empty; credentials use the Provider Token API")
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Provider{ID: request.ID, Name: request.Name, Type: request.Type, BaseURL: request.BaseURL,
		DefaultModel: request.DefaultModel, Config: request.Config, Enabled: enabled,
		CreatedAt: now, UpdatedAt: now}, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("Request body must be one valid JSON object.")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Request body must contain one JSON object.")
	}
	return nil
}

func authorized(r *http.Request, expected string) bool {
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, http.StatusMethodNotAllowed, "REQUEST_INVALID", "Method not allowed.")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
