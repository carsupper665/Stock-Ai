package test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func requireAccountError(t *testing.T, status int, raw []byte, want string) {
	t.Helper()
	if status != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409/%s", status, raw, want)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Error != want {
		t.Fatalf("body=%s, want error=%s (decode: %v)", raw, want, err)
	}
}

func TestAccountMaxDDNameTakenCode(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	a.createAccount(t, "same-name", 10000)
	status, raw := a.request(t, http.MethodPost, "/v1/accounts", tradingUserToken, map[string]any{"user_name": "same-name", "initial_balance": 10000})
	requireAccountError(t, status, raw, "name_taken")
	var accounts struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	status, raw = a.request(t, http.MethodGet, "/v1/accounts", tradingUserToken, nil)
	if status != http.StatusOK || json.Unmarshal(raw, &accounts) != nil || len(accounts.Accounts) != 1 {
		t.Fatalf("duplicate create changed accounts: status=%d body=%s", status, raw)
	}
}

// resumeTrading POST max_dd 解除停止請求；token 是呼叫身分（USER 或 Account Token）。
func resumeTrading(t *testing.T, a *tradingAPI, accountID, token string, body any) (int, []byte) {
	t.Helper()
	return a.request(t, http.MethodPost, "/v1/accounts/"+accountID+"/trading/resume", token, body)
}

// decodeView 把帳號回應解成 map：null 欄位是 nil，方便逐欄比對。
func decodeView(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode account view: %v %s", err, raw)
	}
	return view
}

// stopAndLiquidate 建立 max_dd 帳號、開倉、跌價觸發停止並以一個 Tick 完成
// 全平：回傳已停止、零部位、balance = 全平後 equity 的帳號（D1 情境）。
func stopAndLiquidate(t *testing.T, a *tradingAPI, name string) (string, string) {
	t.Helper()
	accID, token := createMaxDDAccount(t, a, name, 10000, 10)
	a.setPrice(t, 1000)
	placeMarketOrder(t, a, token, 10, 10) // margin = 10*1000/10 = 1000
	a.setPrice(t, 899.1)                  // equity = 10000 + 10*(899.1-1000) = 8991 <= floor 9000
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("stop tick: %+v", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil || len(a.allPositions(t, accID)) != 0 {
		t.Fatalf("應已停止且全平：marker=%v positions=%d", acc.MaxDDStoppedAt, len(a.allPositions(t, accID)))
	}
	return accID, token
}

// TestAccountMaxDDResumeStartsNewRound 是 AC1＋AC4：全平 balance 8991 後 USER
// 解除，baseline=8991、loss_limit 仍以原始本金算出 1000、floor=7991；marker
// 清除、Status 回 active；Token/Balance/initial 不變；payload 指定的
// balance/baseline/新百分比一律忽略。
func TestAccountMaxDDResumeStartsNewRound(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := stopAndLiquidate(t, a, "resume")

	approx(t, "全平後 balance", getAccount(t, a, accID).Balance, 8991)

	status, raw := resumeTrading(t, a, accID, tradingUserToken, map[string]any{
		"balance": 5000, "max_dd_baseline_equity": 9999, "max_dd": 50,
	})
	if status != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", status, raw)
	}
	view := decodeView(t, raw)
	if view["max_dd"] != 10.0 {
		t.Fatalf("max_dd 應沿用 10：got %v", view["max_dd"])
	}
	approx(t, "新 baseline", view["max_dd_baseline_equity"].(float64), 8991)
	approx(t, "loss_limit", view["max_dd_loss_limit"].(float64), 1000)
	approx(t, "新 floor", view["max_dd_equity_floor"].(float64), 7991)
	if view["max_dd_stopped_at"] != nil {
		t.Fatalf("marker 應清除：got %v", view["max_dd_stopped_at"])
	}
	if view["status"] != "active" {
		t.Fatalf("Status 應回 active：got %v", view["status"])
	}
	if view["token"] != token {
		t.Fatalf("Token 不應改變：got %v", view["token"])
	}
	approx(t, "Balance", view["balance"].(float64), 8991)
	if view["initial_balance"] != 10000.0 {
		t.Fatalf("initial_balance 不應改變：got %v", view["initial_balance"])
	}
	if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 1 {
		t.Fatalf("max_dd_resumed=%d want 1", n)
	}

	// 解除後一般交易恢復（不再 409）。
	placeMarketOrder(t, a, token, 1, 10)
}

