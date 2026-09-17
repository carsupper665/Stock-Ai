package test

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func TestGenerateProviderCredentialSnapshot(t *testing.T) {
	t.Setenv("LLM_SERVER_MODEL_CATALOG", "")
	for _, recreate := range []bool{false, true} {
		name := "URL change and token replacement"
		if recreate {
			name = "delete and recreate same Provider ID"
		}
		t.Run(name, func(t *testing.T) {
			var mismatches atomic.Int32
			oldUpstream := snapshotUpstream(t, "snapshot-old-secret", &mismatches)
			defer oldUpstream.Close()
			newUpstream := snapshotUpstream(t, "snapshot-new-secret", &mismatches)
			defer newUpstream.Close()

			dbPath := filepath.Join(t.TempDir(), "gateway.db")
			server, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			createProvider(t, server.Handler(), oldUpstream.URL, "model", true)
			oldToken := createToken(t, server.Handler(), "fixture", "old", "snapshot-old-secret")
			newToken := createToken(t, server.Handler(), "fixture", "new", "snapshot-new-secret")
			response := request(t, server.Handler(), http.MethodPatch,
				"/v1/providers/fixture/tokens/"+newToken.ID, adminToken, []byte(`{"enabled":false}`))
			if response.Code != http.StatusOK {
				t.Fatalf("disable new snapshot token = %d %s", response.Code, response.Body)
			}

			db := openSQLite(t, dbPath)
			defer db.Close()
			oldCiphertext := snapshotCiphertext(t, db, oldToken.ID)
			newCiphertext := snapshotCiphertext(t, db, newToken.ID)
			requestBody := []byte(fmt.Sprintf(`{"provider":"fixture","messages":[{"role":"user","content":%q}]}`, string(bytes.Repeat([]byte("p"), 256<<10))))
			start := make(chan struct{})
			var requests sync.WaitGroup
			var failed atomic.Int32
			for worker := 0; worker < 6; worker++ {
				requests.Add(1)
				go func() {
					defer requests.Done()
					<-start
					for attempt := 0; attempt < 30; attempt++ {
						recorder := httptest.NewRecorder()
						httpRequest := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(requestBody))
						httpRequest.Header.Set("Authorization", "Bearer "+runtimeToken)
						httpRequest.Header.Set("Content-Type", "application/json")
						server.Handler().ServeHTTP(recorder, httpRequest)
						if recorder.Code != http.StatusOK {
							failed.Add(1)
						}
					}
				}()
			}
			close(start)
			for generation := 0; generation < 120; generation++ {
				useNew := generation%2 == 0
				if err := replaceSnapshotState(db, recreate, useNew, oldUpstream.URL, newUpstream.URL,
					oldToken, newToken, oldCiphertext, newCiphertext); err != nil {
					t.Fatal(err)
				}
			}
			requests.Wait()
			if failed.Load() != 0 {
				t.Fatalf("Generate failures during committed snapshot changes = %d", failed.Load())
			}
			if mismatches.Load() != 0 {
				t.Fatalf("observed %d mixed Provider URL/credential snapshots", mismatches.Load())
			}
		})
	}
}

func snapshotUpstream(t *testing.T, secret string, mismatches *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			mismatches.Add(1)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`))
	}))
}

func snapshotCiphertext(t *testing.T, db *sql.DB, tokenID string) string {
	t.Helper()
	var ciphertext string
	if err := db.QueryRow(`SELECT secret_encrypted FROM provider_tokens WHERE id = ?`, tokenID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	return ciphertext
}

func replaceSnapshotState(db *sql.DB, recreate, useNew bool, oldURL, newURL string,
	oldToken, newToken ProviderTokenResponse, oldCiphertext, newCiphertext string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	selectedURL, selectedToken, selectedCiphertext := oldURL, oldToken, oldCiphertext
	if useNew {
		selectedURL, selectedToken, selectedCiphertext = newURL, newToken, newCiphertext
	}
	if recreate {
		if _, err := tx.Exec(`DELETE FROM provider_tokens WHERE provider_id = 'fixture'`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM providers WHERE id = 'fixture'`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO providers
			(id,name,type,base_url,default_model,config_json,enabled,created_at,updated_at,tokens_configured)
			VALUES ('fixture','Fixture','openai_compatible',?,'model','{}',1,?,?,1)`,
			selectedURL, selectedToken.CreatedAt, selectedToken.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO provider_tokens
			(id,provider_id,name,secret_encrypted,masked,enabled,created_at,updated_at)
			VALUES (?,'fixture',?,?,?,1,?,?)`, selectedToken.ID, selectedToken.Name, selectedCiphertext,
			selectedToken.Masked, selectedToken.CreatedAt, selectedToken.UpdatedAt); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE providers SET base_url = ? WHERE id = 'fixture'`, selectedURL); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE provider_tokens SET enabled = CASE WHEN id = ? THEN 1 ELSE 0 END WHERE provider_id = 'fixture'`, selectedToken.ID); err != nil {
		return err
	}
	return tx.Commit()
}
