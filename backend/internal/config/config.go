package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 是整個 backend 的執行期設定，全部來自環境變數或 .env。
type Config struct {
	UserToken            string // 僅明確啟用時作為非 Release 靜態 USER token
	AllowStaticUserToken bool
	AgentServiceToken    string
	OIDCIssuer           string
	OIDCAudience         string
	OIDCUserSubject      string
	OIDCJWKSURL          string
	OIDCTokenType        string
	// AccountTokenKey encrypts Account tokens at rest. It is mandatory in Test/Release.
	AccountTokenKey []byte
	UserName        string // USER 在留言板上的公開名稱
	Port            string

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

	MatchingInterval time.Duration // 撮合引擎掃描間隔
}

// Load 讀取 .env（若存在）後組出設定。真實環境變數優先於 .env。
func Load(envPath string) (*Config, error) {
	if err := loadEnvFile(envPath); err != nil {
		return nil, err
	}
	allowStaticUserToken, err := envBoolStrict("ALLOW_STATIC_USER_TOKEN", false)
	if err != nil {
		return nil, err
	}
	if os.Getenv("STOCK_AI_ENVIRONMENT") == "Release" && allowStaticUserToken {
		return nil, errors.New("ALLOW_STATIC_USER_TOKEN is forbidden in Release")
	}
	var userToken string
	if allowStaticUserToken {
		userToken, err = Secret("USER_TOKEN", "backend_user_token")
		if err != nil {
			return nil, err
		}
	}
	var agentServiceToken string
	if os.Getenv("AGENT_SERVICE_TOKEN") != "" || os.Getenv("AGENT_SERVICE_TOKEN_FILE") != "" {
		// Reuse the existing private Agent/Backend secret file without enabling static USER login.
		agentServiceToken, err = Secret("AGENT_SERVICE_TOKEN", "backend_user_token")
		if err != nil {
			return nil, err
		}
	}
	accountTokenKey, err := Secret("ACCOUNT_TOKEN_KEY", "backend_account_token_key")
	if err != nil {
		return nil, err
	}
	var decodedAccountTokenKey []byte
	if accountTokenKey != "" {
		decodedAccountTokenKey, err = base64.StdEncoding.DecodeString(accountTokenKey)
		if err != nil || len(decodedAccountTokenKey) != 32 || base64.StdEncoding.EncodeToString(decodedAccountTokenKey) != accountTokenKey {
			return nil, errors.New("ACCOUNT_TOKEN_KEY must be canonical base64 encoding of exactly 32 bytes")
		}
	}

	cfg := &Config{
		UserToken:            userToken,
		AgentServiceToken:    agentServiceToken,
		AllowStaticUserToken: allowStaticUserToken,
		OIDCIssuer:           strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		OIDCAudience:         strings.TrimSpace(os.Getenv("OIDC_AUDIENCE")),
		OIDCUserSubject:      strings.TrimSpace(os.Getenv("OIDC_USER_SUBJECT")),
		OIDCJWKSURL:          strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
		OIDCTokenType:        envString("OIDC_TOKEN_TYPE", "at+jwt"),
		AccountTokenKey:      decodedAccountTokenKey,
		UserName:             envString("USER_NAME", "USER"),
		Port:                 envString("PORT", "7794"),
		DBDriver:             envString("DB_DRIVER", "sqlite"),
		DBDSN:                envString("DB_DSN", "backend.db"),
		LogDir:               envString("LOG_DIR", "logs"),
		LogMaxLines:          envInt("LOG_MAX_LINES", 100000),
		Debug:                envBool("DEBUG", false),
	}

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
	if cfg.MatchingInterval, err = envDuration("MATCHING_INTERVAL", time.Second); err != nil {
		return nil, err
	}

	return cfg, cfg.validate()
}

