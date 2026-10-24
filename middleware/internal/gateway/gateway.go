package gateway

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"middleware"
)

const maxOIDCResponse = 1 << 20

type loginAttempt struct {
	Nonce, Verifier, ReturnTo string
	Expires                   time.Time
}
type session struct {
	Subject, User, Email, AccessToken string
	Expires                           time.Time
}

type Server struct {
	cfg      Config
	client   *http.Client
	now      func() time.Time
	mu       sync.Mutex
	attempts map[string]loginAttempt
	sessions map[string]session

	backend, agent, llm *httputil.ReverseProxy
}

func New(cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, now: time.Now, attempts: map[string]loginAttempt{}, sessions: map[string]session{}, client: &http.Client{
		Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if err := s.newProxies(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /oauth2/start", s.start)
	mux.HandleFunc("GET /oauth2/callback", s.callback)
	mux.HandleFunc("GET /oauth2/me", s.me)
	mux.HandleFunc("POST /oauth2/logout", s.logout)
	mux.HandleFunc("/v1", s.serveBackend)
	mux.HandleFunc("/v1/", s.serveBackend)
	mux.HandleFunc("/agent/", s.serveAgent)
	mux.HandleFunc("/llm/", s.serveLLM)
	if winTest {
		// No nginx in front: answer the frontend's middleware health probe here too.
		mux.HandleFunc("GET /middleware/healthz", s.health)
		mux.Handle("/", s.serveWeb())
	}
	return accessLog(s.securityHeaders(mux))
}

// accessLog records every request without its query string, which carries OIDC codes and
// state. Passing health probes run every few seconds and carry no information, so only a
// failing one is logged.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if r.URL.Path == "/healthz" && recorder.status < 300 {
			return
		}
		log.Printf("event=http_request method=%s path=%s status=%d duration=%s",
			r.Method, r.URL.Path, recorder.status, time.Since(start).Round(time.Microsecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status = status
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(body []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(body)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": middleware.Version})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func randomURLSafe(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func safeReturnTo(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	for _, blocked := range []string{"/oauth2/callback", "/oauth2/logout"} {
		if u.Path == blocked {
			return "/"
		}
	}
	return u.String()
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if winTest {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	state, err := randomURLSafe(32)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	nonce, err := randomURLSafe(32)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	verifier, err := randomURLSafe(64)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, "login_unavailable")
		return
	}
	s.mu.Lock()
	s.cleanupLocked()
	s.attempts[state] = loginAttempt{Nonce: nonce, Verifier: verifier, ReturnTo: safeReturnTo(r.URL.Query().Get("return")), Expires: s.now().Add(s.cfg.LoginTTL)}
	s.mu.Unlock()
	http.SetCookie(w, s.loginCookie(s.signID(state), int(s.cfg.LoginTTL.Seconds())))
	challenge := sha256.Sum256([]byte(verifier))
	u, _ := url.Parse(s.cfg.AuthorizationURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.cfg.ClientID)
	q.Set("redirect_uri", s.cfg.RedirectURI)
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) takeAttempt(state string) (loginAttempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	a, ok := s.attempts[state]
	if ok {
		delete(s.attempts, state)
	}
	return a, ok
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if winTest {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	state := r.URL.Query().Get("state")
	transaction, cookieErr := r.Cookie(s.loginCookieName())
	boundState, bound := "", false
	if cookieErr == nil {
		boundState, bound = s.verifiedID(transaction.Value)
	}
	s.clearLoginCookie(w)
	if !bound || subtle.ConstantTimeCompare([]byte(boundState), []byte(state)) != 1 {
		s.clearSession(w, r)
		s.callbackFailure(w, r, "invalid_callback")
		return
	}
	a, ok := s.takeAttempt(state)
	if !ok {
		s.clearSession(w, r)
		s.callbackFailure(w, r, "invalid_callback")
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_denied")
		return
	}
	tokens, err := s.exchange(r.Context(), r.URL.Query().Get("code"), a.Verifier)
	if err != nil {
		log.Printf("OIDC login failed: %v", err)
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_failed")
		return
	}
	claims, err := s.verifyIDToken(r.Context(), tokens.IDToken, a.Nonce)
	if err != nil {
		log.Printf("OIDC login failed: ID token rejected: %v", err)
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_failed")
		return
	}
	// Reject here what the backend would reject later, so a bad IdP mapping fails at login instead of as API 401s.
	if err := s.checkAccessToken(tokens.AccessToken); err != nil {
		log.Printf("OIDC login failed: access token rejected: %v", err)
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_failed")
		return
	}
	ttl := s.cfg.SessionTTL
	if tokens.ExpiresIn <= 0 {
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_failed")
		return
	}
	if d := time.Duration(tokens.ExpiresIn) * time.Second; d < ttl {
		ttl = d
	}
	if until := time.Unix(claims.Expires, 0).Sub(s.now()); until < ttl {
		ttl = until
	}
	if ttl <= 0 {
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_failed")
		return
	}
	id, err := randomURLSafe(32)
	if err != nil {
		s.clearSession(w, r)
		s.callbackFailure(w, r, "login_failed")
		return
	}
	s.clearSession(w, r)
	s.mu.Lock()
	s.sessions[id] = session{Subject: claims.Subject, User: claims.PreferredUsername, Email: claims.Email, AccessToken: tokens.AccessToken, Expires: s.now().Add(ttl)}
	s.mu.Unlock()
	http.SetCookie(w, s.cookie(s.signID(id), int(ttl.Seconds())))
	log.Printf("OIDC login succeeded: user=%q", claims.PreferredUsername)
	http.Redirect(w, r, a.ReturnTo, http.StatusSeeOther)
}

func (s *Server) callbackFailure(w http.ResponseWriter, r *http.Request, code string) {
	log.Printf("OIDC login rejected: %s", code)
	http.Redirect(w, r, "/login?auth_error="+url.QueryEscape(code), http.StatusSeeOther)
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (s *Server) exchange(ctx context.Context, code, verifier string) (tokenResponse, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.cfg.RedirectURI}, "code_verifier": {verifier}}
	if s.cfg.TokenAuthMethod == "client_secret_post" {
		form.Set("client_id", s.cfg.ClientID)
		form.Set("client_secret", s.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if s.cfg.TokenAuthMethod == "client_secret_basic" {
		// RFC 6749 §2.3.1: credentials are form-urlencoded before Basic encoding.
		req.SetBasicAuth(url.QueryEscape(s.cfg.ClientID), url.QueryEscape(s.cfg.ClientSecret))
	}
	res, err := s.client.Do(req)
	if err != nil {
		return tokenResponse{}, explainTLS(err)
	}
	defer res.Body.Close()
	reader := io.LimitReader(res.Body, maxOIDCResponse+1)
	raw, err := io.ReadAll(reader)
	if err != nil || len(raw) > maxOIDCResponse {
		return tokenResponse{}, errors.New("token endpoint response is unreadable or too large")
	}
	if res.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("token endpoint returned %d (error=%s, OIDC_TOKEN_AUTH_METHOD=%s)", res.StatusCode, oauthErrorCode(raw), s.cfg.TokenAuthMethod)
	}
	var out tokenResponse
	if json.Unmarshal(raw, &out) != nil || out.AccessToken == "" || out.IDToken == "" || !strings.EqualFold(out.TokenType, "Bearer") {
		return tokenResponse{}, errors.New("token endpoint response lacks access_token, id_token or Bearer token_type")
	}
	return out, nil
}

// oauthErrorCode keeps only the RFC 6749 error code; descriptions may echo request data.
func oauthErrorCode(raw []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Error == "" || len(body.Error) > 64 || strings.IndexFunc(body.Error, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) >= 0 {
		return "unknown"
	}
	return body.Error
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.current(r)
	if !ok {
		s.clearCookie(w)
		s.fail(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"subject": sess.Subject, "user_name": sess.User, "email": sess.Email, "expires_at": sess.Expires.UTC().Format(time.RFC3339)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if winTest {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	app, _ := url.Parse(s.cfg.RedirectURI)
	expected := app.Scheme + "://" + app.Host
	if r.Header.Get("Origin") != expected {
		s.fail(w, http.StatusForbidden, "invalid_origin")
		return
	}
	if sess, ok := s.current(r); ok {
		log.Printf("logout: user=%q", sess.User)
	}
	s.clearSession(w, r)
	if s.cfg.EndSessionURL == "" {
		http.Redirect(w, r, s.cfg.PostLogoutURI, http.StatusSeeOther)
		return
	}
	u, _ := url.Parse(s.cfg.EndSessionURL)
	q := u.Query()
	q.Set("client_id", s.cfg.ClientID)
	q.Set("post_logout_redirect_uri", s.cfg.PostLogoutURI)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (s *Server) signID(id string) string {
	mac := hmac.New(sha256.New, s.cfg.CookieKey)
	_, _ = mac.Write([]byte(id))
	return id + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) verifiedID(raw string) (string, bool) {
	id, macText, ok := strings.Cut(raw, ".")
	if !ok || id == "" {
		return "", false
	}
	supplied, err := base64.RawURLEncoding.DecodeString(macText)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, s.cfg.CookieKey)
	_, _ = mac.Write([]byte(id))
	return id, subtle.ConstantTimeCompare(supplied, mac.Sum(nil)) == 1
}
func (s *Server) current(r *http.Request) (session, bool) {
	if winTest {
		return s.winTestSession(), true
	}
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil {
		return session{}, false
	}
	id, ok := s.verifiedID(c.Value)
	if !ok {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	sess, ok := s.sessions[id]
	return sess, ok
}
func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cfg.CookieName); err == nil {
		if id, ok := s.verifiedID(c.Value); ok {
			s.mu.Lock()
			delete(s.sessions, id)
			s.mu.Unlock()
		}
	}
	s.clearCookie(w)
}
func (s *Server) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: s.cfg.CookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}
func (s *Server) loginCookieName() string {
	return "__Secure-" + strings.TrimPrefix(strings.TrimSuffix(s.cfg.CookieName, "-session"), "__Host-") + "-login"
}
func (s *Server) loginCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: s.loginCookieName(), Value: value, Path: "/oauth2/callback", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}
func (s *Server) clearCookie(w http.ResponseWriter) {
	c := s.cookie("", -1)
	c.Expires = time.Unix(1, 0)
	http.SetCookie(w, c)
}
func (s *Server) clearLoginCookie(w http.ResponseWriter) {
	c := s.loginCookie("", -1)
	c.Expires = time.Unix(1, 0)
	http.SetCookie(w, c)
}
func (s *Server) cleanupLocked() {
	now := s.now()
	for k, v := range s.attempts {
		if !v.Expires.After(now) {
			delete(s.attempts, k)
		}
	}
	for k, v := range s.sessions {
		if !v.Expires.After(now) {
			delete(s.sessions, k)
		}
	}
}
func (s *Server) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

type idClaims struct {
	Issuer            string          `json:"iss"`
	Audience          json.RawMessage `json:"aud"`
	AuthorizedParty   string          `json:"azp"`
	Subject           string          `json:"sub"`
	Nonce             string          `json:"nonce"`
	PreferredUsername string          `json:"preferred_username"`
	Email             string          `json:"email"`
	Expires           int64           `json:"exp"`
	NotBefore         int64           `json:"nbf"`
}
type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type accessClaims struct {
	Issuer   string          `json:"iss"`
	Audience json.RawMessage `json:"aud"`
	Subject  string          `json:"sub"`
}

func decodePart(part string, target any) error {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err = d.Decode(target); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("extra JSON")
	}
	return nil
}
func audience(raw json.RawMessage, authorizedParty, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want && (authorizedParty == "" || authorizedParty == want)
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	if len(many) < 2 || authorizedParty != want {
		return false
	}
	for _, v := range many {
		if v == want {
			return true
		}
	}
	return false
}
func (s *Server) verifyIDToken(ctx context.Context, token, nonce string) (idClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("invalid id token")
	}
	var h jwtHeader
	if decodePart(parts[0], &h) != nil || h.Algorithm != "RS256" || h.KeyID == "" {
		return idClaims{}, errors.New("invalid id token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return idClaims{}, err
	}
	key, err := s.fetchKey(ctx, h.KeyID)
	if err != nil {
		return idClaims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return idClaims{}, errors.New("invalid signature")
	}
	var c idClaims
	if decodePart(parts[1], &c) != nil {
		return idClaims{}, errors.New("invalid claims")
	}
	now := s.now().Unix()
	if c.Issuer != s.cfg.Issuer || !audience(c.Audience, c.AuthorizedParty, s.cfg.ClientID) || c.Subject != s.cfg.UserSubject || c.Nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 || c.Expires <= now || (c.NotBefore != 0 && c.NotBefore > now) {
		return idClaims{}, errors.New("invalid claims")
	}
	if c.PreferredUsername == "" {
		c.PreferredUsername = c.Subject
	}
	return c, nil
}

// checkAccessToken compares the unverified access token shape with the backend's rules; the backend still verifies the signature.
func (s *Server) checkAccessToken(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("access token is not a JWT")
	}
	var h jwtHeader
	if decodePart(parts[0], &h) != nil {
		return errors.New("access token header is not valid JSON")
	}
	if h.Type != s.cfg.AccessTokenType {
		return fmt.Errorf("header typ=%q but OIDC_TOKEN_TYPE=%q", h.Type, s.cfg.AccessTokenType)
	}
	if h.Algorithm != "RS256" || h.KeyID == "" {
		return fmt.Errorf("header alg=%q kid_present=%t, want RS256 with kid", h.Algorithm, h.KeyID != "")
	}
	var c accessClaims
	if decodePart(parts[1], &c) != nil {
		return errors.New("access token claims are not valid JSON")
	}
	if c.Issuer != s.cfg.Issuer {
		return fmt.Errorf("iss=%q but OIDC_ISSUER=%q", c.Issuer, s.cfg.Issuer)
	}
	if !containsAudience(c.Audience, s.cfg.Audience) {
		return fmt.Errorf("aud=%s does not contain OIDC_AUDIENCE=%q", c.Audience, s.cfg.Audience)
	}
	if c.Subject != s.cfg.UserSubject {
		return errors.New("sub does not match OIDC_USER_SUBJECT")
	}
	return nil
}

