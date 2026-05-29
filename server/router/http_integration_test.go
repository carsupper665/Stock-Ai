package router

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"server/model"
	"server/model/store"
	"server/service"
)

func newHTTPTestApp(t *testing.T) *service.App {
	t.Helper()

	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := model.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	app, err := service.NewApp(service.AppConfig{
		DB:            db,
		SessionSecret: "test-session-secret",
		FrontendURL:   "http://localhost:3000",
		Now: func() time.Time {
			return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	password := "root-pass"
	salt := "salt"
	hash, err := bcrypt.GenerateFromPassword([]byte(password+salt), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	root := store.User{Username: "root", DisplayName: "Root", Role: store.RoleRootUser, Email: "root@example.com", Password: string(hash), Salt: salt}
	if err := db.Create(&root).Error; err != nil {
		t.Fatalf("create root user: %v", err)
	}

	dataset := store.ReplayDataset{ID: "dataset-http", Name: "http replay", Symbol: "BTCUSDT", Interval: "1m", StartAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), EndAt: time.Date(2025, 1, 1, 0, 2, 0, 0, time.UTC), Source: "test"}
	if err := db.Create(&dataset).Error; err != nil {
		t.Fatalf("create dataset: %v", err)
	}
	klines := []store.ReplayKline{
		{DatasetID: dataset.ID, Symbol: dataset.Symbol, Ts: dataset.StartAt, Open: 100, High: 100, Low: 100, Close: 100, Volume: 1},
		{DatasetID: dataset.ID, Symbol: dataset.Symbol, Ts: dataset.StartAt.Add(time.Minute), Open: 95, High: 95, Low: 95, Close: 95, Volume: 1},
	}
	if err := db.Create(&klines).Error; err != nil {
		t.Fatalf("create klines: %v", err)
	}

	return app
}

func TestAdminAndAgentHTTPFlow(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	body := postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	if body["username"] != "root" {
		t.Fatalf("unexpected login response: %+v", body)
	}

	sandboxResp := postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-http",
		"name":                "HTTP Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	if sandboxResp["id"] != "sandbox-http" {
		t.Fatalf("unexpected sandbox response: %+v", sandboxResp)
	}

	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-http/start", map[string]any{}, http.StatusOK)

	accountResp := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-http/accounts", map[string]any{
		"name":            "agent-1",
		"initial_balance": 1000,
	}, http.StatusCreated)
	accountID := accountResp["id"].(string)

	tokenResp := postJSON(t, client, ts.URL+"/tokens", map[string]any{
		"account_id": accountID,
		"name":       "agent-token",
		"scopes":     []string{"trade:read", "trade:write", "account:read", "market:read"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)

	authReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/token/login", bytes.NewBuffer(mustJSON(map[string]any{"token": token})))
	authReq.Header.Set("Content-Type", "application/json")
	authResp, err := client.Do(authReq)
	if err != nil {
		t.Fatalf("token login request: %v", err)
	}
	defer authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("token login status: %d", authResp.StatusCode)
	}

	agent := &http.Client{}
	priceResp := getJSON(t, agent, ts.URL+"/market/price?symbol=BTCUSDT", token, http.StatusOK)
	if priceResp["price"].(float64) != 100 {
		t.Fatalf("unexpected market price: %+v", priceResp)
	}

	orderResp := postJSONWithToken(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"order_type":    "market",
		"qty":           1,
		"leverage":      2,
	}, http.StatusCreated)
	if orderResp["status"] != store.OrderStatusFilled {
		t.Fatalf("unexpected order response: %+v", orderResp)
	}

	positions := getJSON(t, agent, ts.URL+"/positions", token, http.StatusOK)
	items := positions["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 position, got %+v", positions)
	}

	account := getJSON(t, agent, ts.URL+"/account", token, http.StatusOK)
	if account["wallet_balance"].(float64) != 1000 {
		t.Fatalf("unexpected account payload: %+v", account)
	}

	snapshot := getJSON(t, client, ts.URL+"/admin/monitor/sandboxes/sandbox-http/snapshot", "", http.StatusOK)
	if snapshot["sandbox"].(map[string]any)["id"] != "sandbox-http" {
		t.Fatalf("unexpected snapshot payload: %+v", snapshot)
	}
}

