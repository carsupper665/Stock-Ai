package test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentServerProcessShutsDownOnPlatformSignal(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agent-server"+processExecutableSuffix())
	build := exec.Command("go", "build", "-o", binary, "../cmd/agent-server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent server: %v\n%s", err, output)
	}

	cmd := exec.Command(binary)
	prepareSignalProcess(cmd)
	cmd.Env = append(os.Environ(),
		"AGENT_SERVER_ADDR=127.0.0.1:0",
		"AGENT_SERVER_DB_PATH="+filepath.Join(t.TempDir(), "agent.db"),
		"AGENT_SERVER_ADMIN_TOKEN=process-agent-admin",
		"AGENT_SERVER_BACKEND_URL=http://127.0.0.1:1",
		"AGENT_SERVER_BACKEND_USER_TOKEN=process-backend-user",
		"AGENT_SERVER_GATEWAY_URL=http://127.0.0.1:1",
		"AGENT_SERVER_GATEWAY_RUNTIME_TOKEN=process-gateway-runtime",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent server: %v", err)
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

	ready := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "ready: listening on ") {
				select {
				case <-ready:
				default:
					close(ready)
				}
			}
		}
	}()
	select {
	case <-ready:
	case err := <-waitResult:
		exited = true
		t.Fatalf("agent server exited before readiness: %v; stderr=%s", err, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("agent server did not report readiness")
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
			t.Fatalf("agent server did not exit cleanly: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent server did not exit within the shutdown bound")
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("agent server output pipe did not close")
	}
}
