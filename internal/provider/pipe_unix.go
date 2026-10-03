//go:build !windows

package provider

import (
	"errors"
	"syscall"
)

// isPipeGoneErrno reports whether the error is the kernel's "nobody is reading
// this pipe". EPIPE is what a write to such a pipe returns.
func isPipeGoneErrno(err error) bool { return errors.Is(err, syscall.EPIPE) }
