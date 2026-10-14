package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	issuer      = "https://idp.localtest:9443/realms/stock-ai-test"
	clientID    = "stock-ai-test"
	clientKey   = "local-test-oidc-client-secret-only"
	providerKey = "local-test-provider-token-only"
	keyID       = "local-test-signing-key"
)

type authorization struct {
	redirectURI string
	challenge   string
	nonce       string
	scenario    string
}

type fixture struct {
	config        fixtureConfig
	mu            sync.Mutex
	key           *rsa.PrivateKey
	codes         map[string]authorization
	scenarioNext  string
	outage        bool
	providerCalls int
	marketCalls   map[string]int
	adapterCalls  map[string]int
	backendClient *http.Client
}

// The auxiliary fixture is configured at runtime; product images are never rebuilt.
type fixtureConfig struct {
	Issuer             string `json:"issuer"`
	ClientID           string `json:"client_id"`
	ClientSecret       string `json:"-"`
	ClientSecretFile   string `json:"client_secret_file"`
	ProviderSecret     string `json:"-"`
	ProviderSecretFile string `json:"provider_secret_file"`
	Audience           string `json:"audience"`
	Subject            string `json:"subject"`
	RedirectURI        string `json:"redirect_uri"`
	LogoutURI          string `json:"logout_uri"`
	KeyID              string `json:"key_id"`
	MarketFixture      bool   `json:"market_fixture"`
	BackendAdapter     bool   `json:"backend_adapter"`
	BackendSecretFile  string `json:"backend_secret_file,omitempty"`
	BackendSecret      string `json:"-"`
}

func (f *fixture) identity() fixtureConfig {
	if f.config.Issuer != "" {
		return f.config
	}
	return fixtureConfig{Issuer: issuer, ClientID: clientID, ClientSecret: clientKey,
		ProviderSecret: providerKey, Audience: "stock-ai-api-test", Subject: "local-test-user",
		RedirectURI: "https://app.localtest/oauth2/callback", LogoutURI: "https://app.localtest/", KeyID: keyID}
}

func readFixtureConfig(path string) (fixtureConfig, error) {
	var c fixtureConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	for _, value := range []string{c.Issuer, c.ClientID, c.Audience, c.Subject, c.RedirectURI, c.LogoutURI, c.KeyID} {
		if value == "" || strings.Contains(strings.ToLower(value), "test") {
			return c, fmt.Errorf("invalid isolated fixture identity")
		}
	}
	for _, value := range []string{c.Issuer, c.RedirectURI, c.LogoutURI} {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("invalid fixture URL")
		}
	}
	for path, target := range map[string]*string{c.ClientSecretFile: &c.ClientSecret, c.ProviderSecretFile: &c.ProviderSecret} {
		secret, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("fixture secret unavailable")
		}
		*target = strings.TrimSpace(string(secret))
		if len(*target) < 32 || strings.Contains(strings.ToLower(*target), "test") {
			return c, fmt.Errorf("invalid fixture secret")
		}
	}
	if c.BackendAdapter {
		secret, err := os.ReadFile(c.BackendSecretFile)
		if err != nil || !c.MarketFixture {
			return c, fmt.Errorf("private Backend adapter configuration invalid")
		}
		c.BackendSecret = strings.TrimSpace(string(secret))
		if len(c.BackendSecret) < 32 || strings.ContainsAny(c.BackendSecret, " \r\n\t") {
			return c, fmt.Errorf("private Backend adapter credential invalid")
		}
	}
	return c, nil
}

