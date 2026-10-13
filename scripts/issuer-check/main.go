package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type discovery struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwks struct {
	Keys []struct {
		Kid string `json:"kid"`
	} `json:"keys"`
}

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "Test" && os.Args[1] != "Release") {
		fatal("usage: issuer-check Test|Release https://issuer.example/realms/stock-ai-test")
	}
	environment := os.Args[1]
	issuer := strings.TrimSuffix(os.Args[2], "/")
	issuerURL, err := validateIssuerURL(environment, issuer)
	if err != nil {
		fatal("issuer must be the exact stable HTTPS realm URL for this environment")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var config discovery
	getJSON(client, issuer+"/.well-known/openid-configuration", &config)
	if config.Issuer != issuer {
		fatal("discovery issuer does not exactly match configured issuer")
	}
	jwksURL, err := validateJWKSURL(issuerURL, config.JWKSURI)
	if err != nil {
		fatal("discovery returned an invalid same-origin HTTPS JWKS URL")
	}
	var keys jwks
	getJSON(client, config.JWKSURI, &keys)
	if len(keys.Keys) == 0 {
		fatal("JWKS document has no keys")
	}
	fmt.Printf("issuer=%s jwks_host=%s jwks_key_count=%d\n", issuer, jwksURL.Host, len(keys.Keys))
}

func validateIssuerURL(environment, raw string) (*url.URL, error) {
	if environment != "Test" && environment != "Release" {
		return nil, fmt.Errorf("unsupported environment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	expectedPath := "/realms/stock-ai-" + strings.ToLower(environment)
	port := u.Port()
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != expectedPath || (port != "" && port != "443") || !safeDNSHost(u.Hostname()) {
		return nil, fmt.Errorf("issuer URL violates stable HTTPS realm policy")
	}
	return u, nil
}

func validateJWKSURL(issuer *url.URL, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" ||
		!sameHTTPSOrigin(issuer, u) || !strings.HasPrefix(u.Path, issuer.Path+"/") ||
		pathHasTraversal(u.Path) {
		return nil, fmt.Errorf("JWKS URL violates same-origin realm policy")
	}
	return u, nil
}

func sameHTTPSOrigin(a, b *url.URL) bool {
	return a.Scheme == "https" && b.Scheme == "https" &&
		strings.EqualFold(a.Hostname(), b.Hostname()) && effectiveHTTPSPort(a) == effectiveHTTPSPort(b)
}

func effectiveHTTPSPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

func safeDNSHost(host string) bool {
	name := strings.TrimSuffix(strings.ToLower(host), ".")
	if name == "" || strings.HasSuffix(name, ".") || !strings.Contains(name, ".") ||
		name == "localhost" || name == "loopback" || strings.HasSuffix(name, ".localhost") ||
		strings.HasSuffix(name, ".loopback") || strings.HasSuffix(name, ".local") {
		return false
	}
	ip := net.ParseIP(strings.SplitN(name, "%", 2)[0])
	return ip == nil
}

func pathHasTraversal(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func getJSON(client *http.Client, endpoint string, target any) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		fatal("invalid HTTPS endpoint")
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		fatal("HTTPS request failed (DNS, route, certificate trust, or remote service)")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fatal("HTTPS endpoint did not return 200")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		fatal("HTTPS endpoint did not return valid JSON")
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