func postJSON(t *testing.T, client *http.Client, url string, payload any, wantStatus int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(mustJSON(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func postJSONWithToken(t *testing.T, client *http.Client, url, token string, payload any, wantStatus int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(mustJSON(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func getJSON(t *testing.T, client *http.Client, url, token string, wantStatus int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func mustJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

func postJSONWithTokenCaptureStatus(t *testing.T, client *http.Client, url, token string, payload any) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(mustJSON(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, body
}

func assertJSONDoesNotContainSubstrings(t *testing.T, payload any, forbidden ...string) {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	lower := strings.ToLower(string(raw))
	for _, needle := range forbidden {
		if needle == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(needle)) {
			t.Fatalf("payload leaked forbidden value %q: %s", needle, string(raw))
		}
	}
}

func uploadCSV(t *testing.T, client *http.Client, url, fileName, contents string, wantStatus int) map[string]any {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(contents)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return payload
}

func TestAccountWebSocketReceivesTradeEvents(t *testing.T) {
	app := newHTTPTestApp(t)

	ctx := context.Background()
	if _, err := app.Sandboxes.Create(ctx, service.CreateSandboxInput{
		ID:                "sandbox-ws",
		Name:              "WS Sandbox",
		StartDatetime:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ReplayCurrentTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ReplaySpeed:       1,
		DatasetID:         "dataset-http",
	}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := app.Sandboxes.Start(ctx, "sandbox-ws"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	account, err := app.Accounts.Create(ctx, service.CreateAccountInput{SandboxID: "sandbox-ws", Name: "ws-agent", InitialBalance: 1000})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	token, _, err := app.Tokens.Create(ctx, service.CreateTokenInput{AccountID: account.ID, Name: "ws-token", Scopes: []string{"trade:read", "trade:write", "account:read", "market:read"}})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/account"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	var first map[string]any
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read initial snapshot: %v", err)
	}
	if first["type"] != "snapshot" {
		t.Fatalf("unexpected initial message: %+v", first)
	}
	if first["account_id"] != account.ID {
		t.Fatalf("snapshot must be account scoped, got %+v", first)
	}
	payload, ok := first["payload"].(map[string]any)
	if !ok || payload["account"] == nil || payload["positions"] == nil || payload["open_orders"] == nil || payload["recent_trades"] == nil || payload["market"] == nil {
		t.Fatalf("snapshot missing account envelope fields: %+v", first)
	}

	agent := &http.Client{}
	postJSONWithToken(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"order_type":    "market",
		"qty":           1,
		"leverage":      2,
	}, http.StatusCreated)

	deadline := time.Now().Add(2 * time.Second)
	sawTrade := false
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			continue
		}
		if msg["type"] == "event" && msg["account_id"] == account.ID && msg["topic"] == "trade.executed" {
			sawTrade = true
			break
		}
	}
	if !sawTrade {
		t.Fatalf("expected trade.executed event over websocket")
	}
}

func TestAccountWebSocketUsesFrozenEnvelopeAndRedactsSecrets(t *testing.T) {
	app := newHTTPTestApp(t)

	ctx := context.Background()
	if _, err := app.Sandboxes.Create(ctx, service.CreateSandboxInput{
		ID:                "sandbox-ws-envelope",
		Name:              "WS Envelope Sandbox",
		StartDatetime:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ReplayCurrentTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ReplaySpeed:       1,
		DatasetID:         "dataset-http",
	}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := app.Sandboxes.Start(ctx, "sandbox-ws-envelope"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	account, err := app.Accounts.Create(ctx, service.CreateAccountInput{SandboxID: "sandbox-ws-envelope", Name: "ws-envelope-agent", InitialBalance: 1000})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	token, _, err := app.Tokens.Create(ctx, service.CreateTokenInput{AccountID: account.ID, Name: "ws-envelope-token", Scopes: []string{"trade:read", "trade:write", "account:read", "market:read"}})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/account"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	var snapshot map[string]any
	if err := conn.ReadJSON(&snapshot); err != nil {
		t.Fatalf("read initial snapshot: %v", err)
	}
	if snapshot["type"] != "snapshot" {
		t.Fatalf("unexpected initial message: %+v", snapshot)
	}
	if snapshot["account_id"] != account.ID {
		t.Fatalf("expected snapshot account_id %q, got %+v", account.ID, snapshot)
	}
	payload, ok := snapshot["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected snapshot payload object, got %+v", snapshot["payload"])
	}
	for _, key := range []string{"account", "positions", "open_orders", "recent_trades", "market"} {
		if _, exists := payload[key]; !exists {
			t.Fatalf("expected snapshot payload to contain %q, got %+v", key, payload)
		}
	}
	assertJSONDoesNotContainSubstrings(t, snapshot, token, "token_hash", "password", "salt", "api_key", "api_secret")

	agent := &http.Client{}
	postJSONWithToken(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"order_type":    "market",
		"qty":           1,
		"leverage":      2,
	}, http.StatusCreated)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			continue
		}
		if msg["topic"] != "trade.executed" {
			continue
		}
		if msg["type"] != "event" {
			t.Fatalf("expected event envelope type, got %+v", msg)
		}
		if msg["account_id"] != account.ID {
			t.Fatalf("expected event account_id %q, got %+v", account.ID, msg)
		}
		if msg["aggregate_id"] == nil || msg["created_at"] == nil {
			t.Fatalf("expected event aggregate_id and created_at, got %+v", msg)
		}
		assertJSONDoesNotContainSubstrings(t, msg, token, "token_hash", "password", "salt", "api_key", "api_secret")
		return
	}

	t.Fatalf("expected trade.executed event over websocket")
}

func TestAdminPlaceOrderAcceptsFrontendPayloadShape(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)

	postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-admin-orders",
		"name":                "Admin Orders Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-admin-orders/start", map[string]any{}, http.StatusOK)

	accountResp := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-admin-orders/accounts", map[string]any{
		"name":            "admin-order-agent",
		"initial_balance": 1000,
	}, http.StatusCreated)
	accountID := accountResp["id"].(string)

	orderResp := postJSON(t, client, ts.URL+"/admin/accounts/"+accountID+"/orders", map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"type":          "market",
		"quantity":      1,
		"leverage":      2,
	}, http.StatusCreated)

	if orderResp["order_type"] != store.OrderTypeMarket {
		t.Fatalf("expected market order_type, got %+v", orderResp)
	}
	if orderResp["qty"].(float64) != 1 {
		t.Fatalf("expected qty=1, got %+v", orderResp)
	}
}

