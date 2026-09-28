package verification

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/policy"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not usable in this environment")
	}
	// These tests are about Linux container confinement — a read-only root,
	// dropped capabilities, no new privileges. A Windows-container daemon
	// supports none of them and the product refuses to pretend otherwise, so
	// there is nothing here to assert rather than something failing.
	engineOS, err := policy.EngineOS(context.Background())
	if err == nil && engineOS != "" && engineOS != "linux" {
		t.Skipf("docker is in %s container mode; the sandbox requires Linux containers", engineOS)
	}
}

func sandboxEngine(t *testing.T, sandbox policy.SandboxPolicy) *Engine {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	engine, err := New(db, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	return engine.WithSandbox(sandbox)
}

// AT-08: a gate that reaches for the host filesystem finds only the workspace.
// Gates run code the session just wrote; confining them is what keeps a
// verification command from being a way out of the workspace.
func TestSandboxedGateCannotSeeTheHostFilesystem(t *testing.T) {
	requireDocker(t)
	workspace := t.TempDir()
	// A file on the host, outside the workspace. The container has its own
	// filesystem, so the meaningful question is whether the host's is
	// reachable — not whether some path exists inside the image.
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "host-only.txt")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := sandboxEngine(t, policy.SandboxPolicy{Mode: policy.SandboxDocker, Image: "alpine:3", MemoryMB: 256, CPUs: 1, Processes: 64})
	results, passed, err := engine.Check(context.Background(), workspace, []Gate{
		{Type: "host_escape", Command: []string{"cat", secret}, Timeout: time.Minute, Required: true, SuccessValue: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if passed || strings.Contains(results[0].Output, "host secret") {
		t.Fatalf("a gate read a host file from outside the workspace: %q", results[0].Output)
	}
	// The workspace itself is present and writable, or the sandbox would be
	// useless rather than safe.
	results, passed, err = engine.Check(context.Background(), workspace, []Gate{
		{Type: "workspace", Command: []string{"touch", "built.txt"}, Timeout: time.Minute, Required: true, SuccessValue: "true"},
	})
	if err != nil || !passed {
		t.Fatalf("the workspace must be usable: %+v err=%v", results, err)
	}
}

// A verification command that reaches the internet can neither be reproduced
// nor contained, so the network is off unless the project asks for it.
func TestSandboxedGateHasNoNetwork(t *testing.T) {
	requireDocker(t)
	engine := sandboxEngine(t, policy.SandboxPolicy{Mode: policy.SandboxDocker, Image: "alpine:3", MemoryMB: 256, CPUs: 1, Processes: 64})
	results, passed, err := engine.Check(context.Background(), t.TempDir(), []Gate{
		{Type: "network", Command: []string{"wget", "-q", "-T", "5", "-O", "-", "http://example.com"}, Timeout: time.Minute, Required: true, SuccessValue: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if passed {
		t.Fatalf("a gate reached the network: %q", results[0].Output)
	}
}

// Memory is a ceiling, and a gate that blows through it is killed rather than
// taking the machine the control plane runs on with it.
func TestSandboxedGateIsBoundedByMemory(t *testing.T) {
	requireDocker(t)
	engine := sandboxEngine(t, policy.SandboxPolicy{Mode: policy.SandboxDocker, Image: "alpine:3", MemoryMB: 32, CPUs: 1, Processes: 64})
	results, passed, err := engine.Check(context.Background(), t.TempDir(), []Gate{
		{Type: "memory", Command: []string{"dd", "if=/dev/zero", "of=/dev/null", "bs=512M", "count=1"}, Timeout: time.Minute, Required: true, SuccessValue: "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if passed {
		t.Fatalf("a gate exceeded its memory ceiling without being stopped: %q", results[0].Output)
	}
	if !strings.Contains(results[0].Status, "FAILED") {
		t.Fatalf("status=%s", results[0].Status)
	}
}
