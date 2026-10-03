//go:build windows

package provider

import (
	"errors"
	"syscall"
)

// Windows system error codes for a pipe whose reader has gone.
//
// ERROR_BROKEN_PIPE is "The pipe has been ended." and ERROR_NO_DATA is "The
// pipe is being closed." — both are what a write gets after the process at the
// other end exited. Neither is EPIPE, which is why a classification built on
// the Unix spelling missed them.
const (
	errorBrokenPipe syscall.Errno = 109
	errorNoData     syscall.Errno = 232
)

func isPipeGoneErrno(err error) bool {
	return errors.Is(err, errorBrokenPipe) || errors.Is(err, errorNoData) ||
		errors.Is(err, syscall.EPIPE)
}
