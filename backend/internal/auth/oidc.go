package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxJWKSBytes = 1 << 20
	maxJWKKeys   = 32
)

type OIDCSettings struct {
	Issuer          string
	Audience        string
	Subject         string
	JWKSURL         string
	TokenType       string
	Timeout         time.Duration
	CacheTTL        time.Duration
	RefreshCooldown time.Duration
	Now             func() time.Time
	Client          *http.Client
}

type OIDCVerifier struct {
	settings      OIDCSettings
	client        *http.Client
	now           func() time.Time
	mu            sync.Mutex
	keys          map[string]*rsa.PublicKey
	expires       time.Time
	generation    uint64
	cooldownUntil time.Time
	refreshing    bool
	refreshDone   chan struct{}
}

func NewOIDCVerifier(settings OIDCSettings) *OIDCVerifier {
	if settings.Timeout <= 0 || settings.Timeout > 10*time.Second {
		settings.Timeout = 3 * time.Second
	}
	if settings.CacheTTL <= 0 || settings.CacheTTL > time.Hour {
		settings.CacheTTL = 15 * time.Minute
	}
	if settings.RefreshCooldown <= 0 || settings.RefreshCooldown > 5*time.Minute {
		settings.RefreshCooldown = 30 * time.Second
	}
	now := settings.Now
	if now == nil {
		now = time.Now
	}
	client := settings.Client
	if client == nil {
		client = &http.Client{
			Timeout: settings.Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &OIDCVerifier{settings: settings, client: client, now: now}
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type jwtClaims struct {
	Issuer    string          `json:"iss"`
	Audience  json.RawMessage `json:"aud"`
	Subject   string          `json:"sub"`
	Expires   json.Number     `json:"exp"`
	NotBefore json.Number     `json:"nbf"`
}

func (v *OIDCVerifier) Verify(ctx context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", ErrInvalidToken
	}

	var header jwtHeader
	if err := decodeJWTPart(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" || header.Type != v.settings.TokenType {
		return "", ErrInvalidToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", ErrInvalidToken
	}
	signed := []byte(parts[0] + "." + parts[1])
	key, generation, err := v.key(ctx, header.KeyID, false, 0)
	if err != nil {
		return "", ErrInvalidToken
	}
	if !validSignature(key, signed, signature) {
		key, _, err = v.key(ctx, header.KeyID, true, generation)
		if err != nil || !validSignature(key, signed, signature) {
			return "", ErrInvalidToken
		}
	}

	var claims jwtClaims
	if err := decodeJWTPart(parts[1], &claims); err != nil {
		return "", ErrInvalidToken
	}
	if claims.Issuer != v.settings.Issuer || claims.Subject != v.settings.Subject || !audienceContains(claims.Audience, v.settings.Audience) {
		return "", ErrInvalidToken
	}
	expires, err := claims.Expires.Int64()
	if err != nil || expires <= v.now().Unix() {
		return "", ErrInvalidToken
	}
	if string(claims.NotBefore) != "" {
		notBefore, err := claims.NotBefore.Int64()
		if err != nil || notBefore > v.now().Unix() {
			return "", ErrInvalidToken
		}
	}
	return claims.Subject, nil
}

func decodeJWTPart(part string, target any) error {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}

func audienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	for _, audience := range many {
		if audience == expected {
			return true
		}
	}
	return false
}

func validSignature(key *rsa.PublicKey, signed, signature []byte) bool {
	digest := sha256.Sum256(signed)
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) == nil
}

func (v *OIDCVerifier) key(ctx context.Context, keyID string, force bool, observedGeneration uint64) (*rsa.PublicKey, uint64, error) {
	for {
		v.mu.Lock()
		now := v.now()
		key := v.keys[keyID]
		if force && v.generation != observedGeneration {
			generation := v.generation
			v.mu.Unlock()
			if key == nil {
				return nil, generation, errors.New("signing key unavailable")
			}
			return key, generation, nil
		}
		if !force && now.Before(v.expires) && key != nil {
			generation := v.generation
			v.mu.Unlock()
			return key, generation, nil
		}
		if v.refreshing {
			done := v.refreshDone
			v.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}
		if now.Before(v.cooldownUntil) {
			generation := v.generation
			v.mu.Unlock()
			return nil, generation, errors.New("JWKS refresh cooling down")
		}

		v.refreshing = true
		v.refreshDone = make(chan struct{})
		done := v.refreshDone
		v.mu.Unlock()

		keys, err := v.fetch(ctx)

		v.mu.Lock()
		v.cooldownUntil = v.now().Add(v.settings.RefreshCooldown)
		if err == nil {
			v.keys = keys
			v.expires = v.now().Add(v.settings.CacheTTL)
			v.generation++
		}
		v.refreshing = false
		close(done)
		key = v.keys[keyID]
		generation := v.generation
		v.mu.Unlock()
		if err != nil {
			return nil, generation, err
		}
		if key == nil {
			return nil, generation, errors.New("signing key unavailable")
		}
		return key, generation, nil
	}
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

func (v *OIDCVerifier) fetch(parent context.Context) (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(parent, v.settings.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.settings.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("JWKS request failed")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil || len(raw) > maxJWKSBytes {
		return nil, errors.New("invalid JWKS")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	var document jwksDocument
	if err := decoder.Decode(&document); err != nil || len(document.Keys) == 0 || len(document.Keys) > maxJWKKeys {
		return nil, errors.New("invalid JWKS")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("invalid JWKS")
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, raw := range document.Keys {
		var jwk rsaJWK
		if json.Unmarshal(raw, &jwk) != nil || jwk.KeyType != "RSA" || jwk.KeyID == "" ||
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