// TestAccountMaxDDResumeRejectsInvalidState 是 AC2：未停止、未啟用 max_dd、
// 強平未完成（剩部位或掛單）、Account Token、重複解除都拒絕，且不移動
// baseline。
func TestAccountMaxDDResumeRejectsInvalidState(t *testing.T) {
	t.Run("not_stopped", func(t *testing.T) {
		a := newTradingAPIZeroFee(t, "")
		accID, _ := createMaxDDAccount(t, a, "active-acc", 10000, 10)
		status, raw := resumeTrading(t, a, accID, tradingUserToken, nil)
		requireAccountError(t, status, raw, "trading_not_stopped")
	})

	t.Run("no_max_dd", func(t *testing.T) {
		a := newTradingAPIZeroFee(t, "")
		accID, _ := a.createAccount(t, "plain-acc", 10000)
		status, raw := resumeTrading(t, a, accID, tradingUserToken, nil)
		requireAccountError(t, status, raw, "trading_not_stopped")
	})

	t.Run("pending_position", func(t *testing.T) {
		a := newTradingAPIZeroFee(t, "")
		accID, token := createMaxDDAccount(t, a, "pending-pos", 10000, 10)
		a.setPrice(t, 1000)
		placeMarketOrder(t, a, token, 1, 10)
		injectMaxDDMarker(t, a, accID)
		status, raw := resumeTrading(t, a, accID, tradingUserToken, nil)
		requireAccountError(t, status, raw, "liquidation_pending")
		acc := getAccount(t, a, accID)
		if acc.MaxDDStoppedAt == nil {
			t.Fatal("409 不得清除 marker")
		}
		if acc.MaxDDBaselineEquity == nil || *acc.MaxDDBaselineEquity != 10000 {
			t.Fatalf("409 不得移動 baseline：got %v", acc.MaxDDBaselineEquity)
		}
		if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 0 {
			t.Fatalf("409 wrote resume ledger: %d", n)
		}
	})

	t.Run("pending_order", func(t *testing.T) {
		a := newTradingAPIZeroFee(t, "")
		accID, token := createMaxDDAccount(t, a, "pending-order", 10000, 10)
		placeLimitOrder(t, a, token, "buy", 100, nil)
		injectMaxDDMarker(t, a, accID)
		status, raw := resumeTrading(t, a, accID, tradingUserToken, nil)
		requireAccountError(t, status, raw, "liquidation_pending")
	})

	t.Run("account_token", func(t *testing.T) {
		a := newTradingAPIZeroFee(t, "")
		accID, token := stopAndLiquidate(t, a, "token-acc")
		status, raw := resumeTrading(t, a, accID, token, nil)
		if status != http.StatusForbidden {
			t.Fatalf("Account Token 解除 status=%d body=%s", status, raw)
		}
		if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt == nil {
			t.Fatal("403 不得清除 marker")
		}
	})

	t.Run("double_resume", func(t *testing.T) {
		a := newTradingAPIZeroFee(t, "")
		accID, token := stopAndLiquidate(t, a, "double")
		if status, raw := resumeTrading(t, a, accID, tradingUserToken, nil); status != http.StatusOK {
			t.Fatalf("第一次解除 status=%d body=%s", status, raw)
		}
		status, raw := resumeTrading(t, a, accID, tradingUserToken, nil)
		requireAccountError(t, status, raw, "trading_not_stopped")
		if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 1 {
			t.Fatalf("重複解除後 max_dd_resumed=%d want 1", n)
		}
		if acc := getAccount(t, a, accID); acc.MaxDDBaselineEquity == nil {
			t.Fatal("重複解除後 baseline 不應為 null")
		} else {
			approx(t, "重複解除不得再移動 baseline", *acc.MaxDDBaselineEquity, 8991)
		}
	})
}

// blockResumeWrite 以第二個連線建立 trigger，阻擋 max_dd_stopped_at 由非 null
// 改回 null 的 UPDATE（AC4：解除寫入失敗注入）。
func blockResumeWrite(t *testing.T, a *tradingAPI) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := conn.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	sql := "CREATE TRIGGER fail_resume_mark BEFORE UPDATE ON accounts WHEN OLD.max_dd_stopped_at IS NOT NULL AND NEW.max_dd_stopped_at IS NULL BEGIN SELECT RAISE(ABORT, 'injected resume failure'); END"
	if err := conn.WithContext(context.Background()).Exec(sql).Error; err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
}

// unblockResumeWrite 移除故障 trigger，讓重試的解除可以正常提交。
func unblockResumeWrite(t *testing.T, a *tradingAPI) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(a.dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	defer func() {
		if sqlDB, dbErr := conn.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	}()
	if err := conn.WithContext(context.Background()).Exec("DROP TRIGGER fail_resume_mark").Error; err != nil {
		t.Fatalf("drop fault trigger: %v", err)
	}
}

// TestAccountMaxDDResumeFailureRollsBack 是 AC4：marker、baseline、
// max_dd_resumed Ledger 原子提交——解除寫入失敗時 marker 與 baseline 都不動、
// 不寫 ledger；故障清除後重試成功且 ledger 恰好一筆。
func TestAccountMaxDDResumeFailureRollsBack(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := stopAndLiquidate(t, a, "atomic")

	blockResumeWrite(t, a)
	status, raw := resumeTrading(t, a, accID, tradingUserToken, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("故障解除 status=%d body=%s", status, raw)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil {
		t.Fatal("失敗的解除不得清除 marker")
	}
	if acc.MaxDDBaselineEquity == nil || *acc.MaxDDBaselineEquity != 10000 {
		t.Fatalf("失敗的解除不得移動 baseline：got %v", acc.MaxDDBaselineEquity)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 0 {
		t.Fatalf("失敗的解除不得寫 max_dd_resumed：%d", n)
	}

	unblockResumeWrite(t, a)
	status, raw = resumeTrading(t, a, accID, tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("重試解除 status=%d body=%s", status, raw)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 1 {
		t.Fatalf("max_dd_resumed=%d want 1", n)
	}
	acc = getAccount(t, a, accID)
	if acc.MaxDDBaselineEquity == nil {
		t.Fatal("重試後 baseline 應有值")
	}
	approx(t, "重試後 baseline", *acc.MaxDDBaselineEquity, 8991)
}
