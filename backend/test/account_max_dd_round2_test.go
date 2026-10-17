package test

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"testing"
)

// TestAccountMaxDDSecondRoundNewThreshold 是 D2：resume 後第二輪用新 baseline
// 8991 與 loss_limit 1000（原始本金 10000*10%）：equity 7991.1 不觸發、7991
// 觸發；不能按 8991*10% 得到 899.1，也不是原 9000 門檻。新部位不能被舊輪
// 邏輯平掉。
func TestAccountMaxDDSecondRoundNewThreshold(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := stopAndLiquidate(t, a, "round2")
	if status, raw := resumeTrading(t, a, accID, tradingUserToken, nil); status != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", status, raw)
	}

	// 第二輪：現價 899.1 開新部位 10（margin = 10*899.1/10 = 899.1）。
	a.setPrice(t, 899.1)
	placeMarketOrder(t, a, token, 10, 10)
	if len(a.allPositions(t, accID)) != 1 {
		t.Fatalf("應有新部位")
	}

	// loss_limit 仍以原始本金算出 1000，不是 899.1。
	status, raw := a.request(t, http.MethodGet, "/v1/accounts/"+accID, tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("get account status=%d body=%s", status, raw)
	}
	approx(t, "loss_limit 仍以原始本金算", decodeView(t, raw)["max_dd_loss_limit"].(float64), 1000)

	// equity = 8991 + 10*(799.11-899.1) = 7991.1 > floor 7991：不觸發。
	a.setPrice(t, 799.11)
	if r := a.engine.Tick(context.Background()); r.Closed != 0 || r.Errors != 0 {
		t.Fatalf("7991.1 不應觸發：%+v", r)
	}
	if acc := getAccount(t, a, accID); acc.MaxDDStoppedAt != nil || len(a.allPositions(t, accID)) != 1 {
		t.Fatalf("新部位被舊輪邏輯動到：marker=%v positions=%d", acc.MaxDDStoppedAt, len(a.allPositions(t, accID)))
	}

	// equity = 8991 + 10*(799.1-899.1) = 7991 <= floor 7991：觸發並全平。
	a.setPrice(t, 799.1)
	if r := a.engine.Tick(context.Background()); r.Closed != 1 || r.Errors != 0 {
		t.Fatalf("7991 應觸發：%+v", r)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDStoppedAt == nil || len(a.allPositions(t, accID)) != 0 {
		t.Fatalf("應停止且全平：marker=%v positions=%d", acc.MaxDDStoppedAt, len(a.allPositions(t, accID)))
	}
	approx(t, "第二輪全平後 balance", acc.Balance, 7991)
}

// TestAccountMaxDDAdminOpsPreserveRiskState 是 D7/F3–F5：已停止後 rename、
// Token reset、PATCH disabled/active 都不清 marker、不改 baseline；PATCH
// active 不等於解除停止（下單仍被拒）。
func TestAccountMaxDDAdminOpsPreserveRiskState(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := stopAndLiquidate(t, a, "adminops")

	checkState := func(label string) {
		acc := getAccount(t, a, accID)
		if acc.MaxDDStoppedAt == nil {
			t.Fatalf("%s 後 marker 應保留", label)
		}
		if acc.MaxDDBaselineEquity == nil {
			t.Fatalf("%s 後 baseline 不應為 null", label)
		}
		approx(t, label+" 後 baseline 不變", *acc.MaxDDBaselineEquity, 10000)
	}

	if status, raw := a.request(t, http.MethodPatch, "/v1/accounts/"+accID, tradingUserToken, map[string]any{"user_name": "renamed-agent"}); status != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", status, raw)
	}
	checkState("rename")

	status, raw := a.request(t, http.MethodPost, "/v1/accounts/"+accID+"/token/reset", tradingUserToken, nil)
	if status != http.StatusOK {
		t.Fatalf("token reset status=%d body=%s", status, raw)
	}
	token = decodeView(t, raw)["token"].(string) // 舊 token 立即失效
	checkState("Token reset")

	for _, st := range []string{"disabled", "active"} {
		if status, raw := a.request(t, http.MethodPatch, "/v1/accounts/"+accID, tradingUserToken, map[string]any{"status": st}); status != http.StatusOK {
			t.Fatalf("PATCH %s status=%d body=%s", st, status, raw)
		}
		checkState("PATCH " + st)
	}

	// active 不等於解除：新下單仍被拒。
	if status, raw := a.request(t, http.MethodPost, "/v1/orders", token, map[string]any{
		"market": "crypto", "symbol": "BTCUSDT", "product": "futures",
		"side": "buy", "type": "market", "quantity": 1, "leverage": 10,
	}); status != http.StatusConflict {
		t.Fatalf("stopped 且 active 仍不能下單：status=%d body=%s", status, raw)
	}
}

// TestAccountMaxDDParallelResumeOneWinner 是 D5：兩個 USER 同時解除只有
// 一次成功、一次 max_dd_resumed 事件；第二次不能再移動起點。
func TestAccountMaxDDParallelResumeOneWinner(t *testing.T) {
	a := newTradingAPIZeroFee(t, "")
	accID, token := stopAndLiquidate(t, a, "parallel")

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], _ = a.request(t, http.MethodPost, "/v1/accounts/"+accID+"/trading/resume", tradingUserToken, nil)
		}(i)
	}
	wg.Wait()
	sort.Ints(statuses)
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
		t.Fatalf("應一次 200 一次 409：got %v", statuses)
	}
	if n := countLedgerEvent(t, a, token, "max_dd_resumed"); n != 1 {
		t.Fatalf("max_dd_resumed=%d want 1", n)
	}
	acc := getAccount(t, a, accID)
	if acc.MaxDDBaselineEquity == nil {
		t.Fatal("baseline 不應為 null")
	}
	approx(t, "起點只移動一次", *acc.MaxDDBaselineEquity, 8991)
}
