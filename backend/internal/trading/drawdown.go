package trading

import (
	"context"
	"errors"
	"time"

	"backend/internal/database"
)

// drawdownLimits 是 max_dd 風控的純計算結果：每輪可虧損金額永遠由原始
// initial_balance 與百分比決定，baseline 只決定本輪門檻落在哪裡。
// 供交易判斷與 Account view 共用，避免公式散落兩處。
type drawdownLimits struct {
	LossLimit   float64
	EquityFloor float64
}

// DrawdownLimits 是帳戶風控的唯一公式來源：loss_limit 以原始額度算出，
// floor 以本輪 baseline 減去 loss_limit。
func DrawdownLimits(initialBalance, baselineEquity, maxDD float64) drawdownLimits {
	lossLimit := initialBalance * (maxDD / 100)
	return drawdownLimits{
		LossLimit:   lossLimit,
		EquityFloor: baselineEquity - lossLimit,
	}
}

// TriggerMaxDD 是系統風控強制清倉的 trigger（規格 §5）。
const TriggerMaxDD = "max_dd"

// maxDDStopTolerance 是觸發比較的固定容差（規格 §4.6）：equity <= floor + 容差即視為觸發。
const maxDDStopTolerance = 1e-8

// ErrMaxDDStopped 是帳號已觸發 max_dd 風控停止：任何新交易（下單、平倉、改 stops）
// 都不能再執行；撤單與查詢維持原授權語意。
var ErrMaxDDStopped = errors.New("帳號已觸發 max_dd 風控停止，交易已停止")

// ErrMaxDDInconsistent 是 max_dd 已設定但 baseline 缺失（E7）：
// 不得當成未設定或自動補 initial，必須回錯且不成交。
var ErrMaxDDInconsistent = errors.New("max_dd 設定不完整：缺少 baseline equity")

// ErrRiskPriceUnavailable 是帳號部位在本輪缺少行情（E1）：
// 跳過這輪風控評估、下輪重試，不能以 0 或部分部位 equity 做決定。
var ErrRiskPriceUnavailable = errors.New("風控評估缺少現價行情")

// checkMaxDD 在帳號已鎖定的交易內重讀帳號與全部部位，依最新 equity 重算 max_dd
// （規格 §4）。已停止直接回 true 不再寫；未設定回 false；baseline 缺失回
// ErrMaxDDInconsistent；任一部位缺價回 ErrRiskPriceUnavailable。
// 觸發時在同一個交易內寫停止標記、撤掉全部掛單（各一筆 order_canceled）、
// 寫一筆 max_dd_triggered ledger（BalanceAfter 記錄觸發時 equity），一起提交或一起回滾。
func (s *Service) checkMaxDD(ctx context.Context, tx *database.Store, accountID string, marks map[string]float64) (bool, error) {
	account, err := tx.AccountByID(ctx, accountID)
	if err != nil {
		return false, err
	}
	if account.MaxDDStoppedAt != nil {
		return true, nil
	}
	if account.MaxDD == nil {
		return false, nil
	}
	if account.MaxDDBaselineEquity == nil {
		return false, ErrMaxDDInconsistent
	}

	positions, err := tx.ListPositions(ctx, accountID, "")
	if err != nil {
		return false, err
	}
	equity := account.Balance
	for i := range positions {
		price, ok := marks[key(positions[i].Market, positions[i].Symbol)]
		if !ok {
			return false, ErrRiskPriceUnavailable
		}
		equity += Unrealized(&positions[i], price)
	}

	limits := DrawdownLimits(account.InitialBalance, *account.MaxDDBaselineEquity, *account.MaxDD)
	if equity > limits.EquityFloor+maxDDStopTolerance {
		return false, nil
	}

	now := time.Now().UTC()
	account.MaxDDStoppedAt = &now
	if err := tx.SaveAccount(ctx, account); err != nil {
		return false, err
	}
	orders, err := tx.OpenOrdersForAccount(ctx, accountID)
	if err != nil {
		return false, err
	}
	for i := range orders {
		orders[i].Status = database.OrderCanceled
		if err := tx.SaveOrder(ctx, &orders[i]); err != nil {
			return false, err
		}
		if err := tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
			AccountID: accountID, Event: database.LedgerOrderCanceled, OrderID: orders[i].ID,
			Quantity: orders[i].Quantity, Price: orders[i].Price,
		}); err != nil {
			return false, err
		}
	}
	return true, tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
		AccountID:    accountID,
		Event:        database.LedgerMaxDDTriggered,
		BalanceAfter: equity,
	})
}

// enforceMaxDD 是 max_dd 風控的完整流程（規格 §4／§5）：階段一在單一交易裡
// 鎖帳號、重算並（若觸發）寫停止標記、撤單、風控 ledger 後 commit；階段二在
// 標記落盤後逐筆以 max_dd trigger 走原結算路徑全平。已停止的帳號不需要再
// 證明 equity 低於門檻，行情回升也必須把清倉做完；每筆清倉各自 transaction，
// 某筆失敗不解除停止、不撤銷其他已成交，剩餘部位由後續 Tick 重試。
// closed 是這輪強制清倉的筆數。
func (s *Service) enforceMaxDD(ctx context.Context, accountID string, marks map[string]float64) (int, error) {
	var stopped bool
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		var err error
		stopped, err = s.checkMaxDD(ctx, tx, accountID, marks)
		return err
	})
	if err != nil {
		return 0, err
	}
	if !stopped {
		return 0, nil
	}
	return s.liquidateMaxDD(ctx, accountID, marks)
}

// liquidateMaxDD 對已停止帳號逐部位以 max_dd trigger 全平（規格 §4.5／§5.6）：
// 每筆獨立 transaction 原子提交；缺報價的部位留待下輪重試；業務性拒絕
// （部位已不存在等）視為已完成跳過；其餘錯誤記 firstErr 但不影響其他部位。
func (s *Service) liquidateMaxDD(ctx context.Context, accountID string, marks map[string]float64) (int, error) {
	positions, err := s.store.ListPositions(ctx, accountID, "")
	if err != nil {
		return 0, err
	}
	closed := 0
	var firstErr error
	for i := range positions {
		price, ok := marks[key(positions[i].Market, positions[i].Symbol)]
		if !ok {
			continue // 缺報價：留待下輪重試
		}
		if _, err := s.closeAt(ctx, accountID, positions[i].ID, 0, price, database.Source{}, TriggerMaxDD); err != nil {
			if isBusinessRejection(err) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		closed++
	}
	return closed, firstErr
}
