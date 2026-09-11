package trading

import (
	"context"

	"backend/internal/database"
)

type Summary struct {
	Balance      float64
	LockedMargin float64
	Available    float64
	Unrealized   float64
	Equity       float64
}

// OpenPosition 是部位加上依現價算出的浮動損益。
type OpenPosition struct {
	database.Position
	MarkPrice  float64
	Unrealized float64
}

// Summary 需要現價才能算出權益，因此帳號有持倉時會依賴行情來源。
// 沒有持倉的帳號完全不碰行情。
func (s *Service) Summary(ctx context.Context, accountID string) (Summary, error) {
	account, err := s.store.AccountByID(ctx, accountID)
	if err != nil {
		return Summary{}, err
	}
	positions, err := s.Positions(ctx, accountID, "")
	if err != nil {
		return Summary{}, err
	}

	locked, floating := 0.0, 0.0
	for _, p := range positions {
		locked += p.Margin
		floating += p.Unrealized
	}

	return Summary{
		Balance:      account.Balance,
		LockedMargin: locked,
		Available:    account.Balance - locked,
		Unrealized:   floating,
		Equity:       account.Balance + floating,
	}, nil
}

func (s *Service) Positions(ctx context.Context, accountID, product string) ([]OpenPosition, error) {
	positions, err := s.store.ListPositions(ctx, accountID, product)
	if err != nil {
		return nil, err
	}

	views := make([]OpenPosition, 0, len(positions))
	for i := range positions {
		price, err := s.prices.GetPrice(ctx, positions[i].Market, positions[i].Symbol)
		if err != nil {
			return nil, err
		}
		views = append(views, OpenPosition{
			Position:   positions[i],
			MarkPrice:  price.Price,
			Unrealized: Unrealized(&positions[i], price.Price),
		})
	}
	return views, nil
}

func (s *Service) Orders(ctx context.Context, accountID, status string, limit int) ([]database.Order, error) {
	return s.store.ListOrders(ctx, accountID, status, limit)
}

func (s *Service) Order(ctx context.Context, accountID, orderID string) (*database.Order, error) {
	return s.store.OrderByID(ctx, accountID, orderID)
}

func (s *Service) Trades(ctx context.Context, accountID string, limit int) ([]database.Trade, error) {
	return s.store.ListTrades(ctx, accountID, limit)
}
