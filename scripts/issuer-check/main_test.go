package main

import "testing"

func TestValidateIssuerURL(t *testing.T) {
	tests := []struct {
		name string
		env  string
		url  string
		want bool
	}{
		{name: "stable Test issuer", env: "Test", url: "https://idp.example.test/realms/stock-ai-test", want: true},
		{name: "stable issuer with explicit port", env: "Test", url: "https://idp.example.test:8443/realms/stock-ai-test", want: true},
		{name: "userinfo", env: "Test", url: "https://user@idp.example.test/realms/stock-ai-test"},
		{name: "localhost", env: "Test", url: "https://localhost/realms/stock-ai-test"},
		{name: "localhost subdomain", env: "Test", url: "https://idp.localhost/realms/stock-ai-test"},
		{name: "local domain", env: "Test", url: "https://idp.local/realms/stock-ai-test"},
		{name: "IPv4 loopback", env: "Test", url: "https://127.0.0.1/realms/stock-ai-test"},
		{name: "IPv6 loopback", env: "Test", url: "https://[::1]/realms/stock-ai-test"},
		{name: "IP literal must use stable DNS", env: "Test", url: "https://203.0.113.20/realms/stock-ai-test"},
		{name: "wrong realm", env: "Test", url: "https://idp.example.test/realms/stock-ai-release"},
		{name: "query", env: "Test", url: "https://idp.example.test/realms/stock-ai-test?x=1"},
		{name: "wrong scheme", env: "Test", url: "http://idp.example.test/realms/stock-ai-test"},
		{name: "unknown environment", env: "Development", url: "https://idp.example.test/realms/stock-ai-development"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateIssuerURL(test.env, test.url)
			if got := err == nil; got != test.want {
				t.Fatalf("validateIssuerURL(%q, %q) accepted = %t, want %t (err %v)", test.env, test.url, got, test.want, err)
			}
		})
	}
}

func TestValidateJWKSURLRequiresSameOriginAndRealmPath(t *testing.T) {
	issuer, err := validateIssuerURL("Test", "https://idp.example.test:8443/realms/stock-ai-test")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "same origin under realm", url: "https://idp.example.test:8443/realms/stock-ai-test/certs", want: true},
		{name: "foreign origin", url: "https://keys.example.test:8443/realms/stock-ai-test/certs"},
		{name: "different port", url: "https://idp.example.test/realms/stock-ai-test/certs"},
		{name: "userinfo", url: "https://user@idp.example.test:8443/realms/stock-ai-test/certs"},
		{name: "outside realm", url: "https://idp.example.test:8443/realms/stock-ai-release/certs"},
		{name: "realm prefix without boundary", url: "https://idp.example.test:8443/realms/stock-ai-test-evil/certs"},
		{name: "traversal", url: "https://idp.example.test:8443/realms/stock-ai-test/%2e%2e/other/certs"},
		{name: "wrong scheme", url: "http://idp.example.test:8443/realms/stock-ai-test/certs"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateJWKSURL(issuer, test.url)
			if got := err == nil; got != test.want {
				t.Fatalf("validateJWKSURL(%q) accepted = %t, want %t (err %v)", test.url, got, test.want, err)
			}
		})
	}
}
