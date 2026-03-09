package router

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"
    "net/http/cookiejar"
    "net/http/httptest"
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
        "scopes":      []string{"trade:read", "trade:write", "account:read", "market:read"},
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
        if msg["topic"] == "trade.executed" {
            sawTrade = true
            break
        }
    }
    if !sawTrade {
        t.Fatalf("expected trade.executed event over websocket")
    }
}



