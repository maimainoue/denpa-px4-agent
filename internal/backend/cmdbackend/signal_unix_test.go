//go:build !windows

package cmdbackend

import (
	"os/signal"
	"syscall"
)

func ignoreTerm() { signal.Ignore(syscall.SIGTERM) }
