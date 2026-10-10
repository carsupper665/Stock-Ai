package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionSecretRequiresEnvironmentNamespaceFile(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "stock-ai-release_agent_admin_token")
	token := "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_"
	if err := os.WriteFile(valid, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_AI_ENVIRONMENT", "Release")
	t.Setenv("AGENT_SERVER_ADMIN_TOKEN_FILE", valid)
	value, err := secret("AGENT_SERVER_ADMIN_TOKEN", "agent_admin_token")
	if err != nil || value != token {
		t.Fatalf("secret = %q, %v", value, err)
	}

	wrong := filepath.Join(directory, "stock-ai-test_agent_admin_token")
	if err := os.WriteFile(wrong, []byte(strings.Repeat("b", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_SERVER_ADMIN_TOKEN_FILE", wrong)
	if _, err := secret("AGENT_SERVER_ADMIN_TOKEN", "agent_admin_token"); err == nil || !strings.Contains(err.Error(), "stock-ai-release") {
		t.Fatalf("wrong namespace error = %v", err)
	}

	t.Setenv("AGENT_SERVER_ADMIN_TOKEN_FILE", valid)
	if err := os.WriteFile(valid, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secret("AGENT_SERVER_ADMIN_TOKEN", "agent_admin_token"); err == nil || !strings.Contains(err.Error(), "diversity") {
		t.Fatalf("weak value error = %v", err)
	}
}
