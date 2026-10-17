package trading

import (
	"context"
	"errors"
	"fmt"

	"backend/internal/database"
	"backend/internal/id"
	"backend/internal/market"
)

type Service struct {
	store  *database.Store
	prices *market.Runtime
	maker  float64
	taker  float64
}

func New(store *database.Store, prices *market.Runtime, makerFee, takerFee float64) *Service {
	return &Service{store: store, prices: prices, maker: makerFee, taker: takerFee}
}

// PlaceOrder 下單。市價單立刻以現價成交；限價單掛著等撮合引擎觸發。
func (s *Service) PlaceOrder(ctx context.Context, accountID string, in PlaceOrderInput) (*database.Order, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}

	orderID, err := id.New("ord_")
	if err != nil {
		return nil, err
	}
	order := &database.Order{
		ID:         orderID,
		AccountID:  accountID,
		Market:     in.Market,
		Symbol:     in.Symbol,
		Product:    in.Product,
		Side:       in.Side,
		Type:       in.Type,
		Quantity:   in.Quantity,
		Price:      in.Price,
		Leverage:   in.Leverage,
		StopLoss:   in.StopLoss,
		TakeProfit: in.TakeProfit,
		Status:     database.OrderOpen,
		Source:     in.Source,
	}

	if in.Type == database.OrderLimit {
		if err := checkStopLevels(in.Side, in.Price, in.StopLoss, in.TakeProfit); err != nil {
			return nil, err
		}
		marks, err := s.riskMarks(ctx, accountID)
		if err != nil {
			return nil, err
		}
		var blocked bool
		err = s.store.Tx(ctx, func(tx *database.Store) error {
			if err := tx.LockAccount(ctx, accountID); err != nil {
				return err
			}
			account, err := tx.AccountByID(ctx, accountID)
			if err != nil {
				return err
			}
			if !account.Active() {
				return ErrAccountDisabled
			}
			if account.MaxDDStoppedAt != nil {
				return ErrMaxDDStopped
			}
			if account.MaxDD != nil {
				stopped, err := s.checkMaxDD(ctx, tx, accountID, marks)
				if err != nil {
					return err
				}
				if stopped {
					// 門檻已達：checkMaxDD 已在本交易寫停止標記、全戶撤單與
					// max_dd_triggered 並一起提交；callback 回 nil 保存狀態，
					// 業務拒絕在 commit 後回傳（規格 §5／C3）。
					blocked = true
					return nil
				}
			}
			if err := tx.CreateOrder(ctx, order); err != nil {
				return err
			}
			return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
				AccountID: order.AccountID, Event: database.LedgerOrderPlaced, OrderID: order.ID,
				Quantity: order.Quantity, Price: order.Price, Source: order.Source,
			})
		})
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, ErrMaxDDStopped
		}
		return order, nil
	}

	price, err := s.prices.GetPrice(ctx, in.Market, in.Symbol)
	if err != nil {
		return nil, err
	}
	if err := checkStopLevels(in.Side, price.Price, in.StopLoss, in.TakeProfit); err != nil {
		return nil, err
	}
	return s.execute(ctx, order, price.Price, database.RoleTaker)
}

// ClosePosition 以現價平倉。quantity 省略或為 0 時全平；source 是發起平倉的 Run。
// 這裡的檢查只是為了在取價前就擋掉明顯錯誤；真正的判定在 closeAt 的交易內重做。
func (s *Service) ClosePosition(ctx context.Context, accountID, positionID string, quantity float64, source database.Source) (*database.Order, error) {
	// 已風控停止的帳號一律 409（規格 §4），即使部位已被系統強平而不再存在；
	// closeAt 交易內的重新讀取仍會再檢查一次（權威檢查）。
	account, err := s.store.AccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account.MaxDDStoppedAt != nil {
		return nil, ErrMaxDDStopped
	}
	pos, err := s.store.PositionByID(ctx, accountID, positionID)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return nil, ErrPositionClosed
		}
		return nil, err
	}
	if quantity > pos.Quantity+qtyEpsilon {
		return nil, ErrCloseTooMuch
	}

	price, err := s.prices.GetPrice(ctx, pos.Market, pos.Symbol)
	if err != nil {
		return nil, err
	}
	return s.closeAt(ctx, accountID, positionID, quantity, price.Price, source, "")
}

