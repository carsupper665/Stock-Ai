package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) loadSavedCatalog() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS model_catalog (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL)`); err != nil {
		return err
	}
	var body string
	err := s.db.QueryRow(`SELECT body FROM model_catalog WHERE id=1`).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	models, err := parseModelCatalog(body)
	if err != nil {
		return err
	}
	s.modelCatalog = models
	return nil
}

func levelOptions(levels map[string]map[string]json.RawMessage) []map[string]json.RawMessage {
	all := make([]map[string]json.RawMessage, 0, len(levels))
	for _, options := range levels {
		all = append(all, options)
	}
	return all
}

func catalogConfig(models []modelMapping) []modelMappingConfig {
	entries := make([]modelMappingConfig, 0, len(models))
	for _, model := range models {
		enabled, _ := json.Marshal(model.Enabled)
		entries = append(entries, modelMappingConfig{ModelName: model.ModelName, ProviderID: model.ProviderID,
			Model: model.Model, Options: model.Options, Levels: model.Levels, Enabled: enabled})
	}
	return entries
}

// The first admin mutation persists the entire bootstrap catalog. Subsequent opens use
// that durable catalog, so restarting with old environment settings cannot undo UI edits.
func (s *Server) manageModels(w http.ResponseWriter, request *http.Request) {
	if !authorized(request, s.adminToken) {
		writeError(w, http.StatusUnauthorized, "AUTHENTICATION_FAILED", "Valid admin credentials are required.")
		return
	}
	name := strings.TrimPrefix(request.URL.Path, "/v1/model-catalog")
	name = strings.TrimPrefix(name, "/")
	if name != "" && !modelNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Invalid model_name.")
		return
	}
	if request.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Query parameters are not supported.")
		return
	}
	if name == "" && request.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if name != "" && request.Method != http.MethodPut && request.Method != http.MethodDelete {
		methodNotAllowed(w, "PUT, DELETE")
		return
	}
	var input *modelMappingConfig
	if request.Method != http.MethodDelete {
		if err := decodeJSON(w, request, &input); err != nil || input == nil {
			writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Expected a model mapping object.")
			return
		}
		if name != "" && input.ModelName != name {
			writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "model_name is immutable and must match the URL.")
			return
		}
		// Omitted enabled defaults true; re-marshalling a nil RawMessage would produce the invalid null.
		if len(input.Enabled) == 0 {
			input.Enabled = json.RawMessage("true")
		}
	}
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	entries := catalogConfig(s.modelCatalog)
	index := -1
	lookup := name
	if input != nil {
		lookup = strings.TrimSpace(input.ModelName)
	}
	for i := range entries {
		if entries[i].ModelName == lookup {
			index = i
			break
		}
	}
	if request.Method == http.MethodPost && index >= 0 {
		writeError(w, http.StatusConflict, "MODEL_ALREADY_EXISTS", "model_name already exists.")
		return
	}
	if request.Method != http.MethodPost && index < 0 {
		writeError(w, http.StatusNotFound, "MODEL_NOT_FOUND", "Model mapping does not exist.")
		return
	}
	if input != nil {
		provider, err := s.getProvider(strings.TrimSpace(input.ProviderID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "Provider does not exist.")
			} else {
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to read Provider.")
			}
			return
		}
		// Reject at save time what generateHarness would reject at call time, so a mapping
		// that can never be invoked is never stored.
		if isHarnessType(provider.Type) {
			for _, options := range append([]map[string]json.RawMessage{input.Options}, levelOptions(input.Levels)...) {
				if _, err := harnessEffort(provider.Type, options); err != nil {
					writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
					return
				}
			}
		}
	}
	switch request.Method {
	case http.MethodPost:
		entries = append(entries, *input)
	case http.MethodPut:
		entries[index] = *input
	case http.MethodDelete:
		entries = append(entries[:index], entries[index+1:]...)
	}
	body, err := json.Marshal(entries)
	if err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", "Invalid model mapping.")
		return
	}
	models, err := parseModelCatalog(string(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	}
	if _, err := s.db.ExecContext(request.Context(), `INSERT INTO model_catalog(id,body) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body`, string(body)); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to save model catalog.")
		return
	}
	s.modelCatalog = models
	if request.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, model := range models {
		if model.ModelName == lookup {
			provider, err := s.getProvider(model.ProviderID)
			model.Available = err == nil && provider.Enabled && model.Enabled
			status := http.StatusOK
			if request.Method == http.MethodPost {
				status = http.StatusCreated
			}
			writeJSON(w, status, model)
			return
		}
	}
}
