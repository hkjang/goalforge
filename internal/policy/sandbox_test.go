package policy

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestSandboxNoneLeavesTheCommandAlone(t *testing.T) {
	policy := SandboxPolicy{Mode: SandboxNone}
	wrapped, err := policy.Wrap("/workspace", []string{"go", "test", "./..."})
	if err != nil || strings.Join(wrapped, " ") != "go test ./..." {
		t.Fatalf("wrapped=%v err=%v", wrapped, err)
	}
}

// A sandbox that cannot run the project's gates is worse than none, so the
// misconfiguration is refused where it is set rather than at the first gate.
func TestSandboxRefusesIncompleteConfiguration(t *testing.T) {
	if err := (SandboxPolicy{Mode: SandboxDocker}).Valid(); err == nil {
		t.Fatal("docker mode without an image must be refused")
	}
	if err := (SandboxPolicy{Mode: "chroot"}).Valid(); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	if err := (SandboxPolicy{Mode: SandboxNone}).Valid(); err != nil {
		t.Fatalf("none is valid: %v", err)
	}
}

// The container gets the workspace and nothing else: no host filesystem, no
// network, no privileges to gain, and ceilings on what it can consume.
func TestSandboxDockerConfinesTheCommand(t *testing.T) {
	policy := SandboxPolicy{Mode: SandboxDocker, Image: "golang:1.23", MemoryMB: 1024, CPUs: 1.5, Processes: 64}
	// The workspace is a real directory on this platform, because Wrap makes
	// it absolute and "/tmp/work" is not absolute on Windows — asserting a
	// POSIX path here made the test a statement about the host rather than
	// about the policy.
	workspace := t.TempDir()
	wrapped, err := policy.Wrap(workspace, []string{"go", "test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(wrapped, " ")
	for _, expected := range []string{
		"docker run --rm --init",
		"--workdir /workspace",
		"--volume " + workspace + ":/workspace:rw",
		"--read-only",
		"--cap-drop ALL",
		"--security-opt no-new-privileges",
		"--network none",
		"--memory 1024m",
		"--cpus 1.5",
		"--pids-limit 64",
		"golang:1.23 go test ./...",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in %q", expected, joined)
		}
	}
	// The workspace is mounted once; a second mount would be a silent
	// contradiction about what is writable.
	if strings.Count(joined, "--volume") != 1 {
		t.Errorf("workspace mounted more than once: %q", joined)
	}
}

// Network access has to be asked for. A verification command that reaches the
// internet can neither be reproduced nor contained.
func TestSandboxNetworkIsOptIn(t *testing.T) {
	off, err := SandboxPolicy{Mode: SandboxDocker, Image: "alpine"}.Wrap("/tmp/work", []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(off, " "), "--network none") {
		t.Error("network must be off by default")
	}
	on, err := SandboxPolicy{Mode: SandboxDocker, Image: "alpine", Network: true}.Wrap("/tmp/work", []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(on, " "), "--network none") {
		t.Error("network must be allowed when a project asks for it")
	}
}

// The identity flag exists to make a bind mount writable under Linux file
// ownership. Where the host has no POSIX identity to copy — Windows reports
// -1 — the flag must be left out rather than passed as "-1:-1", which docker
// refuses, taking the whole sandbox down with it.
func TestContainerUserIsOmittedWithoutAPosixIdentity(t *testing.T) {
	policy := SandboxPolicy{Mode: SandboxDocker, Image: "golang:1.23"}
	wrapped, err := policy.Wrap(t.TempDir(), []string{"go", "build", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(wrapped, " ")
	if strings.Contains(joined, "-1") {
		t.Fatalf("a host with no POSIX identity must not produce a --user value: %q", joined)
	}
	if os.Getuid() >= 0 {
		// On a POSIX host the identity is still copied, or the mounted
		// workspace stops being writable by the user who owns it.
		if !strings.Contains(joined, fmt.Sprintf("--user %d:%d", os.Getuid(), os.Getgid())) {
			t.Fatalf("the host identity must be carried into the container: %q", joined)
		}
	} else if strings.Contains(joined, "--user") {
		t.Fatalf("no identity to carry, so no --user: %q", joined)
	}
}

// An explicitly configured user is honoured on every platform, which is the
// escape hatch for an image that needs a particular account.
func TestExplicitContainerUserIsHonoured(t *testing.T) {
	policy := SandboxPolicy{Mode: SandboxDocker, Image: "golang:1.23", User: "1000:1000"}
	wrapped, err := policy.Wrap(t.TempDir(), []string{"go", "build"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(wrapped, " "), "--user 1000:1000") {
		t.Fatalf("wrapped=%v", wrapped)
	}
}