func containsAudience(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	for _, v := range many {
		if v == want {
			return true
		}
	}
	return false
}

type jwksDocument struct {
	Keys []json.RawMessage `json:"keys"`
}

type rsaJWK struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

func (s *Server) fetchKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("jwks unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxOIDCResponse+1))
	if err != nil || len(raw) > maxOIDCResponse {
		return nil, errors.New("invalid jwks")
	}
	keys, err := parseJWKS(raw)
	if err != nil {
		return nil, errors.New("invalid jwks")
	}
	key := keys[kid]
	if key == nil {
		return nil, errors.New("signing key unavailable")
	}
	return key, nil
}

func parseJWKS(raw []byte) (map[string]*rsa.PublicKey, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	var document jwksDocument
	if err := decoder.Decode(&document); err != nil || len(document.Keys) == 0 || len(document.Keys) > 32 {
		return nil, errors.New("invalid JWKS")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("invalid JWKS")
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, rawKey := range document.Keys {
		var jwk rsaJWK
		if json.Unmarshal(rawKey, &jwk) != nil || jwk.KeyType != "RSA" || jwk.KeyID == "" ||
			(jwk.Use != "" && jwk.Use != "sig") || (jwk.Algorithm != "" && jwk.Algorithm != "RS256") {
			continue
		}
		key, err := rsaPublicKey(jwk.Modulus, jwk.Exponent)
		if err != nil {
			continue
		}
		if _, duplicate := keys[jwk.KeyID]; duplicate {
			return nil, errors.New("duplicate signing key")
		}
		keys[jwk.KeyID] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("no supported signing keys")
	}
	return keys, nil
}

func rsaPublicKey(modulus, exponent string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil || len(nBytes) == 0 {
		return nil, errors.New("invalid modulus")
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
		return nil, errors.New("invalid exponent")
	}
	e := 0
	for _, item := range eBytes {
		e = e<<8 | int(item)
	}
	if e < 3 || e%2 == 0 {
		return nil, errors.New("invalid exponent")
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("RSA key is too small")
	}
	return key, nil
}
