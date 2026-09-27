package policy

import (
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
	wrapped, err := policy.Wrap("/tmp/work", []string{"go", "test", "./..."})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(wrapped, " ")
	for _, expected := range []string{
		"docker run --rm --init",
		"--workdir /workspace",
		"--user ",
		"--volume /tmp/work:/workspace:rw",
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
