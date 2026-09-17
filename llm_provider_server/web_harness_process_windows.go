package gateway

import (
	"context"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
)

var (
	harnessKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	harnessCreateJobObject          = harnessKernel32.NewProc("CreateJobObjectW")
	harnessSetInformationJobObject  = harnessKernel32.NewProc("SetInformationJobObject")
	harnessAssignProcessToJobObject = harnessKernel32.NewProc("AssignProcessToJobObject")
	harnessTerminateJobObject       = harnessKernel32.NewProc("TerminateJobObject")
	harnessOpenProcess              = harnessKernel32.NewProc("OpenProcess")
	harnessCreateSnapshot           = harnessKernel32.NewProc("CreateToolhelp32Snapshot")
	harnessThreadFirst              = harnessKernel32.NewProc("Thread32First")
	harnessThreadNext               = harnessKernel32.NewProc("Thread32Next")
	harnessOpenThread               = harnessKernel32.NewProc("OpenThread")
	harnessResumeThread             = harnessKernel32.NewProc("ResumeThread")
)

type harnessJobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type harnessJobIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type harnessJobExtendedLimitInformation struct {
	BasicLimitInformation harnessJobBasicLimitInformation
	IOInfo                harnessJobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func runHarnessProcess(ctx context.Context, command *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	job, err := newHarnessJob()
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(job)
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= 0x00000004 // CREATE_SUSPENDED: contain before any child code can run.
	if err := command.Start(); err != nil {
		return err
	}
	if err := assignHarnessProcess(job, command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	if err := resumeHarnessProcess(command.Process.Pid); err != nil {
		_, _, _ = harnessTerminateJobObject.Call(uintptr(job), 1)
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_, _, _ = harnessTerminateJobObject.Call(uintptr(job), 1)
		_ = command.Process.Kill()
		<-done
		return ctx.Err()
	}
}

func resumeHarnessProcess(pid int) error {
	snapshot, _, callErr := harnessCreateSnapshot.Call(0x00000004, 0) // TH32CS_SNAPTHREAD
	if snapshot == ^uintptr(0) {
		return windowsCallError(callErr)
	}
	defer syscall.CloseHandle(syscall.Handle(snapshot))
	var entry struct {
		Size, Usage, ThreadID, ProcessID uint32
		BasePriority, DeltaPriority      int32
		Flags                            uint32
	}
	entry.Size = uint32(unsafe.Sizeof(entry))
	ok, _, callErr := harnessThreadFirst.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.ProcessID == uint32(pid) {
			thread, _, err := harnessOpenThread.Call(0x0002, 0, uintptr(entry.ThreadID))
			if thread == 0 {
				return windowsCallError(err)
			}
			res, _, err := harnessResumeThread.Call(thread)
			_ = syscall.CloseHandle(syscall.Handle(thread))
			if res == 0xffffffff {
				return windowsCallError(err)
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, callErr = harnessThreadNext.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	}
	return windowsCallError(callErr)
}

func newHarnessJob() (syscall.Handle, error) {
	handle, _, callErr := harnessCreateJobObject.Call(0, 0)
	if handle == 0 {
		return 0, windowsCallError(callErr)
	}
	job := syscall.Handle(handle)
	information := harnessJobExtendedLimitInformation{}
	information.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	result, _, callErr := harnessSetInformationJobObject.Call(
		uintptr(job), jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&information)), unsafe.Sizeof(information),
	)
	if result == 0 {
		syscall.CloseHandle(job)
		return 0, windowsCallError(callErr)
	}
	return job, nil
}

func assignHarnessProcess(job syscall.Handle, pid int) error {
	handle, _, callErr := harnessOpenProcess.Call(processSetQuota|processTerminate, 0, uintptr(uint32(pid)))
	if handle == 0 {
		return windowsCallError(callErr)
	}
	process := syscall.Handle(handle)
	defer syscall.CloseHandle(process)
	result, _, callErr := harnessAssignProcessToJobObject.Call(uintptr(job), uintptr(process))
	if result == 0 {
		return windowsCallError(callErr)
	}
	return nil
}

func windowsCallError(err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno == 0 {
		return syscall.EINVAL
	}
	return err
}
