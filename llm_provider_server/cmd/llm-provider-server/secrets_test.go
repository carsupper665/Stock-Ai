package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionSecretRequiresEnvironmentNamespaceFile(t *testing.T) {
	directory := t.TempDir()
	valid := filepath.Join(directory, "stock-ai-test_llm_master_key")
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdefGHIJKLMNOPQRSTUV"))
	if err := os.WriteFile(valid, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOCK_AI_ENVIRONMENT", "Test")
	t.Setenv("LLM_SERVER_MASTER_KEY_FILE", valid)
	value, err := secret("LLM_SERVER_MASTER_KEY", "llm_master_key")
	if err != nil || value != key {
		t.Fatalf("secret = %q, %v", value, err)
	}

	t.Setenv("LLM_SERVER_MASTER_KEY", "direct-secret-must-not-win")
	if _, err := secret("LLM_SERVER_MASTER_KEY", "llm_master_key"); err == nil || !strings.Contains(err.Error(), "cannot both") {
		t.Fatalf("mixed source error = %v", err)
	}
}
