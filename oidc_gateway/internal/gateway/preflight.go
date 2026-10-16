package gateway

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type discoveryDocument struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	EndSessionEndpoint    string   `json:"end_session_endpoint"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

// Preflight checks, from inside the deployment, that the external IdP matches this configuration.
func (s *Server) Preflight(ctx context.Context, out io.Writer) error {
	var d discoveryDocument
	if err := s.getJSON(ctx, strings.TrimSuffix(s.cfg.Issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	fmt.Fprintln(out, "ok   TLS and discovery reachable")
	if d.Issuer != s.cfg.Issuer {
		return fmt.Errorf("discovery issuer=%q but OIDC_ISSUER=%q", d.Issuer, s.cfg.Issuer)
	}
	for _, endpoint := range []struct{ name, configured, discovered string }{
		{"OIDC_AUTHORIZATION_URL", s.cfg.AuthorizationURL, d.AuthorizationEndpoint},
		{"OIDC_TOKEN_URL", s.cfg.TokenURL, d.TokenEndpoint},
		{"OIDC_JWKS_URL", s.cfg.JWKSURL, d.JWKSURI},
	} {
		if endpoint.configured != endpoint.discovered {
			return fmt.Errorf("%s=%q but discovery says %q", endpoint.name, endpoint.configured, endpoint.discovered)
		}
	}
	if s.cfg.EndSessionURL != "" && s.cfg.EndSessionURL != d.EndSessionEndpoint {
		return fmt.Errorf("OIDC_END_SESSION_URL=%q but discovery says %q", s.cfg.EndSessionURL, d.EndSessionEndpoint)
	}
	fmt.Fprintln(out, "ok   issuer and endpoints match discovery")
	methods := d.TokenAuthMethods
	if len(methods) == 0 {
		methods = []string{"client_secret_basic"} // OpenID Connect Discovery default.
	}
	if !slices.Contains(methods, s.cfg.TokenAuthMethod) {
		return fmt.Errorf("OIDC_TOKEN_AUTH_METHOD=%s is not supported by the IdP %v", s.cfg.TokenAuthMethod, methods)
	}
	fmt.Fprintln(out, "ok   token endpoint supports "+s.cfg.TokenAuthMethod)
	if err := s.checkJWKS(ctx); err != nil {
		return fmt.Errorf("JWKS: %w", err)
	}
	fmt.Fprintln(out, "ok   JWKS has an RS256 key of at least 2048 bits")
	if err := s.probeRedirect(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "ok   authorization endpoint accepts the redirect URI (best effort)")
	fmt.Fprintln(out, "Register in the IdP client "+s.cfg.ClientID+":")
	fmt.Fprintln(out, "  Redirect URI:    "+s.cfg.RedirectURI)
	fmt.Fprintln(out, "  Post-logout URI: "+s.cfg.PostLogoutURI)
	return nil
}

func (s *Server) checkJWKS(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	raw, status, err := s.read(req)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d", status)
	}
	_, err = parseJWKS(raw)
	return err
}

// probeRedirect asks the IdP to start a login; IdPs reject an unregistered redirect_uri before showing a login page.
func (s *Server) probeRedirect(ctx context.Context) error {
	u, _ := url.Parse(s.cfg.AuthorizationURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.cfg.ClientID)
	q.Set("redirect_uri", s.cfg.RedirectURI)
	q.Set("scope", "openid profile email")
	q.Set("state", "preflight")
	q.Set("nonce", "preflight")
	q.Set("code_challenge", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("authorization endpoint: %w", explainTLS(err))
	}
	res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("authorization endpoint returned %d for redirect_uri %s; register it in the IdP client %s", res.StatusCode, s.cfg.RedirectURI, s.cfg.ClientID)
	}
	if location, err := url.Parse(res.Header.Get("Location")); err == nil && location.Query().Get("error") != "" {
		return fmt.Errorf("authorization endpoint redirected with error=%s", location.Query().Get("error"))
	}
	return nil
}

func (s *Server) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	raw, status, err := s.read(req)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", status, endpoint)
	}
	if json.Unmarshal(raw, target) != nil {
		return fmt.Errorf("invalid JSON from %s", endpoint)
	}
	return nil
}

func (s *Server) read(req *http.Request) ([]byte, int, error) {
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, 0, explainTLS(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxOIDCResponse+1))
	if err != nil || len(raw) > maxOIDCResponse {
		return nil, 0, errors.New("response is unreadable or too large")
	}
	return raw, res.StatusCode, nil
}

func explainTLS(err error) error {
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		return fmt.Errorf("%w; the IdP certificate chain cannot be verified: the IdP may not send its intermediate certificates, or its CA is not trusted; put the full chain in OIDC_CA_BUNDLE", err)
	}
	return err
}
