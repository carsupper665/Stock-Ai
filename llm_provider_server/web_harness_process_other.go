//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package gateway

import (
	"context"
	"os/exec"
)

func runHarnessProcess(ctx context.Context, _ *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrWebHarnessUnavailable
}