func main() {
	var configured fixtureConfig
	listen := ":9443"
	adapterListen := ""
	if path := os.Getenv("FIXTURE_CONFIG"); path != "" {
		var err error
		configured, err = readFixtureConfig(path)
		if err != nil {
			log.Fatal("isolated fixture configuration invalid")
		}
		u, _ := url.Parse(configured.Issuer)
		if u.Port() == "" {
			log.Fatal("fixture issuer requires explicit port")
		}
		listen = ":" + u.Port()
	}
	if configured.BackendAdapter {
		var err error
		adapterListen, err = privateAdapterAddress()
		if err != nil {
			log.Fatal("private Backend adapter network unavailable")
		}
	}
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		addresses := []string{"127.0.0.1" + listen, "127.0.0.1:18080"}
		if configured.MarketFixture {
			addresses = append(addresses, "127.0.0.1:443")
		}
		if configured.BackendAdapter {
			addresses = append(addresses, adapterListen)
		}
		for _, address := range addresses {
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				os.Exit(1)
			}
			_ = conn.Close()
		}
		return
	}
	keyPEM, err := os.ReadFile("/fixture/jwks-signing-key.pem")
	if err != nil {
		log.Fatal("fixture signing key read failed")
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		log.Fatal("fixture signing key parse failed")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		log.Fatal("fixture signing key parse failed")
	}
	key, ok := parsedKey.(*rsa.PrivateKey)
	if !ok {
		log.Fatal("fixture signing key has invalid type")
	}
	f := &fixture{key: key, codes: make(map[string]authorization), config: configured}
	issuerMux := f.issuerHandler()
	providerMux := f.providerHandler()
	if configured.BackendAdapter {
		go func() {
			if err := http.ListenAndServe(adapterListen, f.adapterHandler()); err != nil {
				log.Fatal("private Backend adapter stopped")
			}
		}()
	}
	if configured.MarketFixture {
		go func() {
			if err := http.ListenAndServeTLS(":443", "/fixture/fullchain.pem", "/fixture/privkey.pem", f.marketHandler()); err != nil {
				log.Fatal("local market fixture stopped")
			}
		}()
	}

	go func() {
		log.Print("local fake-provider fixture listening on :18080")
		if err := http.ListenAndServe(":18080", providerMux); err != nil {
			log.Fatal("fake-provider fixture stopped")
		}
	}()
	log.Print("local deterministic OIDC/JWKS fixture listening on :9443")
	if err := http.ListenAndServeTLS(listen, "/fixture/fullchain.pem", "/fixture/privkey.pem", issuerMux); err != nil {
		log.Fatalf("OIDC fixture stopped: %v", err)
	}
}

func privateAdapterAddress() (string, error) {
	// This alias exists only on the application network. Bind that exact own
	// interface, never 0.0.0.0 or the fixture's separately attached edge IP.
	ips, err := net.LookupIP("fake-provider.localrelease")
	if err != nil {
		return "", err
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	matched := make(map[string]bool)
	for _, ip := range ips {
		if ip.To4() == nil || !ip.IsPrivate() {
			continue
		}
		for _, address := range addresses {
			own, _, err := net.ParseCIDR(address.String())
			if err == nil && own.Equal(ip) {
				matched[ip.String()] = true
			}
		}
	}
	if len(matched) != 1 {
		return "", fmt.Errorf("ambiguous private adapter interface")
	}
	for ip := range matched {
		return net.JoinHostPort(ip, "18081"), nil
	}
	return "", fmt.Errorf("private adapter interface missing")
}

func (f *fixture) issuerHandler() http.Handler {
	u, _ := url.Parse(f.identity().Issuer)
	realm := u.Path
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc(realm+"/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc(realm+"/authorize", f.authorize)
	mux.HandleFunc(realm+"/token", f.token)
	mux.HandleFunc(realm+"/jwks", f.jwks)
	mux.HandleFunc(realm+"/logout", f.logout)
	mux.HandleFunc("/__fixture/next", f.nextCase)
	mux.HandleFunc("/__fixture/outage", f.setOutage)
	return mux
}

func (f *fixture) providerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/v1/chat/completions", f.complete)
	mux.HandleFunc("/__fixture/market", f.marketCounters)
	mux.HandleFunc("/__fixture/adapter", f.adapterCounters)
	mux.HandleFunc("/__fixture/backend-rejections", f.backendRejections)
	return mux
}

func (f *fixture) backendRejections(w http.ResponseWriter, r *http.Request) {
	if !f.identity().BackendAdapter || r.Method != "GET" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	statuses := make(map[string]int)
	for _, scenario := range []string{"anonymous", "static-user", "expired", "wrong-issuer", "wrong-audience", "wrong-subject"} {
		token := ""
		if scenario == "static-user" {
			token = f.identity().BackendSecret
		}
		if scenario != "anonymous" && scenario != "static-user" {
			claims := map[string]any{"iss": f.identity().Issuer, "aud": f.identity().Audience, "sub": f.identity().Subject,
				"exp": time.Now().Unix() + 45, "iat": time.Now().Unix()}
			switch scenario {
			case "expired":
				claims["exp"] = time.Now().Unix() - 1
			case "wrong-issuer":
				claims["iss"] = "https://wrong.invalid"
			case "wrong-audience":
				claims["aud"] = "wrong-audience"
			case "wrong-subject":
				claims["sub"] = "wrong-subject"
			}
			var err error
			token, err = f.signToken("at+jwt", claims)
			if err != nil {
				w.WriteHeader(500)
				return
			}
		}
		request, err := http.NewRequestWithContext(r.Context(), "GET", "http://backend:7794/v1/accounts/acc_adapter_negative_probe", nil)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		client := f.backendClient
		if client == nil {
			client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil},
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		}
		response, err := client.Do(request)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		statuses[scenario] = response.StatusCode
	}
	writeJSON(w, 200, map[string]any{"fixture": "stock-ai-backend-oidc-rejections-v1", "statuses": statuses,
		"external_calls": 0, "tokens_disclosed": false})
}

