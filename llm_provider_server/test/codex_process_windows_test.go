//go:build windows

package test

import (
	"fmt"
	"os/exec"
	"syscall"
)

var (
	testKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	testOpenProcess              = testKernel32.NewProc("OpenProcess")
	testWaitForSingleObject      = testKernel32.NewProc("WaitForSingleObject")
	testGenerateConsoleCtrlEvent = testKernel32.NewProc("GenerateConsoleCtrlEvent")
)

func processTreeTestSupported() bool { return true }

func gatewaySignalTestSupported() bool { return true }

func prepareGatewayCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

func signalGatewayProcess(command *exec.Cmd) error {
	result, _, callErr := testGenerateConsoleCtrlEvent.Call(1, uintptr(uint32(command.Process.Pid))) // CTRL_BREAK_EVENT
	if result == 0 {
		return fmt.Errorf("signal Gateway process: %w", callErr)
	}
	return nil
}

func processIsAlive(pid int) (bool, error) {
	const (
		synchronize           = 0x00100000
		errorInvalidParameter = syscall.Errno(87)
	)
	handle, _, callErr := testOpenProcess.Call(synchronize, 0, uintptr(uint32(pid)))
	if handle == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorInvalidParameter {
			return false, nil
		}
		return false, fmt.Errorf("open descendant process: %w", callErr)
	}
	defer syscall.CloseHandle(syscall.Handle(handle))
	result, _, callErr := testWaitForSingleObject.Call(handle, 0)
	if result == 0x102 {
		return true, nil
	}
	if result == 0 {
		return false, nil
	}
	return false, fmt.Errorf("query descendant process: %w", callErr)
}
