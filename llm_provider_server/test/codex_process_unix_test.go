//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package test

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func processTreeTestSupported() bool { return true }

func gatewaySignalTestSupported() bool { return true }

func prepareGatewayCommand(*exec.Cmd) {}

func signalGatewayProcess(command *exec.Cmd) error { return command.Process.Signal(os.Interrupt) }

func processIsAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return err == nil, err
}