var adapterID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func adapterRoute(method, path string) (string, bool, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	if !strings.HasPrefix(path, "/v1/") {
		return "", false, false
	}
	if len(parts) == 2 && parts[0] == "accounts" && strings.HasPrefix(parts[1], "acc_") && adapterID.MatchString(parts[1]) && method == "GET" {
		return "GET /v1/accounts/:id", true, true
	}
	if method == "GET" && path == "/v1/market/price" {
		return "GET /v1/market/price", true, true
	}
	if method == "GET" {
		for _, allowed := range []string{"/v1/account", "/v1/positions", "/v1/orders", "/v1/trades", "/v1/ledger", "/v1/messages", "/v1/market/info", "/v1/market/ohlcv"} {
			if path == allowed {
				return "GET " + path, false, true
			}
		}
	}
	if method == "POST" && (path == "/v1/orders" || path == "/v1/messages") {
		return "POST " + path, false, true
	}
	if len(parts) >= 2 && adapterID.MatchString(parts[1]) {
		if parts[0] == "orders" && len(parts) == 2 && method == "GET" {
			return "GET /v1/orders/:id", false, true
		}
		if parts[0] == "orders" && len(parts) == 3 && parts[2] == "cancel" && method == "POST" {
			return "POST /v1/orders/:id/cancel", false, true
		}
		if parts[0] == "positions" && len(parts) == 2 && method == "PATCH" {
			return "PATCH /v1/positions/:id", false, true
		}
		if parts[0] == "positions" && len(parts) == 3 && parts[2] == "close" && method == "POST" {
			return "POST /v1/positions/:id/close", false, true
		}
	}
	return "", false, false
}

func (f *fixture) adapterCounters(w http.ResponseWriter, r *http.Request) {
	if !f.identity().BackendAdapter || r.Method != "GET" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, 200, map[string]any{"fixture": "stock-ai-private-backend-oidc-adapter-v1", "routes": f.adapterCalls,
		"upstream": "http://backend:7794", "external_calls": 0, "user_token_ttl_seconds": 45,
		"account_credentials_preserved": true, "privileged_routes": []string{"GET /v1/accounts/:id", "GET /v1/market/price"}})
}

