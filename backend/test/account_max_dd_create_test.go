package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"backend/internal/account"
	"backend/internal/database"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// maxDDAccountView 是 max_dd 驗收要斷言的 account 欄位子集。
// 指標型別讓 JSON null 解碼成 nil，與 0 明確區分。
type maxDDAccountView struct {
	ID                  string   `json:"id"`
	UserName            string   `json:"user_name"`
	InitialBalance      float64  `json:"initial_balance"`
	Balance             float64  `json:"balance"`
	Status              string   `json:"status"`
	Token               string   `json:"token"`
	MaxDD               *float64 `json:"max_dd"`
	MaxDDBaselineEquity *float64 `json:"max_dd_baseline_equity"`
	MaxDDStoppedAt      *string  `json:"max_dd_stopped_at"`
	MaxDDLossLimit      *float64 `json:"max_dd_loss_limit"`
	MaxDDEquityFloor    *float64 `json:"max_dd_equity_floor"`
}

func approx(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("%s 預期 %v，得到 %v", what, want, got)
	}
}

// postMaxDD 建立帳號，maxDD 為 nil 時省略欄位，maxDD 為 &null 時送 JSON null。
func postMaxDD(t *testing.T, a *tradingAPI, name string, balance float64, maxDD *float64) (int, maxDDAccountView) {
	t.Helper()
	body := map[string]any{"user_name": name, "initial_balance": balance}
	if maxDD != nil {
		body["max_dd"] = *maxDD
	}
	status, raw := a.request(t, http.MethodPost, "/v1/accounts", tradingUserToken, body)
	var view maxDDAccountView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode account: %v %s", err, raw)
	}
	return status, view
}

func getAccountView(t *testing.T, a *tradingAPI, id string) (int, maxDDAccountView) {
	t.Helper()
	status, raw := a.request(t, http.MethodGet, "/v1/accounts/"+id, tradingUserToken, nil)
	var view maxDDAccountView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode account: %v %s", err, raw)
	}
	return status, view
}

// selfAccountRaw 用 Account Token 自查，回傳原始 JSON 以檢查 token 鍵是否存在。
func selfAccountRaw(t *testing.T, a *tradingAPI, token string) (int, map[string]any) {
	t.Helper()
	status, raw := a.request(t, http.MethodGet, "/v1/account", token, nil)
	body := map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode self account: %v %s", err, raw)
	}
	return status, body
}

