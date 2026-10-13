package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	expectedPath := "/realms/stock-ai-" + strings.ToLower(environment)
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != expectedPath {
		fatal("issuer must be the exact stable HTTPS realm URL for this environment")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var config discovery
	getJSON(client, issuer+"/.well-known/openid-configuration", &config)
	if config.Issuer != issuer {
		fatal("discovery issuer does not exactly match configured issuer")
	}
	jwksURL, err := url.Parse(config.JWKSURI)
	if err != nil || jwksURL.Scheme != "https" || jwksURL.Host == "" || jwksURL.User != nil || jwksURL.Fragment != "" {
		fatal("discovery returned an invalid HTTPS JWKS URL")
	}
	if !strings.HasPrefix(jwksURL.Path, u.Path+"/") {
		fatal("discovery JWKS URL is outside the configured realm")
	}
	var keys jwks
	getJSON(client, config.JWKSURI, &keys)
	if len(keys.Keys) == 0 {
		fatal("JWKS document has no keys")
	}
	fmt.Printf("issuer=%s jwks_host=%s jwks_key_count=%d\n", issuer, jwksURL.Host, len(keys.Keys))
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