func (f *fixture) adapterHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.identity().BackendAdapter {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/healthz" && r.URL.RawQuery == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		route, privileged, allowed := adapterRoute(r.Method, r.URL.Path)
		if !allowed || r.URL.RawPath != "" || r.URL.Fragment != "" || len(r.URL.RawQuery) > 4096 || r.URL.IsAbs() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		query, queryErr := url.ParseQuery(r.URL.RawQuery)
		if queryErr != nil || (strings.HasPrefix(r.URL.Path, "/v1/accounts/") && len(query) != 0) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		credential := strings.TrimPrefix(headers[0], "Bearer ")
		if credential == "" || strings.ContainsAny(credential, " \t\r\n") || len(credential) > 8192 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		actual, expected := sha256.Sum256([]byte(credential)), sha256.Sum256([]byte(f.identity().BackendSecret))
		service := subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
		// Account lookup is always USER-only. The service credential can never
		// become USER on mutations, monitoring, messages, or Account routes.
		if (service && !privileged) || (!service && strings.HasPrefix(r.URL.Path, "/v1/accounts/")) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if service {
			if r.URL.Path == "/v1/market/price" {
				for name := range query {
					if name != "market" && name != "symbol" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
				}
			}
			if r.URL.Path == "/v1/market/price" && (len(query) > 2 || len(query["symbol"]) != 1 || query.Get("symbol") != "BTCUSDT" ||
				(len(query["market"]) > 1 || (query.Get("market") != "" && query.Get("market") != "crypto"))) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			now := time.Now().Unix()
			var err error
			credential, err = f.signToken("at+jwt", map[string]any{"iss": f.identity().Issuer, "aud": f.identity().Audience,
				"sub": f.identity().Subject, "iat": now, "nbf": now - 1, "exp": now + 45})
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil || (r.Method == "GET" && len(body) != 0) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		upstream, err := http.NewRequestWithContext(r.Context(), r.Method, "http://backend:7794"+r.URL.Path, strings.NewReader(string(body)))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upstream.URL.RawQuery = query.Encode()
		upstream.Header.Set("Authorization", "Bearer "+credential)
		upstream.Header.Set("Accept", "application/json")
		if len(body) > 0 {
			upstream.Header.Set("Content-Type", "application/json")
		}
		client := f.backendClient
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil},
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		}
		response, err := client.Do(upstream)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		value, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		if err != nil || len(value) > 4<<20 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		f.mu.Lock()
		if f.adapterCalls == nil {
			f.adapterCalls = make(map[string]int)
		}
		kind := "ACCOUNT "
		if service {
			kind = "USER "
		}
		f.adapterCalls[kind+route]++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(value)
	})
}

// Market routes are private to the Release application network. The immutable
// Backend keeps https://api.binance.com and strict certificate verification;
// its Docker DNS alias resolves to this local TLS listener.
func (f *fixture) marketHandler() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/api/v3/ticker/price", "/api/v3/exchangeInfo", "/api/v3/klines"} {
		mux.HandleFunc(path, f.marketResponse)
	}
	return mux
}

func (f *fixture) marketCounters(w http.ResponseWriter, r *http.Request) {
	if !f.identity().MarketFixture || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"fixture": "stock-ai-local-binance-v1", "symbol": "BTCUSDT", "price": 100,
		"routes": f.marketCalls, "external_calls": 0})
}