func TestAdminLiveIndicatorsExposeTimeFieldForFrontend(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)

	payload := getJSON(t, client, ts.URL+"/admin/live/indicators?symbol=BTCUSDT&interval=1h", "", http.StatusOK)
	indicators, ok := payload["indicators"].(map[string]any)
	if !ok {
		t.Fatalf("expected indicators object, got %+v", payload)
	}
	obvSeries, ok := indicators["obv"].([]any)
	if !ok || len(obvSeries) == 0 {
		t.Fatalf("expected obv indicator points, got %+v", indicators)
	}
	firstPoint, ok := obvSeries[0].(map[string]any)
	if !ok {
		t.Fatalf("expected indicator point object, got %+v", obvSeries[0])
	}
	if _, ok := firstPoint["time"]; !ok {
		t.Fatalf("expected indicator point to include time field for frontend, got %+v", firstPoint)
	}
}

func TestReplayDatasetHTTPFlow(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	login := postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	if login["username"] != "root" {
		t.Fatalf("unexpected login response: %+v", login)
	}

	created := postJSON(t, client, ts.URL+"/admin/replay-datasets", map[string]any{
		"id":       "dataset-upload",
		"name":     "Upload Dataset",
		"symbol":   "BTCUSDT",
		"interval": "1m",
		"source":   "upload",
	}, http.StatusCreated)
	if created["id"] != "dataset-upload" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	uploadCSV(t, client, ts.URL+"/admin/replay-datasets/dataset-upload/import", "dataset.csv", "timestamp,open,high,low,close,volume\n2025-01-01T00:00:00Z,100,101,99,100,1\n2025-01-01T00:01:00Z,101,102,100,101,2\n", http.StatusAccepted)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		detail := getJSON(t, client, ts.URL+"/admin/replay-datasets/dataset-upload", "", http.StatusOK)
		latestJob, _ := detail["latest_import_job"].(map[string]any)
		if latestJob != nil && latestJob["status"] == store.DatasetImportStatusCompleted && detail["row_count"].(float64) == 2 {
			list := getJSON(t, client, ts.URL+"/admin/replay-datasets", "", http.StatusOK)
			items := list["items"].([]any)
			if len(items) == 0 {
				t.Fatalf("expected dataset list items, got %+v", list)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	detail := getJSON(t, client, ts.URL+"/admin/replay-datasets/dataset-upload", "", http.StatusOK)
	t.Fatalf("dataset import did not complete in time: %+v", detail)
}

func patchJSON(t *testing.T, client *http.Client, url string, payload any, wantStatus int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewBuffer(mustJSON(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("patch request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestReplayControlHTTPFlow(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-ctl",
		"name":                "Replay Control Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/start", map[string]any{}, http.StatusOK)

	accountResp := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/accounts", map[string]any{
		"name":            "agent-ctl",
		"initial_balance": 1000,
	}, http.StatusCreated)
	tokenResp := postJSON(t, client, ts.URL+"/tokens", map[string]any{
		"account_id": accountResp["id"],
		"name":       "control-token",
		"scopes":     []string{"trade:read", "trade:write", "account:read", "market:read"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)

	agent := &http.Client{}
	order := postJSONWithToken(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"order_type":    "limit",
		"qty":           1,
		"price":         99,
		"leverage":      2,
	}, http.StatusCreated)
	if order["status"] != store.OrderStatusNew {
		t.Fatalf("unexpected order response: %+v", order)
	}

	rejected := patchJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl", map[string]any{
		"replay_current_time": "2025-01-01T00:01:00Z",
	}, http.StatusBadRequest)
	if rejected["code"] == nil {
		t.Fatalf("expected validation error, got %+v", rejected)
	}

	seekRejected := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/replay/seek", map[string]any{
		"replay_current_time": "2025-01-01T00:01:00Z",
	}, http.StatusConflict)
	if seekRejected["code"] != "SANDBOX_SEEK_REQUIRES_PAUSE" {
		t.Fatalf("unexpected seek rejection response: %+v", seekRejected)
	}

	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/pause", map[string]any{}, http.StatusOK)
	if app.Runtime.IsRunning("sandbox-ctl") {
		t.Fatalf("expected pause endpoint to stop sandbox runtime")
	}
	seek := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/replay/seek", map[string]any{
		"replay_current_time": "2025-01-01T00:01:00Z",
	}, http.StatusOK)
	if seek["status"] != store.SandboxStatusPaused {
		t.Fatalf("unexpected seek response after pause: %+v", seek)
	}

	orderDetail := getJSON(t, agent, ts.URL+"/orders/"+order["id"].(string), token, http.StatusOK)
	if orderDetail["status"] != store.OrderStatusFilled {
		t.Fatalf("expected order to fill after seek, got %+v", orderDetail)
	}

	speed := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/replay/speed", map[string]any{
		"replay_speed": 4,
	}, http.StatusOK)
	if speed["speed"].(float64) != 4 {
		t.Fatalf("unexpected speed response: %+v", speed)
	}

	resumed := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/replay/resume", map[string]any{}, http.StatusOK)
	if resumed["status"] != store.SandboxStatusRunning {
		t.Fatalf("unexpected resume response: %+v", resumed)
	}
	if !app.Runtime.IsRunning("sandbox-ctl") {
		t.Fatalf("expected resume endpoint to restart sandbox runtime")
	}

	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/stop", map[string]any{}, http.StatusOK)
	if app.Runtime.IsRunning("sandbox-ctl") {
		t.Fatalf("expected stop endpoint to terminate sandbox runtime")
	}
	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/pause", map[string]any{}, http.StatusConflict)
	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/replay/resume", map[string]any{}, http.StatusConflict)
	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-ctl/start", map[string]any{}, http.StatusConflict)
	if app.Runtime.IsRunning("sandbox-ctl") {
		t.Fatalf("stopped sandbox should remain without runtime after failed restart attempts")
	}
}

