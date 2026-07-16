// Package testscript writes small fake executables for tests so provider
// adapters and verification gates can be exercised on both Unix and Windows.
package testscript

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Write creates an executable script at dir/name and returns its path. posix
// is a /bin/sh body and windows is a cmd.exe body (without the @echo off
// prefix); the variant matching the current platform is written. On Windows
// the file gets a .cmd extension so the OS can execute it directly.
func Write(t testing.TB, dir, name, posix, windows string) string {
	t.Helper()
	var path string
	var body []byte
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, name+".cmd")
		body = []byte("@echo off\r\n" + strings.ReplaceAll(windows, "\n", "\r\n") + "\r\n")
	} else {
		path = filepath.Join(dir, name)
		body = []byte("#!/bin/sh\n" + posix + "\n")
	}
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
