package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"backend/internal/config"
	"backend/internal/database"
	"backend/internal/logging"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const testUserToken = "user-token-secret"

func newTestServer(t *testing.T) (*gin.Engine, *database.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{
		TranslateError: true,
		Logger:         gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("開啟測試資料庫: %v", err)
	}
	store := database.NewStore(db)
	// Windows 上檔案沒關就刪不掉 TempDir。
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("建表: %v", err)
	}

	log, err := logging.New("test", t.TempDir(), 100000, false)
	if err != nil {
		t.Fatalf("建立 logger: %v", err)
	}
	log.SetConsole(io.Discard)
	t.Cleanup(func() { _ = log.Close() })

	cfg := &config.Config{
		UserToken: testUserToken,
		UserName:  "Bless",
		DBDriver:  "sqlite",
	}
	return New(cfg, store, log), store
}

// do 發出一個請求並回傳狀態碼與解析後的 body。token 為空時不帶 Authorization。
func do(t *testing.T, engine *gin.Engine, method, path, token string, body any) (int, map[string]any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化請求: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	parsed := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	}
	return rec.Code, parsed
}

// createAccountFor 用 USER token 建立帳號，回傳 id 與 token。
func createAccountFor(t *testing.T, engine *gin.Engine, name string, balance float64) (string, string) {
	t.Helper()
	status, body := do(t, engine, http.MethodPost, "/v1/accounts", testUserToken, gin.H{
		"user_name": name, "initial_balance": balance,
	})
	if status != http.StatusCreated {
		t.Fatalf("建立帳號 %s 失敗: %d %v", name, status, body)
	}
	id, _ := body["id"].(string)
	token, _ := body["token"].(string)
	if id == "" || token == "" {
		t.Fatalf("建立帳號回應缺少 id 或 token: %v", body)
	}
	return id, token
}

func TestCreateAccountReturnsToken(t *testing.T) {
	engine, _ := newTestServer(t)

	status, body := do(t, engine, http.MethodPost, "/v1/accounts", testUserToken, gin.H{
		"user_name": "BTC-Agent-01", "initial_balance": 10000,
	})

	if status != http.StatusCreated {
		t.Fatalf("預期 201，得到 %d: %v", status, body)
	}
	if body["user_name"] != "BTC-Agent-01" {
		t.Fatalf("名稱不符: %v", body)
	}
	if body["balance"] != 10000.0 || body["initial_balance"] != 10000.0 {
		t.Fatalf("餘額不符: %v", body)
	}
	if body["status"] != "active" {
		t.Fatalf("新帳號應為 active: %v", body)
	}
	if token, _ := body["token"].(string); token == "" {
		t.Fatalf("USER 建立帳號時必須拿得到 token: %v", body)
	}
}

func TestResetTokenInvalidatesOldToken(t *testing.T) {
	engine, _ := newTestServer(t)
	id, oldToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	status, body := do(t, engine, http.MethodPost, "/v1/accounts/"+id+"/token/reset", testUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("換發 token 失敗: %d %v", status, body)
	}
	newToken, _ := body["token"].(string)
	if newToken == "" || newToken == oldToken {
		t.Fatalf("換發後應取得不同的 token: %v", body)
	}

	if status, _ := do(t, engine, http.MethodGet, "/v1/account", oldToken, nil); status != http.StatusUnauthorized {
		t.Fatalf("舊 token 應立即失效，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/account", newToken, nil); status != http.StatusOK {
		t.Fatalf("新 token 應可使用，得到 %d", status)
	}
}

func TestValidationErrors(t *testing.T) {
	engine, _ := newTestServer(t)
	createAccountFor(t, engine, "taken", 1000)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"名稱空白", gin.H{"user_name": "  ", "initial_balance": 100}, http.StatusBadRequest},
		{"餘額為零", gin.H{"user_name": "zero", "initial_balance": 0}, http.StatusBadRequest},
		{"餘額為負", gin.H{"user_name": "neg", "initial_balance": -5}, http.StatusBadRequest},
		{"名稱重複", gin.H{"user_name": "taken", "initial_balance": 100}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, engine, http.MethodPost, "/v1/accounts", testUserToken, tc.body)
			if status != tc.want {
				t.Fatalf("預期 %d，得到 %d: %v", tc.want, status, body)
			}
			if body["error"] == nil || body["message"] == nil {
				t.Fatalf("錯誤回應缺少 error 或 message: %v", body)
			}
		})
	}
}

func TestMissingAccountReturns404(t *testing.T) {
	engine, _ := newTestServer(t)

	if status, _ := do(t, engine, http.MethodGet, "/v1/accounts/acc_missing", testUserToken, nil); status != http.StatusNotFound {
		t.Fatalf("不存在的帳號應回 404，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodDelete, "/v1/accounts/acc_missing", testUserToken, nil); status != http.StatusNotFound {
		t.Fatalf("刪除不存在的帳號應回 404，得到 %d", status)
	}
}

func TestDeleteRemovesAccount(t *testing.T) {
	engine, _ := newTestServer(t)
	id, accountToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	if status, _ := do(t, engine, http.MethodDelete, "/v1/accounts/"+id, testUserToken, nil); status != http.StatusNoContent {
		t.Fatalf("刪除帳號應回 204，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/accounts/"+id, testUserToken, nil); status != http.StatusNotFound {
		t.Fatalf("刪除後查詢應回 404，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/account", accountToken, nil); status != http.StatusUnauthorized {
		t.Fatalf("刪除後 token 應失效，得到 %d", status)
	}
}

func TestUpdateRequiresAtLeastOneField(t *testing.T) {
	engine, _ := newTestServer(t)
	id, _ := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	if status, _ := do(t, engine, http.MethodPatch, "/v1/accounts/"+id, testUserToken, gin.H{}); status != http.StatusBadRequest {
		t.Fatalf("沒有要修改的欄位應回 400，得到 %d", status)
	}
}

func TestListReturnsTokensForUser(t *testing.T) {
	engine, _ := newTestServer(t)
	createAccountFor(t, engine, "one", 100)
	createAccountFor(t, engine, "two", 200)

	status, body := do(t, engine, http.MethodGet, "/v1/accounts", testUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("列出帳號失敗: %d %v", status, body)
	}
	accounts, ok := body["accounts"].([]any)
	if !ok || len(accounts) != 2 {
		t.Fatalf("預期 2 個帳號: %v", body)
	}
	for _, raw := range accounts {
		item, _ := raw.(map[string]any)
		if token, _ := item["token"].(string); token == "" {
			t.Fatalf("USER 列出帳號時每一筆都該帶 token: %v", item)
		}
	}
}