func TestLiveSymbolsAdminMonitorEndpoint(t *testing.T) {
	app := newHTTPTestApp(t)
	if _, err := app.LiveMarket.GetTicker(context.Background(), "", "BTCUSDT"); err != nil {
		t.Fatalf("seed live ticker: %v", err)
	}

	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	payload := getJSON(t, client, ts.URL+"/admin/monitor/live-symbols", "", http.StatusOK)

	summary := payload["summary"].(map[string]any)
	if summary["active_symbols"].(float64) < 1 {
		t.Fatalf("unexpected live symbol summary: %+v", payload)
	}
	items := payload["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("expected live symbol items, got %+v", payload)
	}
	first := items[0].(map[string]any)
	if first["symbol"] != "BTCUSDT" {
		t.Fatalf("unexpected first live symbol: %+v", first)
	}
}

func postJSONWithTokenExpectStatus(t *testing.T, client *http.Client, url, token string, payload any, wantStatus int) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(mustJSON(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestLiveAccountHTTPFlow(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	created := postJSON(t, client, ts.URL+"/admin/live-accounts", map[string]any{
		"name":               "binance-paper-1",
		"initial_balance":    1000,
		"provider":           "binance",
		"environment":        "paper",
		"price_mode":         "live",
		"credentials_status": "healthy",
		"supported_symbols":  []string{"BTCUSDT", "ETHUSDT"},
		"api_key":            "paper-visible-key",
		"api_secret":         "paper-visible-secret",
	}, http.StatusCreated)
	if created["type"] != store.AccountTypeLive {
		t.Fatalf("unexpected live account response: %+v", created)
	}
	assertJSONDoesNotContainSubstrings(t, created, "paper-visible-key", "paper-visible-secret", "token_hash", "password", "salt", "api_key", "api_secret")

	list := getJSON(t, client, ts.URL+"/admin/live-accounts", "", http.StatusOK)
	if len(list["items"].([]any)) == 0 {
		t.Fatalf("expected live account list items, got %+v", list)
	}
	assertJSONDoesNotContainSubstrings(t, list, "paper-visible-key", "paper-visible-secret", "token_hash", "password", "salt", "api_key", "api_secret")

	detail := getJSON(t, client, ts.URL+"/admin/live-accounts/"+created["id"].(string), "", http.StatusOK)
	assertJSONDoesNotContainSubstrings(t, detail, "paper-visible-key", "paper-visible-secret", "token_hash", "password", "salt", "api_key", "api_secret")

	updated := patchJSON(t, client, ts.URL+"/admin/live-accounts/"+created["id"].(string), map[string]any{
		"credentials_status": "stale",
	}, http.StatusOK)
	if updated["credentials_status"] != "stale" {
		t.Fatalf("unexpected updated live account: %+v", updated)
	}
	assertJSONDoesNotContainSubstrings(t, updated, "paper-visible-key", "paper-visible-secret", "token_hash", "password", "salt", "api_key", "api_secret")

	tokenResp := postJSON(t, client, ts.URL+"/tokens", map[string]any{
		"account_id": created["id"],
		"name":       "live-token",
		"scopes":     []string{"account:read", "market:read", "trade:write"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)

	agent := &http.Client{}
	price := getJSON(t, agent, ts.URL+"/market/price?symbol=BTCUSDT", token, http.StatusOK)
	if price["price"].(float64) != 100 {
		t.Fatalf("unexpected live market price: %+v", price)
	}

	orderResp := postJSONWithToken(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"order_type":    "market",
		"qty":           1,
		"leverage":      1,
	}, http.StatusCreated)
	if orderResp["status"] != store.OrderStatusFilled || orderResp["sandbox_id"] != "" {
		t.Fatalf("unexpected paper live order response: %+v", orderResp)
	}
	assertJSONDoesNotContainSubstrings(t, orderResp, token, "paper-visible-key", "paper-visible-secret", "token_hash", "password", "salt", "api_key", "api_secret")
}

func TestLiveOrderClientOrderIDIsIdempotentOverHTTP(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-client-order-id",
		"name":                "Client Order ID Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-client-order-id/start", map[string]any{}, http.StatusOK)

	accountResp := postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-client-order-id/accounts", map[string]any{
		"name":            "agent-client-order-id",
		"initial_balance": 1000,
	}, http.StatusCreated)
	tokenResp := postJSON(t, client, ts.URL+"/tokens", map[string]any{
		"account_id": accountResp["id"],
		"name":       "client-order-id-token",
		"scopes":     []string{"trade:read", "trade:write", "account:read", "market:read"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)
	agent := &http.Client{}

	orderPayload := map[string]any{
		"symbol":          "BTCUSDT",
		"side":            "buy",
		"position_side":   "long",
		"order_type":      "limit",
		"qty":             1,
		"price":           99,
		"leverage":        2,
		"client_order_id": "strategy-001",
	}
	firstStatus, first := postJSONWithTokenCaptureStatus(t, agent, ts.URL+"/orders", token, orderPayload)
	if firstStatus != http.StatusCreated {
		t.Fatalf("expected first order status %d, got %d: %+v", http.StatusCreated, firstStatus, first)
	}

	secondStatus, second := postJSONWithTokenCaptureStatus(t, agent, ts.URL+"/orders", token, orderPayload)
	if secondStatus != http.StatusOK && secondStatus != http.StatusCreated {
		t.Fatalf("expected idempotent replay to return 200 or 201, got %d: %+v", secondStatus, second)
	}
	if first["id"] != second["id"] {
		t.Fatalf("expected same client_order_id to return existing order, got first=%+v second=%+v", first, second)
	}

	orders := getJSON(t, agent, ts.URL+"/orders", token, http.StatusOK)
	if got := len(orders["items"].([]any)); got != 1 {
		t.Fatalf("expected one persisted order for repeated client_order_id, got %d: %+v", got, orders)
	}

	conflictStatus, conflict := postJSONWithTokenCaptureStatus(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":          "BTCUSDT",
		"side":            "buy",
		"position_side":   "long",
		"order_type":      "limit",
		"qty":             2,
		"price":           99,
		"leverage":        2,
		"client_order_id": "strategy-001",
	})
	if conflictStatus != http.StatusConflict {
		t.Fatalf("expected conflicting payload to return %d, got %d: %+v", http.StatusConflict, conflictStatus, conflict)
	}
	if conflict["code"] != "CLIENT_ORDER_ID_CONFLICT" {
		t.Fatalf("expected conflict code CLIENT_ORDER_ID_CONFLICT, got %+v", conflict)
	}
}

func adminWSHeaders(t *testing.T, jar http.CookieJar, rawURL string) http.Header {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	headers := http.Header{}
	for _, cookie := range jar.Cookies(parsed) {
		headers.Add("Cookie", cookie.String())
	}
	return headers
}

func TestMonitorSnapshotIncludesReplayAndDatasetSummary(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-monitor",
		"name":                "Monitor Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)

	snapshot := getJSON(t, client, ts.URL+"/admin/monitor/sandboxes/sandbox-monitor/snapshot", "", http.StatusOK)
	if snapshot["replay_control"] == nil || snapshot["dataset"] == nil || snapshot["freshness_at"] == nil {
		t.Fatalf("unexpected monitor snapshot payload: %+v", snapshot)
	}
	dataset := snapshot["dataset"].(map[string]any)
	if dataset["id"] != "dataset-http" {
		t.Fatalf("unexpected dataset summary: %+v", dataset)
	}
}

func TestAdminMonitorWebSocketReceivesP1Events(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-monitor",
		"name":                "Monitor Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/admin/monitor?sandbox_id=sandbox-monitor"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, adminWSHeaders(t, jar, ts.URL))
	if err != nil {
		t.Fatalf("dial admin websocket: %v", err)
	}
	defer conn.Close()

	var first map[string]any
	if err := conn.ReadJSON(&first); err != nil {
		t.Fatalf("read initial snapshot: %v", err)
	}
	if first["type"] != "snapshot" {
		t.Fatalf("unexpected initial monitor message: %+v", first)
	}

	postJSON(t, client, ts.URL+"/admin/sandboxes/sandbox-monitor/replay/seek", map[string]any{
		"replay_current_time": "2025-01-01T00:01:00Z",
	}, http.StatusOK)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			continue
		}
		if msg["topic"] == "sandbox.replay.seek" {
			return
		}
	}
	t.Fatalf("expected sandbox.replay.seek event over admin monitor websocket")
}

