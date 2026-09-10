package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEnv 在暫存目錄寫一個 .env，回傳路徑。
func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("寫入 .env: %v", err)
	}
	return path
}

func TestLoadReadsEnvFile(t *testing.T) {
	path := writeEnv(t, strings.Join([]string{
		"# 這行是註解",
		"",
		"USER_TOKEN=secret-token",
		`USER_NAME="Bless"`,
		"DB_DRIVER=sqlite",
		"FEE_RATE_MAKER=0.001",
		"FEE_RATE_TAKER=0.002",
	}, "\n"))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("載入設定: %v", err)
	}
	if cfg.UserToken != "secret-token" {
		t.Fatalf("USER_TOKEN 錯誤: %q", cfg.UserToken)
	}
	if cfg.UserName != "Bless" {
		t.Fatalf("引號沒有被去掉: %q", cfg.UserName)
	}
	if cfg.FeeRateMaker != 0.001 || cfg.FeeRateTaker != 0.002 {
		t.Fatalf("手續費率錯誤: maker=%v taker=%v", cfg.FeeRateMaker, cfg.FeeRateTaker)
	}
	if cfg.Port != "7794" {
		t.Fatalf("PORT 預設值錯誤: %q", cfg.Port)
	}
}

func TestRealEnvBeatsEnvFile(t *testing.T) {
	t.Setenv("USER_TOKEN", "from-environment")
	path := writeEnv(t, "USER_TOKEN=from-file\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("載入設定: %v", err)
	}
	if cfg.UserToken != "from-environment" {
		t.Fatalf(".env 覆蓋了真實環境變數: %q", cfg.UserToken)
	}
}

func TestMissingEnvFileIsNotAnError(t *testing.T) {
	t.Setenv("USER_TOKEN", "token")

	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Fatalf(".env 不存在不該視為錯誤: %v", err)
	}
}

func TestMissingUserTokenIsRejected(t *testing.T) {
	t.Setenv("USER_TOKEN", "")
	path := writeEnv(t, "USER_NAME=Bless\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("USER_TOKEN 未設定時應該報錯")
	}
	if !strings.Contains(err.Error(), "USER_TOKEN") {
		t.Fatalf("錯誤訊息應指出是 USER_TOKEN 的問題: %v", err)
	}
}

func TestUnknownDriverIsRejected(t *testing.T) {
	t.Setenv("USER_TOKEN", "token")
	t.Setenv("DB_DRIVER", "mysql")

	_, err := Load(filepath.Join(t.TempDir(), "none"))
	if err == nil || !strings.Contains(err.Error(), "DB_DRIVER") {
		t.Fatalf("預期拒絕未支援的 driver，得到: %v", err)
	}
}

func TestMalformedLineIsRejected(t *testing.T) {
	t.Setenv("USER_TOKEN", "token")
	path := writeEnv(t, "USER_TOKEN=token\nTHIS_LINE_HAS_NO_EQUALS\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("預期指出第 2 行格式錯誤，得到: %v", err)
	}
}

func TestNonNumericFeeRateIsRejected(t *testing.T) {
	t.Setenv("USER_TOKEN", "token")
	t.Setenv("FEE_RATE_TAKER", "abc")

	_, err := Load(filepath.Join(t.TempDir(), "none"))
	if err == nil || !strings.Contains(err.Error(), "FEE_RATE_TAKER") {
		t.Fatalf("預期拒絕非數字的費率，得到: %v", err)
	}
}
