package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	webharness "stock-ai/llm-provider-server/internal/webharness"
)

func TestCodexMissingToolHostIsRejectedBeforeModelInvocation(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "codex")
	if err := os.WriteFile(executable, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", executable)
	_, err := webharness.ResolveHarness("codex", json.RawMessage(`{"auth_mode":"api_key"}`), "fixture-key", false, nil)
	if err == nil || providerErrorCode(err) != "HARNESS_PACKAGE_INCOMPLETE" {
		t.Fatalf("incomplete Codex package must fail before invocation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "codex-package.json"), []byte(`{"layoutVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex-code-mode-host"), []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := webharness.ResolveHarness("codex", json.RawMessage(`{"auth_mode":"api_key"}`), "fixture-key", false, nil); err != nil {
		t.Fatalf("complete package rejected: %v", err)
	}
}

func TestCodexPackageLookupFollowsTheInstalledLauncherSymlink(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "package", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(bin, "codex"), filepath.Join(bin, "codex-code-mode-host"), filepath.Join(root, "package", "codex-package.json")} {
		if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(root, "codex")
	if err := os.Symlink(filepath.Join(bin, "codex"), launcher); err != nil {
		t.Skip("symlink unavailable")
	}
	t.Setenv("LLM_SERVER_CODEX_EXECUTABLE", launcher)
	if _, err := webharness.ResolveHarness("codex", json.RawMessage(`{"auth_mode":"api_key"}`), "fixture-key", false, nil); err != nil {
		t.Fatalf("complete package reached through launcher was rejected: %v", err)
	}
}