func (f *fixture) marketResponse(w http.ResponseWriter, r *http.Request) {
	if !f.identity().MarketFixture || r.Method != http.MethodGet {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("symbol") != "BTCUSDT" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1121, "msg": "Invalid symbol."})
		return
	}
	f.mu.Lock()
	if f.marketCalls == nil {
		f.marketCalls = make(map[string]int)
	}
	f.marketCalls[r.URL.Path]++
	f.mu.Unlock()
	w.Header().Set("X-Stock-AI-Market-Fixture", "stock-ai-local-binance-v1")
	switch r.URL.Path {
	case "/api/v3/ticker/price":
		writeJSON(w, http.StatusOK, map[string]string{"symbol": "BTCUSDT", "price": "100.00000000"})
	case "/api/v3/exchangeInfo":
		writeJSON(w, http.StatusOK, map[string]any{"symbols": []any{map[string]any{"symbol": "BTCUSDT", "status": "TRADING",
			"baseAsset": "BTC", "quoteAsset": "USDT", "orderTypes": []string{"LIMIT", "MARKET"},
			"isSpotTradingAllowed": true, "isMarginTradingAllowed": false,
			"filters": []any{map[string]string{"filterType": "PRICE_FILTER", "minPrice": "0.01", "maxPrice": "1000000", "tickSize": "0.01"},
				map[string]string{"filterType": "LOT_SIZE", "minQty": "0.00001", "maxQty": "1000", "stepSize": "0.00001"},
				map[string]string{"filterType": "MIN_NOTIONAL", "minNotional": "0.01"}}}}})
	case "/api/v3/klines":
		start, err := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		end, endErr := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		duration := map[string]int64{"1m": 60000, "5m": 300000, "15m": 900000, "1h": 3600000, "4h": 14400000, "1d": 86400000}[r.URL.Query().Get("interval")]
		limit, limitErr := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || endErr != nil || limitErr != nil || duration == 0 || limit < 1 || limit > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": -1100, "msg": "Invalid fixture query."})
			return
		}
		rows := make([]any, 0)
		aligned := ((start + duration - 1) / duration) * duration
		for opening := aligned; opening+duration-1 <= end && len(rows) < limit; opening += duration {
			rows = append(rows, []any{opening, "100", "101", "99", "100", "1", opening + duration - 1, "100", 1, "1", "100", "0"})
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

func (f *fixture) down(w http.ResponseWriter) bool {
	f.mu.Lock()
	down := f.outage
	f.mu.Unlock()
	if down {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_unavailable"})
	}
	return down
}

func (f *fixture) discovery(w http.ResponseWriter, _ *http.Request) {
	issuer := f.identity().Issuer
	if f.down(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"jwks_uri":                              issuer + "/jwks",
		"end_session_endpoint":                  issuer + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fixture) authorize(w http.ResponseWriter, r *http.Request) {
	clientID := f.identity().ClientID
	if f.down(w) {
		return
	}
	q := r.URL.Query()
	if q.Get("client_id") != clientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("redirect_uri") == "" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_authorization_request"})
		return
	}
	f.mu.Lock()
	scenario := f.scenarioNext
	f.scenarioNext = ""
	f.mu.Unlock()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("redirect_uri") != f.identity().RedirectURI {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_redirect_uri"})
		return
	}
	if scenario == "denial" {
		redirectQuery := redirect.Query()
		redirectQuery.Set("error", "access_denied")
		redirectQuery.Set("state", q.Get("state"))
		redirect.RawQuery = redirectQuery.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
		return
	}
	code, err := randomString(32)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_error"})
		return
	}
	f.mu.Lock()
	f.codes[code] = authorization{redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), scenario: scenario}
	f.mu.Unlock()
	redirectQuery := redirect.Query()
	redirectQuery.Set("code", code)
	redirectQuery.Set("state", q.Get("state"))
	redirect.RawQuery = redirectQuery.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (f *fixture) token(w http.ResponseWriter, r *http.Request) {
	if f.down(w) {
		return
	}
	client, secret, ok := r.BasicAuth()
	if !ok || client != f.identity().ClientID || secret != f.identity().ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	code := r.Form.Get("code")
	f.mu.Lock()
	a, exists := f.codes[code]
	delete(f.codes, code)
	f.mu.Unlock()
	if !exists || r.Form.Get("redirect_uri") != a.redirectURI || r.Form.Get("code_verifier") == "" || pkce(r.Form.Get("code_verifier")) != a.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	subject := f.identity().Subject
	tokenIssuer, audience := f.identity().Issuer, f.identity().ClientID
	apiAudience := f.identity().Audience
	expiresIn := int64(3600)
	exp := time.Now().Add(time.Hour).Unix()
	accessExp := exp
	switch a.scenario {
	case "wrong-subject":
		subject = "unexpected-local-user"
	case "expired-token":
		exp = time.Now().Add(-time.Minute).Unix()
	case "short-token":
		expiresIn = 2
		accessExp = time.Now().Add(2 * time.Second).Unix()
	case "expired-access-token":
		accessExp = time.Now().Add(-time.Minute).Unix()
	case "issuer-mismatch":
		tokenIssuer = "https://wrong.localtest/realms/stock-ai-test"
	case "audience-mismatch":
		audience, apiAudience = "wrong-client", "wrong-api"
	}
	idClaims := map[string]any{
		"iss": tokenIssuer, "aud": audience, "sub": subject, "nonce": a.nonce,
		"preferred_username": f.identity().Subject, "email": f.identity().Subject + "@example.invalid", "exp": exp,
	}
	accessClaims := map[string]any{"iss": tokenIssuer, "aud": apiAudience, "sub": subject, "exp": accessExp, "nbf": time.Now().Add(-time.Minute).Unix()}
	idToken, err := f.signToken("JWT", idClaims)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_error"})
		return
	}
	accessToken, err := f.signToken("at+jwt", accessClaims)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "fixture_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": accessToken, "id_token": idToken, "token_type": "Bearer", "expires_in": expiresIn})
}

