//go:build !windows

package test

import (
	"os/exec"
	"syscall"
)

func processExecutableSuffix() string { return "" }

func prepareSignalProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func sendTerminationSignal(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func terminationSignalUnsupported(error) bool { return false }