// closeAt 以指定價格平掉指定 id 的部位，供手動平倉、停損停利觸發與 max_dd
// 系統全平共用；quantity 為 0 表示全平。
// 部位在交易內鎖定後重新讀取：id 對不上（已平倉或已重開成新部位）就失敗，不會平到別的部位。
// trigger 為 SL/TP 時是撮合引擎觸發：只有持久化的 stop 現在仍會在此價位觸發同一種 stop
// 才平倉，source 以持久化部位為準（設定該 stop 的 Run），引擎的快照不算數；
// trigger 為 max_dd 時是系統強平：只在重新讀到停止標記非 null 且部位仍存在時
// 建全量 reduce-only order，Source 為空（規格 §5.6）；
// 手動平倉的 source 是呼叫的 Run。已風控停止的帳號擋下手動／SL/TP 平倉，系統強平本身除外。
func (s *Service) closeAt(ctx context.Context, accountID, positionID string, quantity, price float64, source database.Source, trigger string) (*database.Order, error) {
	orderID, err := id.New("ord_")
	if err != nil {
		return nil, err
	}
	var order *database.Order
	err = s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		account, err := tx.AccountByID(ctx, accountID)
		if err != nil {
			return err
		}
		systemMaxDD := trigger == TriggerMaxDD
		if !systemMaxDD && account.MaxDDStoppedAt != nil {
			return ErrMaxDDStopped
		}
		pos, err := tx.PositionByID(ctx, accountID, positionID)
		if err != nil {
			if errors.Is(err, database.ErrNotFound) {
				return ErrPositionClosed
			}
			return err
		}
		switch {
		case systemMaxDD:
			// 系統強平只可能在停止標記落盤後執行（重新讀取確認）；Source 保持空。
			if account.MaxDDStoppedAt == nil {
				return ErrMaxDDInconsistent
			}
		case trigger != "":
			reason, hit := stopTriggered(pos, price)
			if !hit || reason != trigger {
				return ErrStopInactive
			}
			source = pos.StopLossSource
			if trigger == TriggerTakeProfit {
				source = pos.TakeProfitSource
			}
		}
		if quantity <= 0 {
			quantity = pos.Quantity
		}
		if quantity > pos.Quantity+qtyEpsilon {
			return ErrCloseTooMuch
		}
		side := database.SideSell
		if pos.Side == database.Short {
			side = database.SideBuy
		}
		order = &database.Order{
			ID:         orderID,
			AccountID:  pos.AccountID,
			Market:     pos.Market,
			Symbol:     pos.Symbol,
			Product:    pos.Product,
			Side:       side,
			Type:       database.OrderMarket,
			Quantity:   quantity,
			Leverage:   pos.Leverage,
			ReduceOnly: true,
			Status:     database.OrderOpen,
			Source:     source,
			Trigger:    trigger,
		}
		if err := s.settle(ctx, tx, order, price, database.RoleTaker); err != nil {
			return err
		}
		return tx.CreateOrder(ctx, order)
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// CancelOrder 取消掛單。訂單保留建立它的來源；撤單者記在 Ledger。
func (s *Service) CancelOrder(ctx context.Context, accountID, orderID string, source database.Source) (*database.Order, error) {
	var order *database.Order
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		var err error
		order, err = tx.OrderByID(ctx, accountID, orderID)
		if err != nil {
			return err
		}
		if order.Status != database.OrderOpen {
			return fmt.Errorf("%w：目前狀態為 %s", ErrOrderNotOpen, order.Status)
		}
		order.Status = database.OrderCanceled
		if err := tx.SaveOrder(ctx, order); err != nil {
			return err
		}
		return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
			AccountID: order.AccountID, Event: database.LedgerOrderCanceled, OrderID: order.ID,
			Quantity: order.Quantity, Price: order.Price, Source: source,
		})
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// execute 建立並結算一張新的市價單。
func (s *Service) execute(ctx context.Context, order *database.Order, price float64, role string) (*database.Order, error) {
	marks, err := s.riskMarks(ctx, order.AccountID)
	if err != nil {
		return nil, err
	}
	var blocked bool
	err = s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, order.AccountID); err != nil {
			return err
		}
		account, err := tx.AccountByID(ctx, order.AccountID)
		if err != nil {
			return err
		}
		if account.MaxDDStoppedAt != nil {
			return ErrMaxDDStopped
		}
		if account.MaxDD != nil {
			stopped, err := s.checkMaxDD(ctx, tx, order.AccountID, marks)
			if err != nil {
				return err
			}
			if stopped {
				// 門檻已達：停止標記與撤單已在本交易提交（callback 回 nil
				// 保存狀態），業務拒絕在 commit 後回傳（規格 §5／C3）。
				blocked = true
				return nil
			}
		}
		if err := s.settle(ctx, tx, order, price, role); err != nil {
			return err
		}
		if err := tx.CreateOrder(ctx, order); err != nil {
			return err
		}
		if account.MaxDD != nil {
			// risk postcheck（規格 §5.5／C4）：成交與手續費可能已越線，鎖內以
			// 最新 Balance／部位重算；越線時在同一交易提交停止標記、全戶撤單
			// 與 max_dd_triggered。本單已保存成 filled，撤單不會誤撤它；成交
			// 結果保留，Engine 在本輪末清理完成清倉。
			postMarks := make(map[string]float64, len(marks)+1)
			for k, v := range marks {
				postMarks[k] = v
			}
			postMarks[key(order.Market, order.Symbol)] = price
			if _, err := s.checkMaxDD(ctx, tx, order.AccountID, postMarks); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, ErrMaxDDStopped
	}
	return order, nil
}

