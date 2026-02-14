package utils

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Price struct {
	// 用 atomic 存 float64（避免 WS goroutine 與 Get() data race）
	valueBits atomic.Uint64

	LastUsed   time.Time
	ExpireTime time.Duration

	stop       chan struct{}
	connecting bool
	conn       *websocket.Conn

	mu sync.Mutex
}

func (p *Price) StartWs(symbol string) error {
	if p.stop != nil {
		return nil
	}

	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return errors.New("symbol is empty")
	}

	if !strings.HasSuffix(symbol, "USDT") {
		symbol = symbol + "USDT"
	}

	wsURL := fmt.Sprintf("wss://stream.binance.com:9443/ws/%s@miniTicker", strings.ToLower(symbol))

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return err
	}

	p.conn = conn
	p.stop = make(chan struct{})
	p.connecting = true
	go func() {
		defer func() { _ = conn.Close() }()

		type miniTicker struct {
			Close string `json:"c"` // last price
		}

		for {
			select {
			case <-p.stop:
				return
			default:
			}

			if time.Since(p.LastUsed) > p.ExpireTime {
				// 過期
				return
			}

			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var ev miniTicker
			if err := json.Unmarshal(msg, &ev); err != nil {
				continue
			}

			usd, err := strconv.ParseFloat(ev.Close, 64)
			if err != nil {
				continue
			}

			p.valueBits.Store(math.Float64bits(usd))
			//p.onUpdate()
		}
		p.connecting = false
	}()

	return nil
}

func (p *Price) StopWs() error {
	// 假設：不會並發 StopWs / StartWs
	if p.stop == nil {
		return nil
	}

	close(p.stop)
	p.stop = nil

	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}

	return nil
}

func (p *Price) Get() float64 {
	p.mu.Lock()
	p.LastUsed = time.Now()
	p.mu.Unlock()
	return math.Float64frombits(p.valueBits.Load())
}

type PriceCache struct {
	mu      sync.RWMutex
	symbols map[string]*Price
}

func NewPriceCache() *PriceCache {
	return &PriceCache{
		symbols: make(map[string]*Price),
	}
}

func (pc *PriceCache) Get(symbol string) (float64, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))

	pc.mu.RLock()
	p, exist := pc.symbols[symbol]
	pc.mu.RUnlock()

	if !exist {
		np, err := pc.addCache(symbol)
		if err != nil {
			return 0, err
		}
		return np.Get(), nil
	}

	if time.Since(p.LastUsed) > p.ExpireTime {
		err := pc.restartWs(p, symbol)
		if err != nil {
			return 0, err
		}
	}

	return p.Get(), nil
}

func (pc *PriceCache) restartWs(p *Price, symbol string) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	err := p.StartWs(symbol)
	if err != nil {
		return err
	}
	return nil
}

func (pc *PriceCache) addCache(symbol string) (p *Price, err error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	p, exist := pc.symbols[symbol] // 防重入
	if exist {
		return p, nil
	}

	p = &Price{
		ExpireTime: 5 * time.Minute,
		LastUsed:   time.Now(),
	}

	if err := p.StartWs(symbol); err != nil {
		return nil, err
	}

	pc.symbols[symbol] = p
	return p, nil
}
