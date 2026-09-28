package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sandbox modes.
const (
	// SandboxNone runs verification on the host. It is the default because a
	// sandbox that cannot run the project's toolchain is worse than none, and
	// which image can is something only the project knows.
	SandboxNone = "none"
	// SandboxDocker runs verification in a container with the workspace
	// mounted and nothing else reachable.
	SandboxDocker = "docker"
)

// SandboxPolicy bounds what a verification command may consume and reach.
// Gates run code the session just wrote, so they are not more trusted than the
// session: an unbounded gate can exhaust the machine the control plane runs on.
type SandboxPolicy struct {
	Mode  string
	Image string
	// MemoryMB, CPUs, and Processes are ceilings, not reservations. Zero means
	// the runtime default rather than unlimited.
	MemoryMB  int
	CPUs      float64
	Processes int
	// Network is off unless a project says otherwise: a verification command
	// that reaches the internet can neither be reproduced nor contained.
	Network bool
	// User is the identity inside the container, defaulting to the invoking
	// user. Running as root there would either need capabilities back to
	// write the mounted workspace, or leave files the operator cannot delete.
	User string
}

// containerUser keeps the identity inside the container the same as outside,
// so the mounted workspace is writable without handing back the capability
// that lets root ignore file permissions.
//
// It returns an empty string where the host has no POSIX identity to copy.
// On Windows and Plan 9 os.Getuid reports -1, and this used to hand docker
// "--user -1:-1", which it refuses — the flag exists to make a bind mount
// writable under Linux's file ownership, and Docker Desktop's file sharing
// does not work that way, so there is nothing to translate.
func (p SandboxPolicy) containerUser() string {
	if trimmed := strings.TrimSpace(p.User); trimmed != "" {
		return trimmed
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

func DefaultSandboxPolicy() SandboxPolicy {
	return SandboxPolicy{Mode: SandboxNone, MemoryMB: 2048, CPUs: 2, Processes: 256}
}

// Valid reports whether the policy is usable, so a misconfiguration is refused
// at the point it is set rather than at the first gate that runs.
func (p SandboxPolicy) Valid() error {
	switch p.Mode {
	case "", SandboxNone:
		return nil
	case SandboxDocker:
		if strings.TrimSpace(p.Image) == "" {
			return fmt.Errorf("sandbox mode %s needs an image that can run this project's gates", SandboxDocker)
		}
		return nil
	default:
		return fmt.Errorf("unknown sandbox mode %q", p.Mode)
	}
}

// Wrap turns a gate command into the command that actually runs it. In none
// mode the command is unchanged; in docker mode it runs in a container with
// the workspace mounted, no host environment, and no network unless allowed.
func (p SandboxPolicy) Wrap(workspace string, command []string) ([]string, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("command is required")
	}
	if p.Mode != SandboxDocker {
		return command, nil
	}
	if err := p.Valid(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	args := []string{"docker", "run", "--rm", "--init", "--workdir", "/workspace"}
	if user := p.containerUser(); user != "" {
		args = append(args, "--user", user)
	}
	args = append(args,
		// The workspace is writable so a gate can build; the container root is
		// not, so an escalation inside it cannot become a change to the host.
		"--volume", absolute+":/workspace:rw",
		"--read-only", "--tmpfs", "/tmp:rw,exec,size=512m",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
	)
	if !p.Network {
		args = append(args, "--network", "none")
	}
	if p.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(p.MemoryMB)+"m")
	}
	if p.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(p.CPUs, 'f', -1, 64))
	}
	if p.Processes > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(p.Processes))
	}
	args = append(args, p.Image)
	return append(args, command...), nil
}