// Secret reads a development environment value or a namespace-scoped secret
// file in Test/Release. Error messages never contain secret material.
func Secret(name, logicalName string) (string, error) {
	environment := os.Getenv("STOCK_AI_ENVIRONMENT")
	direct, directSet := os.LookupEnv(name)
	fileName, fileSet := os.LookupEnv(name + "_FILE")
	if directSet && fileSet {
		return "", fmt.Errorf("%s and %s_FILE cannot both be set", name, name)
	}
	production := environment == "Test" || environment == "Release"
	if environment != "" && !production {
		return "", errors.New("STOCK_AI_ENVIRONMENT must be exactly Test or Release")
	}
	if production && !fileSet {
		return "", fmt.Errorf("%s_FILE is required in %s", name, environment)
	}
	if fileSet {
		namespace := "stock-ai-" + strings.ToLower(environment)
		if production && filepath.Base(fileName) != namespace+"_"+logicalName {
			return "", fmt.Errorf("%s_FILE is outside the %s secret namespace", name, namespace)
		}
		info, err := os.Lstat(fileName)
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s_FILE must name a readable regular file", name)
		}
		raw, err := os.ReadFile(fileName)
		if err != nil {
			return "", fmt.Errorf("%s_FILE is unreadable", name)
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return "", fmt.Errorf("%s_FILE is empty", name)
		}
		if production {
			if err := validateProductionSecret(value, strings.HasSuffix(logicalName, "_key")); err != nil {
				return "", fmt.Errorf("%s_FILE is invalid: %w", name, err)
			}
		}
		return value, nil
	}
	return direct, nil
}

func validateProductionSecret(value string, key bool) error {
	lower := strings.ToLower(value)
	for _, marker := range []string{"change-me", "changeme", "placeholder", "example", "fixture", "password", "dev-secret", "secret-token"} {
		if strings.Contains(lower, marker) {
			return errors.New("known development or placeholder value")
		}
	}
	if len(value) < 32 {
		return errors.New("fewer than 32 bytes")
	}
	characters := make(map[rune]struct{})
	for _, character := range value {
		characters[character] = struct{}{}
	}
	if len(characters) < 16 {
		return errors.New("insufficient character diversity")
	}
	if key {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 || base64.StdEncoding.EncodeToString(decoded) != value {
			return errors.New("must be canonical base64 encoding of exactly 32 bytes")
		}
		bytes := make(map[byte]struct{})
		for _, item := range decoded {
			bytes[item] = struct{}{}
		}
		if len(bytes) < 16 || string(decoded[:16]) == string(decoded[16:]) {
			return errors.New("decoded key has insufficient randomness")
		}
	}
	return nil
}

func (c *Config) validate() error {
	environment := os.Getenv("STOCK_AI_ENVIRONMENT")
	if environment == "Release" && c.AllowStaticUserToken {
		return errors.New("ALLOW_STATIC_USER_TOKEN is forbidden in Release")
	}
	if c.AllowStaticUserToken && c.UserToken == "" {
		return errors.New("USER_TOKEN is required when ALLOW_STATIC_USER_TOKEN is enabled")
	}
	oidcValues := []string{c.OIDCIssuer, c.OIDCAudience, c.OIDCUserSubject, c.OIDCJWKSURL}
	oidcCount := 0
	for _, value := range oidcValues {
		if value != "" {
			oidcCount++
		}
	}
	if oidcCount != 0 && oidcCount != len(oidcValues) {
		return errors.New("OIDC_ISSUER, OIDC_AUDIENCE, OIDC_USER_SUBJECT, OIDC_JWKS_URL, and OIDC_TOKEN_TYPE must be configured together")
	}
	if oidcCount == 0 && !c.AllowStaticUserToken {
		return errors.New("OIDC configuration is required when static USER authentication is disabled")
	}
	if oidcCount == len(oidcValues) {
		issuer, issuerErr := url.Parse(c.OIDCIssuer)
		jwks, jwksErr := url.Parse(c.OIDCJWKSURL)
		if issuerErr != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" {
			return errors.New("OIDC_ISSUER must be an exact HTTPS issuer URL")
		}
		if jwksErr != nil || jwks.Scheme != "https" || jwks.Host == "" || jwks.User != nil || jwks.Fragment != "" {
			return errors.New("OIDC_JWKS_URL must be an HTTPS URL")
		}
		if strings.TrimSpace(c.OIDCTokenType) == "" {
			return errors.New("OIDC_TOKEN_TYPE must not be empty")
		}
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
	if c.MatchingInterval <= 0 {
		return errors.New("MATCHING_INTERVAL 必須大於 0")
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

func envBoolStrict(key string, fallback bool) (bool, error) {
	raw, exists := os.LookupEnv(key)
	if !exists || raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value, nil
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
