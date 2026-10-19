package test

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"backend/internal/database"
)

func TestBackendProcessGracefullyClosesSQLiteOnPlatformSignal(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "backend"+processExecutableSuffix())
	build := exec.Command("go", "build", "-o", binary, "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build backend: %v\n%s", err, output)
	}

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "backend.db")
	cmd := exec.Command(binary)
	prepareSignalProcess(cmd)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(),
		"USER_TOKEN=backend-process-user-token",
		"ACCOUNT_TOKEN_KEY=",
		"BACKEND_ADDR="+address,
		"DB_DRIVER=sqlite",
		"DB_DSN="+databasePath,
		"LOG_DIR="+filepath.Join(directory, "logs"),
		"MATCHING_INTERVAL=50ms",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start backend: %v", err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if exited {
			return
		}
		_ = cmd.Process.Kill()
		select {
		case <-waitResult:
		case <-time.After(5 * time.Second):
		}
	})

	healthURL := "http://" + address + "/v1/health"
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, requestErr := http.Get(healthURL) // #nosec G107 -- loopback process fixture
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case waitErr := <-waitResult:
			exited = true
			t.Fatalf("backend exited before readiness: %v", waitErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("backend did not become healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := sendTerminationSignal(cmd.Process.Pid); err != nil {
		if terminationSignalUnsupported(err) {
			t.Skipf("platform has no usable console termination signal in this test host: %v", err)
		}
		t.Fatalf("send platform termination signal: %v", err)
	}
	select {
	case err := <-waitResult:
		exited = true
		if err != nil {
			t.Fatalf("backend did not exit cleanly: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("backend did not exit within the shutdown bound")
	}

	store, err := database.OpenSQLite(databasePath)
	if err != nil {
		t.Fatalf("reopen SQLite after graceful stop: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate reopened SQLite after graceful stop: %v", err)
	}
}