// riskMarks 在 transaction 外取帳號全部持倉標的的現價，供鎖內的 max_dd
// risk precheck 使用（規格 §5：取價必須在 transaction 外）。max_dd 未設定的
// 帳號維持原有快速路徑：回傳 nil，完全不多取任何持倉報價。
func (s *Service) riskMarks(ctx context.Context, accountID string) (map[string]float64, error) {
	account, err := s.store.AccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account.MaxDD == nil {
		return nil, nil
	}
	positions, err := s.store.ListPositions(ctx, accountID, "")
	if err != nil {
		return nil, err
	}
	marks := make(map[string]float64, len(positions))
	for i := range positions {
		k := key(positions[i].Market, positions[i].Symbol)
		if _, ok := marks[k]; ok {
			continue
		}
		price, err := s.prices.GetPrice(ctx, positions[i].Market, positions[i].Symbol)
		if err != nil {
			return nil, err
		}
		marks[k] = price.Price
	}
	return marks, nil
}

// fillOpenOrder 結算一張已存在的掛單。訂單在載入後若已不是 open（被取消或
// 上一輪已成交）就跳過。業務規則擋下時把訂單標成 rejected 並記原因，
// 避免每一輪都重試同一張注定失敗的單。marks 是 transaction 外取好的持倉
// 現價，供鎖內的 max_dd risk precheck 使用。
func (s *Service) fillOpenOrder(ctx context.Context, accountID, orderID string, price float64, marks map[string]float64) (*database.Order, error) {
	var result *database.Order
	err := s.store.Tx(ctx, func(tx *database.Store) error {
		if err := tx.LockAccount(ctx, accountID); err != nil {
			return err
		}
		order, err := tx.OrderByID(ctx, accountID, orderID)
		if err != nil {
			return err
		}
		if order.Status != database.OrderOpen {
			return nil
		}
		account, err := tx.AccountByID(ctx, accountID)
		if err != nil {
			return err
		}
		if account.MaxDDStoppedAt != nil {
			// 風控停止應已撤掉這筆掛單；stale 快照仍帶進來時補撤，不能算成交。
			order.Status = database.OrderCanceled
			if err := tx.SaveOrder(ctx, order); err != nil {
				return err
			}
			return tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
				AccountID: order.AccountID, Event: database.LedgerOrderCanceled, OrderID: order.ID,
				Quantity: order.Quantity, Price: order.Price,
			})
		}
		if account.MaxDD != nil {
			// risk precheck（規格 §5）：掛單快照載入後門檻已達時，checkMaxDD
			// 在同一交易寫停止標記並撤掉全部掛單（含這張）；這張不能走結算，
			// 以 canceled 回傳，Engine 不得算成 Filled。
			stopped, err := s.checkMaxDD(ctx, tx, accountID, marks)
			if err != nil {
				return err
			}
			if stopped {
				order.Status = database.OrderCanceled
				result = order
				return nil
			}
		}

		rejected := false
		if err := s.settle(ctx, tx, order, price, database.RoleMaker); err != nil {
			if !isBusinessRejection(err) {
				return err
			}
			rejected = true
			order.Status = database.OrderRejected
			order.RejectReason = err.Error()
			if err := tx.CreateLedgerEntry(ctx, &database.LedgerEntry{
				AccountID: order.AccountID, Event: database.LedgerOrderRejected, OrderID: order.ID,
				Quantity: order.Quantity, Price: order.Price, Source: order.Source,
			}); err != nil {
				return err
			}
		}
		result = order
		// 先把訂單存檔（成交成功時 status 已是 filled），再做 risk postcheck：
		// 越線時 checkMaxDD 的全戶撤單不會誤撤這張已不 open 的單（規格 §5.5／C4）。
		if err := tx.SaveOrder(ctx, order); err != nil {
			return err
		}
		if !rejected && account.MaxDD != nil {
			// risk postcheck（規格 §5.5／C4）：成交與手續費可能已越線，鎖內以
			// 最新 Balance／部位重算；越線時在同一交易提交停止標記、全戶撤單
			// 與 max_dd_triggered。成交結果保留，Engine 在本輪末清理完成清倉。
			if _, err := s.checkMaxDD(ctx, tx, accountID, marks); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}
