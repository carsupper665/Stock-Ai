package gateway

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	Environment, ListenAddress, Issuer, AuthorizationURL, TokenURL, JWKSURL string
	ClientID, ClientSecret, RedirectURI, EndSessionURL, PostLogoutURI       string
	CookieName, UserSubject                                                 string
	TokenAuthMethod, AccessTokenType, Audience                              string
	CookieKey                                                               []byte
	SessionTTL, LoginTTL                                                    time.Duration
}

func LoadConfig() (Config, error) {
	environment := os.Getenv("STOCK_AI_ENVIRONMENT")
	if environment != "Test" && environment != "Release" {
		return Config{}, errors.New("STOCK_AI_ENVIRONMENT must be exactly Test or Release")
	}
	secret, err := readSecret("OIDC_CLIENT_SECRET", "oidc_client_secret", environment, false)
	if err != nil {
		return Config{}, err
	}
	keyText, err := readSecret("OIDC_COOKIE_KEY", "oidc_cookie_key", environment, true)
	if err != nil {
		return Config{}, err
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != keyText {
		return Config{}, errors.New("OIDC_COOKIE_KEY_FILE must contain canonical base64 for exactly 32 bytes")
	}
	c := Config{
		Environment: environment, ListenAddress: value("OIDC_GATEWAY_ADDR", ":4180"),
		Issuer:           strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		AuthorizationURL: strings.TrimSpace(os.Getenv("OIDC_AUTHORIZATION_URL")),
		TokenURL:         strings.TrimSpace(os.Getenv("OIDC_TOKEN_URL")), JWKSURL: strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
		ClientID: strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID")), ClientSecret: secret,
		RedirectURI: strings.TrimSpace(os.Getenv("OIDC_REDIRECT_URI")), EndSessionURL: strings.TrimSpace(os.Getenv("OIDC_END_SESSION_URL")),
		PostLogoutURI: strings.TrimSpace(os.Getenv("OIDC_POST_LOGOUT_URI")), CookieName: strings.TrimSpace(os.Getenv("OIDC_COOKIE_NAME")),
		CookieKey: key, UserSubject: strings.TrimSpace(os.Getenv("OIDC_USER_SUBJECT")), SessionTTL: 8 * time.Hour, LoginTTL: 10 * time.Minute,
		TokenAuthMethod: value("OIDC_TOKEN_AUTH_METHOD", "client_secret_basic"), AccessTokenType: value("OIDC_TOKEN_TYPE", "at+jwt"),
		Audience: strings.TrimSpace(os.Getenv("OIDC_AUDIENCE")),
	}
	return c, c.Validate()
}

func value(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func readSecret(name, logical, environment string, key bool) (string, error) {
	if _, ok := os.LookupEnv(name); ok {
		return "", fmt.Errorf("%s is forbidden in %s; use %s_FILE", name, environment, name)
	}
	path := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if path == "" {
		return "", fmt.Errorf("%s_FILE is required", name)
	}
	expected := "stock-ai-" + strings.ToLower(environment) + "_" + logical
	if filepath.Base(path) != expected {
		return "", fmt.Errorf("%s_FILE is outside the environment secret namespace", name)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s_FILE must be a readable regular file", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s_FILE is unreadable", name)
	}
	v := strings.TrimSpace(string(raw))
	if len(v) < 32 {
		return "", fmt.Errorf("%s_FILE has fewer than 32 bytes", name)
	}
	if !key && strings.Contains(strings.ToLower(v), "placeholder") {
		return "", fmt.Errorf("%s_FILE contains a placeholder", name)
	}
	return v, nil
}

func (c Config) Validate() error {
	expectedCookie := "__Host-stock-ai-" + strings.ToLower(c.Environment) + "-session"
	if c.ClientID == "" || strings.IndexFunc(c.ClientID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("OIDC_CLIENT_ID must be a non-empty identifier without whitespace")
	}
	if c.CookieName != expectedCookie {
		return fmt.Errorf("OIDC_COOKIE_NAME must be %q in %s", expectedCookie, c.Environment)
	}
	if c.ClientSecret == "" || len(c.CookieKey) != 32 || c.UserSubject == "" {
		return errors.New("OIDC secrets and OIDC_USER_SUBJECT are required")
	}
	if c.TokenAuthMethod != "client_secret_basic" && c.TokenAuthMethod != "client_secret_post" {
		return errors.New("OIDC_TOKEN_AUTH_METHOD must be client_secret_basic or client_secret_post")
	}
	if c.AccessTokenType == "" || c.Audience == "" {
		return errors.New("OIDC_TOKEN_TYPE and OIDC_AUDIENCE are required")
	}
	parsed := make(map[string]*url.URL)
	for name, raw := range map[string]string{"OIDC_ISSUER": c.Issuer, "OIDC_AUTHORIZATION_URL": c.AuthorizationURL, "OIDC_TOKEN_URL": c.TokenURL, "OIDC_JWKS_URL": c.JWKSURL, "OIDC_REDIRECT_URI": c.RedirectURI, "OIDC_POST_LOGOUT_URI": c.PostLogoutURI} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "*") || localHostname(u.Hostname()) {
			return fmt.Errorf("%s must be an exact non-local HTTPS URL", name)
		}
		parsed[name] = u
	}
	issuer := parsed["OIDC_ISSUER"]
	if issuer.RawQuery != "" {
		return errors.New("OIDC_ISSUER must not contain a query")
	}
	for _, name := range []string{"OIDC_AUTHORIZATION_URL", "OIDC_TOKEN_URL", "OIDC_JWKS_URL"} {
		u := parsed[name]
		if u.Scheme != issuer.Scheme || u.Host != issuer.Host {
			return fmt.Errorf("%s must use the issuer origin", name)
		}
	}
	redirect := parsed["OIDC_REDIRECT_URI"]
	if redirect.Path != "/oauth2/callback" || redirect.RawQuery != "" {
		return errors.New("OIDC_REDIRECT_URI must have exact path /oauth2/callback and no query")
	}
	logout := parsed["OIDC_POST_LOGOUT_URI"]
	if logout.Path != "/" || logout.RawQuery != "" {
		return errors.New("OIDC_POST_LOGOUT_URI must have exact path / and no query")
	}
	if redirect.Scheme != logout.Scheme || redirect.Host != logout.Host {
		return errors.New("OIDC redirect and post-logout URIs must use the same environment host")
	}
	if c.EndSessionURL != "" {
		u, err := url.Parse(c.EndSessionURL)
		if err != nil || u.Scheme != issuer.Scheme || u.Host != issuer.Host || u.User != nil || u.Fragment != "" {
			return errors.New("OIDC_END_SESSION_URL must use the issuer origin")
		}
	}
	if c.SessionTTL <= 0 || c.SessionTTL > 24*time.Hour || c.LoginTTL <= 0 || c.LoginTTL > 15*time.Minute {
		return errors.New("gateway TTL is outside the safe range")
	}
	return nil
}

func localHostname(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
