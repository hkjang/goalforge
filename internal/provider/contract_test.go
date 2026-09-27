package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
	"github.com/goalforge/goalforge/internal/provider/claude"
	"github.com/goalforge/goalforge/internal/provider/codex"
	"github.com/goalforge/goalforge/internal/provider/opencode"
	"github.com/goalforge/goalforge/internal/provider/qwen"
	"github.com/goalforge/goalforge/internal/testscript"
)

// broadPermissionFlags are the modes that hand a provider unrestricted
// approval. The README states GoalForge deliberately avoids them and relies on
// its own policy-checked verification instead; this test is what makes that a
// property of the code rather than a claim in a document.
var broadPermissionFlags = []string{
	"--yolo",
	"--dangerously-skip-permissions",
	"--dangerously-bypass-approvals-and-sandbox",
	"--full-auto",
	"--auto-approve",
}

var adapterNames = []string{"claude", "codex", "qwen", "opencode"}

func newAdapter(name, binary string) provider.Provider {
	switch name {
	case "claude":
		return claude.New(binary)
	case "codex":
		return codex.New(binary)
	case "qwen":
		return qwen.New(binary)
	case "opencode":
		return opencode.New(binary)
	}
	return nil
}

// launch runs one adapter invocation against a fresh recording binary and
// returns the command line it produced. Each call gets its own record file:
// sharing one across invocations made a read return the previous run's
// arguments, which silently checked the wrong command line.
func launch(t *testing.T, name string, writable bool) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	quoted := strings.ReplaceAll(record, `\`, `\\`)
	binary := testscript.Write(t, dir, name,
		"printf '%s\\n' \"$*\" > '"+record+"'\ncat >/dev/null 2>/dev/null\nexit 0",
		"echo %* > \""+quoted+"\"\nexit /b 0")
	request := provider.RunRequest{RunID: "R1", Prompt: "hi", WorkDir: t.TempDir(), WorkspaceWrite: writable, Model: "small"}
	if _, err := newAdapter(name, binary).Start(context.Background(), request); err != nil {
		t.Fatalf("%s start (writable=%t): %v", name, writable, err)
	}
	// The provider process runs asynchronously; wait for it to record.
	for i := 0; i < 100; i++ {
		if data, err := os.ReadFile(record); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s recorded no command line (writable=%t)", name, writable)
	return ""
}

// No adapter may hand a provider the broadest permission mode, in either
// direction of the read-only/writable split. A single adapter regressing this
// would silently remove the boundary every other guarantee rests on.
func TestAdaptersNeverRequestBroadPermissions(t *testing.T) {
	for _, name := range adapterNames {
		for _, writable := range []bool{false, true} {
			args := launch(t, name, writable)
			for _, flag := range broadPermissionFlags {
				if strings.Contains(args, flag) {
					t.Errorf("%s passed %s (writable=%t): %s", name, flag, writable, args)
				}
			}
		}
	}
}

// Read-only work must be visibly different from writable work on the command
// line. An adapter that sends the same arguments for both is not enforcing the
// distinction discovery and audit runs depend on.
func TestAdaptersDistinguishReadOnlyFromWritable(t *testing.T) {
	for _, name := range adapterNames {
		readOnly, writable := launch(t, name, false), launch(t, name, true)
		if readOnly == writable {
			t.Errorf("%s sends identical arguments for read-only and writable runs: %s", name, readOnly)
		}
	}
}

// The writable mappings documented in the README are the ones actually sent.
// A drift here would mean the table describes a permission model the code no
// longer implements.
func TestAdaptersUseDocumentedPermissionMappings(t *testing.T) {
	for _, tc := range []struct{ name, readOnly, writable string }{
		{"claude", "--permission-mode plan", "--permission-mode acceptEdits"},
		{"codex", "--sandbox read-only", "--sandbox workspace-write"},
		{"qwen", "--approval-mode plan", "--approval-mode auto-edit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if args := launch(t, tc.name, false); !strings.Contains(args, tc.readOnly) {
				t.Errorf("read-only args %q lack %q", args, tc.readOnly)
			}
			if args := launch(t, tc.name, true); !strings.Contains(args, tc.writable) {
				t.Errorf("writable args %q lack %q", args, tc.writable)
			}
		})
	}
}

// Every adapter has to answer the same questions about itself, because the
// orchestrator branches on these and an unset capability reads as "not
// supported" rather than "not declared".
func TestAdaptersDeclareCapabilitiesAndName(t *testing.T) {
	for _, name := range adapterNames {
		adapter := newAdapter(name, name)
		if adapter.Name() != name {
			t.Errorf("adapter reports name %q, registered as %q", adapter.Name(), name)
		}
		if !adapter.Capabilities().StructuredStream {
			t.Errorf("%s does not declare a structured stream; the orchestrator parses one from every provider", name)
		}
	}
}

// Resume without a session is a programming error, not something to send to a
// provider as a fresh run: a resumed turn that silently starts over loses the
// conversation the caller was trying to continue.
func TestAdaptersRefuseResumeWithoutSession(t *testing.T) {
	for _, name := range adapterNames {
		adapter := newAdapter(name, name)
		if _, err := adapter.Resume(context.Background(), "", provider.RunRequest{RunID: "R1", Prompt: "hi", WorkDir: t.TempDir()}); err == nil {
			t.Errorf("%s accepted an empty session ID on Resume", name)
		}
	}
}