func TestAdminEndpointsRequireRootRole(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	postJSON(t, client, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)

	if err := app.DB.Model(&store.User{}).Where("username = ?", "root").Update("role", 1).Error; err != nil {
		t.Fatalf("downgrade role: %v", err)
	}

	postJSON(t, client, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "should-forbid",
		"name":                "forbid",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusForbidden)
}

func TestAgentCannotAccessAdminOrReplayControlEndpoints(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	adminClient := &http.Client{Jar: jar}
	postJSON(t, adminClient, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, adminClient, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-guard-admin",
		"name":                "Guard Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	accountResp := postJSON(t, adminClient, ts.URL+"/admin/sandboxes/sandbox-guard-admin/accounts", map[string]any{
		"name":            "agent-guard",
		"initial_balance": 1000,
	}, http.StatusCreated)
	tokenResp := postJSON(t, adminClient, ts.URL+"/tokens", map[string]any{
		"account_id": accountResp["id"],
		"name":       "agent-guard-token",
		"scopes":     []string{"trade:read", "trade:write", "account:read", "market:read"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)

	agent := &http.Client{}
	postJSONWithTokenExpectStatus(t, agent, ts.URL+"/admin/sandboxes/sandbox-guard-admin/replay/seek", token, map[string]any{
		"replay_current_time": "2025-01-01T00:01:00Z",
	}, http.StatusUnauthorized)
	postJSONWithTokenExpectStatus(t, agent, ts.URL+"/admin/sandboxes", token, map[string]any{
		"id":                  "forbidden-create",
		"name":                "Forbidden Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusUnauthorized)
	postJSONWithTokenExpectStatus(t, agent, ts.URL+"/tokens", token, map[string]any{
		"account_id": accountResp["id"],
		"name":       "forbidden-token-create",
		"scopes":     []string{"market:read"},
	}, http.StatusUnauthorized)
}

func TestAgentTokenCanAccessOnlyTradingReadWriteAndAccountMarketReadPaths(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	adminClient := &http.Client{Jar: jar}
	postJSON(t, adminClient, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, adminClient, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-agent-allowed",
		"name":                "Agent Allowed Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	postJSON(t, adminClient, ts.URL+"/admin/sandboxes/sandbox-agent-allowed/start", map[string]any{}, http.StatusOK)
	accountResp := postJSON(t, adminClient, ts.URL+"/admin/sandboxes/sandbox-agent-allowed/accounts", map[string]any{
		"name":            "agent-allowed",
		"initial_balance": 1000,
	}, http.StatusCreated)
	tokenResp := postJSON(t, adminClient, ts.URL+"/tokens", map[string]any{
		"account_id": accountResp["id"],
		"name":       "agent-allowed-token",
		"scopes":     []string{"trade:read", "trade:write", "account:read", "market:read"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)

	agent := &http.Client{}
	sandboxTime := getJSON(t, agent, ts.URL+"/sandbox/time", token, http.StatusOK)
	if sandboxTime["sandbox_id"] != "sandbox-agent-allowed" {
		t.Fatalf("unexpected sandbox time response: %+v", sandboxTime)
	}
	if sandboxTime["current_time"] == nil || sandboxTime["status"] == nil {
		t.Fatalf("sandbox time missing fields: %+v", sandboxTime)
	}

	getJSON(t, agent, ts.URL+"/market/price?symbol=BTCUSDT", token, http.StatusOK)
	getJSON(t, agent, ts.URL+"/market/klines?symbol=BTCUSDT", token, http.StatusOK)
	getJSON(t, agent, ts.URL+"/account", token, http.StatusOK)
	getJSON(t, agent, ts.URL+"/positions", token, http.StatusOK)
	getJSON(t, agent, ts.URL+"/orders", token, http.StatusOK)

	orderResp := postJSONWithToken(t, agent, ts.URL+"/orders", token, map[string]any{
		"symbol":        "BTCUSDT",
		"side":          "buy",
		"position_side": "long",
		"order_type":    "limit",
		"qty":           1,
		"price":         99,
		"leverage":      2,
	}, http.StatusCreated)
	postJSONWithToken(t, agent, ts.URL+"/orders/"+orderResp["id"].(string)+"/cancel", token, map[string]any{}, http.StatusOK)
}

func TestMarketKlinesRejectsFutureWindowBeyondSandboxTime(t *testing.T) {
	app := newHTTPTestApp(t)
	engine := New(app)
	ts := httptest.NewServer(engine)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	adminClient := &http.Client{Jar: jar}
	postJSON(t, adminClient, ts.URL+"/admin/login", map[string]any{"username": "root", "password": "root-pass"}, http.StatusOK)
	postJSON(t, adminClient, ts.URL+"/admin/sandboxes", map[string]any{
		"id":                  "sandbox-klines-boundary",
		"name":                "Kline Boundary Sandbox",
		"start_datetime":      "2025-01-01T00:00:00Z",
		"replay_current_time": "2025-01-01T00:00:00Z",
		"replay_speed":        1,
		"dataset_id":          "dataset-http",
	}, http.StatusCreated)
	accountResp := postJSON(t, adminClient, ts.URL+"/admin/sandboxes/sandbox-klines-boundary/accounts", map[string]any{
		"name":            "agent-klines",
		"initial_balance": 1000,
	}, http.StatusCreated)
	tokenResp := postJSON(t, adminClient, ts.URL+"/tokens", map[string]any{
		"account_id": accountResp["id"],
		"name":       "agent-klines-token",
		"scopes":     []string{"trade:read", "trade:write", "account:read", "market:read"},
	}, http.StatusCreated)
	token := tokenResp["token"].(string)

	agent := &http.Client{}
	resp := getJSON(
		t,
		agent,
		ts.URL+"/market/klines?symbol=BTCUSDT&interval=1m&from=2025-01-01T00:00:00Z&to=2025-01-01T00:10:00Z",
		token,
		http.StatusOK,
	)
	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("unexpected klines payload: %+v", resp)
	}

	boundary := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, item := range items {
		k, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected kline item: %+v", item)
		}
		rawAt, ok := k["at"].(string)
		if !ok {
			t.Fatalf("unexpected kline at value: %+v", k["at"])
		}
		at, err := time.Parse(time.RFC3339, rawAt)
		if err != nil {
			t.Fatalf("parse kline at: %v", err)
		}
		if at.After(boundary) {
			t.Fatalf("found future candle %s beyond sandbox boundary %s", at.UTC().Format(time.RFC3339), boundary.Format(time.RFC3339))
		}
	}
}