func (f *fixture) jwks(w http.ResponseWriter, _ *http.Request) {
	if f.down(w) {
		return
	}
	n := base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.PublicKey.E)).Bytes())
	writeJSON(w, http.StatusOK, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.identity().KeyID, "n": n, "e": e}}})
}

func (f *fixture) logout(w http.ResponseWriter, r *http.Request) {
	if f.down(w) {
		return
	}
	postLogout := r.URL.Query().Get("post_logout_redirect_uri")
	if postLogout != f.identity().LogoutURI {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_logout_uri"})
		return
	}
	http.Redirect(w, r, postLogout, http.StatusFound)
}

func (f *fixture) nextCase(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Scenario string `json:"scenario"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_control"})
		return
	}
	allowed := map[string]bool{"success": true, "wrong-subject": true, "expired-token": true, "expired-access-token": true, "short-token": true, "issuer-mismatch": true, "audience-mismatch": true, "denial": true}
	if !allowed[input.Scenario] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_scenario"})
		return
	}
	f.mu.Lock()
	f.scenarioNext = input.Scenario
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"accepted": input.Scenario})
}

func (f *fixture) setOutage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fixture_control"})
		return
	}
	f.mu.Lock()
	f.outage = input.Enabled
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"outage": input.Enabled})
}

func (f *fixture) complete(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.identity().ProviderSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_api_key"})
		return
	}
	var request struct {
		Tools    []json.RawMessage `json:"tools"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	f.mu.Lock()
	f.providerCalls++
	f.mu.Unlock()
	if f.identity().MarketFixture {
		toolResults := 0
		for _, message := range request.Messages {
			if message.Role == "tool" {
				toolResults++
			}
		}
		if len(request.Tools) == 0 || toolResults >= 2 {
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
				"role": "assistant", "content": `{"output":"Local market read and stopped order completed.","summary":"Deterministic Release fixture completed.","candidate_memories":[],"expire_memories":[]}`,
			}, "finish_reason": "stop"}}})
			return
		}
		name, args := "get_market_snapshot", `{"market":"crypto","symbol":"BTCUSDT"}`
		if toolResults == 1 {
			name, args = "place_order", `{"market":"crypto","symbol":"BTCUSDT","product":"spot","side":"buy","type":"limit","quantity":0.001,"price":100,"stop_loss":90,"take_profit":110}`
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
			"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-release-" + name, "type": "function", "function": map[string]string{"name": name, "arguments": args}}},
		}, "finish_reason": "tool_calls"}}})
		return
	}
	for _, message := range request.Messages {
		if message.Role == "tool" {
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
				"role": "assistant", "content": `{"output":"Local fixture placed the requested limit order.","summary":"Limit order completed by local Test fixture.","candidate_memories":[],"expire_memories":[]}`,
			}, "finish_reason": "stop"}}})
			return
		}
	}
	args := `{"market":"crypto","symbol":"BTCUSDT","product":"spot","side":"buy","type":"limit","quantity":0.001,"price":100}`
	writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{
		"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call-local-limit-order", "type": "function", "function": map[string]string{"name": "place_order", "arguments": args}}},
	}, "finish_reason": "tool_calls"}}})
}

func (f *fixture) signToken(typ string, claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": f.identity().KeyID, "typ": typ})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func pkce(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomString(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
