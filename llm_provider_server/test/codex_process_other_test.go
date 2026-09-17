//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package test

import (
	"errors"
	"os/exec"
)

func processTreeTestSupported() bool { return false }

func processIsAlive(int) (bool, error) { return false, nil }

func gatewaySignalTestSupported() bool { return false }

func prepareGatewayCommand(*exec.Cmd) {}

func signalGatewayProcess(*exec.Cmd) error { return errors.New("process signals are unsupported") }
