// Package testscript writes small fake executables for tests so provider
// adapters and verification gates can be exercised on both Unix and Windows.
package testscript

import (
	"os"
	"os/exec"
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

// WriteGo compiles a Go program into an executable at dir/name and returns its
// path.
//
// A fake written twice — once as a shell script, once as a batch file — is two
// programs that drift. One of the batch halves in this repository did nothing
// at all: it exited zero without writing the file or emitting the events its
// POSIX twin produced, so the test that depended on it could not have passed
// on Windows and nobody found out until the checks ran there. A compiled
// program is one program, and it behaves the same everywhere.
//
// Use Write for a fake whose two bodies are genuinely equivalent (`exit 0`);
// use this one as soon as the fake has to do something.
func WriteGo(t testing.TB, dir, name, source string) string {
	t.Helper()
	build := t.TempDir()
	if err := os.WriteFile(filepath.Join(build, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", output, filepath.Join(build, "main.go"))
	cmd.Dir = build
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake %s: %v\n%s", name, err, combined)
	}
	return output
}
