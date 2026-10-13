package test

import (
	"errors"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	ctrlBreakEvent        = 1
)

func processExecutableSuffix() string { return ".exe" }

func prepareSignalProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func sendTerminationSignal(pid int) error {
	generateConsoleCtrlEvent := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")
	result, _, callErr := generateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(pid))
	if result != 0 {
		return nil
	}
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return callErr
	}
	return syscall.EINVAL
}

func terminationSignalUnsupported(err error) bool {
	return errors.Is(err, syscall.Errno(6)) || errors.Is(err, syscall.Errno(5))
}
