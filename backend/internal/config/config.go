package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是整個 backend 的執行期設定，全部來自環境變數或 .env。
type Config struct {
	UserToken string // USER 的固定 token
	UserName  string // USER 在留言板上的公開名稱
	Port      string

	DBDriver string // sqlite | postgres
	DBDSN    string

	LogDir      string
	LogMaxLines int
	Debug       bool

	FeeRateMaker float64 // 掛單成交費率
	FeeRateTaker float64 // 市價成交費率

	// 行情參數。規格 §8、§11 標示為「暫定」，因此開放設定。
	MarketFreshTTL    time.Duration // 快取多久內算新鮮
	MarketIdleTimeout time.Duration // 多久沒人要價就停掉訂閱
	MarketWaitTimeout time.Duration // 等新報價的上限
}

// Load 讀取 .env（若存在）後組出設定。真實環境變數優先於 .env。
func Load(envPath string) (*Config, error) {
	if err := loadEnvFile(envPath); err != nil {
		return nil, err
	}

	cfg := &Config{
		UserToken:   os.Getenv("USER_TOKEN"),
		UserName:    envString("USER_NAME", "USER"),
		Port:        envString("PORT", "7794"),
		DBDriver:    envString("DB_DRIVER", "sqlite"),
		DBDSN:       envString("DB_DSN", "backend.db"),
		LogDir:      envString("LOG_DIR", "logs"),
		LogMaxLines: envInt("LOG_MAX_LINES", 100000),
		Debug:       envBool("DEBUG", false),
	}

	var err error
	if cfg.FeeRateMaker, err = envFloat("FEE_RATE_MAKER", 0.0002); err != nil {
		return nil, err
	}
	if cfg.FeeRateTaker, err = envFloat("FEE_RATE_TAKER", 0.0004); err != nil {
		return nil, err
	}
	if cfg.MarketFreshTTL, err = envDuration("MARKET_FRESH_TTL", 1500*time.Millisecond); err != nil {
		return nil, err
	}
	if cfg.MarketIdleTimeout, err = envDuration("MARKET_IDLE_TIMEOUT", 60*time.Second); err != nil {
		return nil, err
	}
	if cfg.MarketWaitTimeout, err = envDuration("MARKET_WAIT_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}

	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.UserToken == "" {
		return errors.New("USER_TOKEN 未設定：這是 USER 身分的唯一憑證，必須在 .env 指定")
	}
	if c.DBDriver != "sqlite" && c.DBDriver != "postgres" {
		return fmt.Errorf("DB_DRIVER 只支援 sqlite 或 postgres，收到 %q", c.DBDriver)
	}
	if c.FeeRateMaker < 0 || c.FeeRateTaker < 0 {
		return errors.New("手續費率不可為負數")
	}
	if c.MarketFreshTTL <= 0 || c.MarketIdleTimeout <= 0 || c.MarketWaitTimeout <= 0 {
		return errors.New("行情的時間參數必須大於 0")
	}
	return nil
}

// loadEnvFile 讀取 KEY=VALUE 格式的檔案。檔案不存在不算錯誤，
// 已存在的環境變數不會被覆蓋。
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("讀取 %q 失敗: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("%s 第 %d 行格式錯誤，需要 KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, unquote(strings.TrimSpace(value))); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

// envDuration 讀取時間長度，接受 Go 的寫法（例如 1.5s、60s、200ms）。
// 寫錯就報錯，不要靜默套用預設值——那會讓人以為設定生效了。
func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 必須是時間長度（例如 1.5s、60s），收到 %q", key, raw)
	}
	return v, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s 必須是數字，收到 %q", key, raw)
	}
	return v, nil
}