// A1：省略與顯式 null 都是未設定；五個 risk 欄位都是 null 而不是 0。
func TestAccountMaxDDCreateUnset(t *testing.T) {
	a := newTradingAPI(t, "")

	status, view := postMaxDD(t, a, "unset-omit", 10000, nil)
	if status != http.StatusCreated {
		t.Fatalf("省略 max_dd 應 201，得到 %d", status)
	}
	assertRiskFieldsNull(t, "create(omit)", view)

	// 顯式 JSON null 也是未設定。
	status, raw := a.request(t, http.MethodPost, "/v1/accounts", tradingUserToken, map[string]any{"user_name": "unset-explicit-null", "initial_balance": 10000, "max_dd": nil})
	if status != http.StatusCreated {
		t.Fatalf("顯式 null 應 201，得到 %d: %s", status, raw)
	}
	var v2 maxDDAccountView
	if err := json.Unmarshal(raw, &v2); err != nil {
		t.Fatalf("decode: %v %s", err, raw)
	}
	assertRiskFieldsNull(t, "create(null)", v2)

	// List 一致
	status, raw = a.request(t, http.MethodGet, "/v1/accounts", tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, raw)
	}
	var list struct {
		Accounts []maxDDAccountView `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Accounts) != 2 {
		t.Fatalf("預期 2 個帳號，得到 %d", len(list.Accounts))
	}
	for _, v := range list.Accounts {
		assertRiskFieldsNull(t, "list("+v.UserName+")", v)
	}
}

func assertRiskFieldsNull(t *testing.T, where string, view maxDDAccountView) {
	t.Helper()
	if view.MaxDD != nil || view.MaxDDBaselineEquity != nil || view.MaxDDStoppedAt != nil || view.MaxDDLossLimit != nil || view.MaxDDEquityFloor != nil {
		t.Fatalf("%s 的 risk 欄位應全為 null: %+v", where, view)
	}
}

// A2：10、0.5、100 都可用；baseline=initial，loss_limit 與 floor 用算好的數字。
func TestAccountMaxDDCreateValues(t *testing.T) {
	a := newTradingAPI(t, "")
	cases := []struct {
		maxDD     float64
		lossLimit float64
		floor     float64
	}{
		{10, 1000, 9000},
		{0.5, 50, 9950},
		{100, 10000, 0},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("max_dd=%v", tc.maxDD), func(t *testing.T) {
			value := tc.maxDD
			status, created := postMaxDD(t, a, fmt.Sprintf("dd-%v", tc.maxDD), 10000, &value)
			if status != http.StatusCreated {
				t.Fatalf("建立應 201，得到 %d", status)
			}
			assertLimits(t, "create", created, tc)

			status, got := getAccountView(t, a, created.ID)
			if status != http.StatusOK {
				t.Fatalf("get: %d", status)
			}
			assertLimits(t, "get", got, tc)

			status, raw := a.request(t, http.MethodGet, "/v1/accounts", tradingUserToken, nil)
			if status != http.StatusOK {
				t.Fatalf("list: %d", status)
			}
			var list struct {
				Accounts []maxDDAccountView `json:"accounts"`
			}
			if err := json.Unmarshal(raw, &list); err != nil {
				t.Fatalf("decode list: %v", err)
			}
			found := false
			for _, v := range list.Accounts {
				if v.ID == created.ID {
					assertLimits(t, "list", v, tc)
					found = true
				}
			}
			if !found {
				t.Fatalf("list 找不到 %s", created.ID)
			}

			status, self := selfAccountRaw(t, a, created.Token)
			if status != http.StatusOK {
				t.Fatalf("self: %d %v", status, self)
			}
			selfView, err := json.Marshal(self)
			if err != nil {
				t.Fatalf("marshal self: %v", err)
			}
			var sv maxDDAccountView
			if err := json.Unmarshal(selfView, &sv); err != nil {
				t.Fatalf("decode self: %v", err)
			}
			assertLimits(t, "self", sv, tc)
			if _, hasToken := self["token"]; hasToken {
				t.Fatalf("Account self 不能回傳 token: %v", self)
			}
		})
	}
}

func assertLimits(t *testing.T, where string, view maxDDAccountView, tc struct {
	maxDD     float64
	lossLimit float64
	floor     float64
}) {
	t.Helper()
	if view.MaxDD == nil || *view.MaxDD != tc.maxDD {
		t.Fatalf("%s max_dd 預期 %v，得到 %v", where, tc.maxDD, view.MaxDD)
	}
	if view.MaxDDBaselineEquity == nil || *view.MaxDDBaselineEquity != 10000 {
		t.Fatalf("%s baseline 預期 10000，得到 %v", where, view.MaxDDBaselineEquity)
	}
	if view.MaxDDLossLimit == nil {
		t.Fatalf("%s loss_limit 應非 null", where)
	}
	approx(t, where+" loss_limit", *view.MaxDDLossLimit, tc.lossLimit)
	if view.MaxDDEquityFloor == nil {
		t.Fatalf("%s floor 應非 null", where)
	}
	approx(t, where+" floor", *view.MaxDDEquityFloor, tc.floor)
	if view.MaxDDStoppedAt != nil {
		t.Fatalf("%s 新帳號不應有停止標記", where)
	}
}

// A3：0、負、>100、錯誤型別都 400 且不留任何資料；direct service 的 NaN/±Inf 同樣拒絕。
func TestAccountMaxDDCreateInvalid(t *testing.T) {
	a := newTradingAPI(t, "")
	bad := []struct {
		name  string
		maxDD any
	}{
		{"zero", 0.0},
		{"negative", -1.0},
		{"over100", 100.01},
		{"string", "10"},
		{"bool", true},
		{"object", map[string]any{"max_dd": 10}},
		{"array", []any{10}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			status, raw := a.request(t, http.MethodPost, "/v1/accounts", tradingUserToken, map[string]any{"user_name": "bad-" + tc.name, "initial_balance": 1000, "max_dd": tc.maxDD})
			if status != http.StatusBadRequest {
				t.Fatalf("max_dd=%v 應 400，得到 %d: %s", tc.maxDD, status, raw)
			}
		})
	}

	status, raw := a.request(t, http.MethodGet, "/v1/accounts", tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("list: %d", status)
	}
	var list struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Accounts) != 0 {
		t.Fatalf("非法 max_dd 不應建立任何帳號，得到 %d 個", len(list.Accounts))
	}

	svc := account.New(a.store)
	direct := []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -1, 100.01}
	for _, v := range direct {
		pct := v
		_, err := svc.Create(context.Background(), account.CreateInput{UserName: "svc-direct", InitialBalance: 1000, MaxDD: &pct})
		if !errors.Is(err, account.ErrInvalidMaxDD) {
			t.Fatalf("direct service max_dd=%v 應拒絕，得到 %v", v, err)
		}
	}

	ok := []float64{0.5, 100}
	for i, v := range ok {
		pct := v
		acc, err := svc.Create(context.Background(), account.CreateInput{UserName: fmt.Sprintf("svc-ok-%d", i), InitialBalance: 1000, MaxDD: &pct})
		if err != nil {
			t.Fatalf("direct service max_dd=%v 應接受，得到 %v", v, err)
		}
		if acc.MaxDD == nil || *acc.MaxDD != v {
			t.Fatalf("MaxDD 預期 %v，得到 %v", v, acc.MaxDD)
		}
		if acc.MaxDDBaselineEquity == nil || *acc.MaxDDBaselineEquity != 1000 {
			t.Fatalf("baseline 應為 initial_balance 1000，得到 %v", acc.MaxDDBaselineEquity)
		}
		if acc.Balance != 1000 || acc.InitialBalance != 1000 {
			t.Fatalf("建立 max_dd 不得改動 balance/initial_balance: %v/%v", acc.Balance, acc.InitialBalance)
		}
	}
}

// A4：Create→List→Get→self→重啟再讀全部一致；Account self 不暴露 Token。
func TestAccountMaxDDCreateViewsConsistent(t *testing.T) {
	a := newTradingAPI(t, "")
	value := float64(10)
	status, created := postMaxDD(t, a, "consistent", 10000, &value)
	if status != http.StatusCreated {
		t.Fatalf("create: %d", status)
	}

	status, got := getAccountView(t, a, created.ID)
	if status != http.StatusOK {
		t.Fatalf("get: %d", status)
	}
	if got.MaxDD == nil || *got.MaxDD != 10 || got.MaxDDBaselineEquity == nil || *got.MaxDDBaselineEquity != 10000 {
		t.Fatalf("get 不一致: %+v", got)
	}

	status, self := selfAccountRaw(t, a, created.Token)
	if status != http.StatusOK {
		t.Fatalf("self: %d", status)
	}
	if _, has := self["token"]; has {
		t.Fatalf("Account self 不能含 token: %v", self)
	}
	if self["max_dd"] != float64(10) || self["max_dd_baseline_equity"] != float64(10000) {
		t.Fatalf("self 不一致: %v", self)
	}
	if self["max_dd_loss_limit"] != float64(1000) || self["max_dd_equity_floor"] != float64(9000) {
		t.Fatalf("self derived 不一致: %v", self)
	}

	// 重啟：同一個 DB 檔案再開一個 store 讀回。
	reopened, err := database.OpenSQLite(a.dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	acc, err := reopened.AccountByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read after restart: %v", err)
	}
	if acc.MaxDD == nil || *acc.MaxDD != 10 {
		t.Fatalf("重啟後 MaxDD 不一致: %v", acc.MaxDD)
	}
	if acc.MaxDDBaselineEquity == nil || *acc.MaxDDBaselineEquity != 10000 {
		t.Fatalf("重啟後 baseline 不一致: %v", acc.MaxDDBaselineEquity)
	}
	if acc.MaxDDStoppedAt != nil {
		t.Fatalf("重啟後不該有停止標記")
	}
	if acc.Balance != created.Balance || acc.InitialBalance != created.InitialBalance || acc.Token != created.Token {
		t.Fatalf("重啟後 balance/initial/token 被改動: %v %v %v", acc.Balance, acc.InitialBalance, acc.Token)
	}
}

// legacyAccountRow 是新增 max_dd 欄位之前的 Account schema，用來模擬舊資料。
type legacyAccountRow struct {
	ID             string  `gorm:"primaryKey;size:64"`
	UserName       string  `gorm:"size:64;not null;uniqueIndex"`
	Token          string  `gorm:"size:128;not null;uniqueIndex"`
	InitialBalance float64 `gorm:"not null"`
	Balance        float64 `gorm:"not null"`
	Status         string  `gorm:"size:16;not null;index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (legacyAccountRow) TableName() string { return "accounts" }

// A5：舊 schema 的帳號 Migrate 兩次後新欄位為 null，balance/initial/token 不變，第二次不重設任何值。
func TestAccountMaxDDCreateMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	rawDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if err := rawDB.AutoMigrate(&legacyAccountRow{}); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	now := time.Now().UTC()
	legacy := legacyAccountRow{
		ID: "legacy-1", UserName: "legacy-user", Token: "legacy-token",
		InitialBalance: 5000, Balance: 4000, Status: database.AccountActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := rawDB.Create(&legacy).Error; err != nil {
		t.Fatalf("insert legacy account: %v", err)
	}
	sqlDB, err := rawDB.DB()
	if err != nil {
		t.Fatalf("get legacy sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := database.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	acc, err := store.AccountByID(context.Background(), "legacy-1")
	if err != nil {
		t.Fatalf("read legacy account: %v", err)
	}
	assertMigratedLegacy(t, acc)

	if err := store.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	acc2, err := store.AccountByID(context.Background(), "legacy-1")
	if err != nil {
		t.Fatalf("read after second migrate: %v", err)
	}
	assertMigratedLegacy(t, acc2)
}

func assertMigratedLegacy(t *testing.T, acc *database.Account) {
	t.Helper()
	if acc.MaxDD != nil || acc.MaxDDBaselineEquity != nil || acc.MaxDDStoppedAt != nil {
		t.Fatalf("舊帳號的新欄位應為 null: %+v", acc)
	}
	if acc.InitialBalance != 5000 || acc.Balance != 4000 || acc.Token != "legacy-token" {
		t.Fatalf("遷移不得改動既有資料: %+v", acc)
	}
}
