package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var blockedExecutables = map[string]string{
	"rm": "destructive file deletion", "dd": "raw device write", "mkfs": "filesystem formatting",
	"mkfs.ext4": "filesystem formatting", "shutdown": "host shutdown", "reboot": "host reboot",
	"poweroff": "host shutdown", "curl": "unapproved network access", "wget": "unapproved network access",
	"ssh": "unapproved remote access", "scp": "unapproved remote transfer", "nc": "unapproved network access",
}

func ValidateCommand(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.New("command is required")
	}
	executable := strings.ToLower(filepath.Base(command[0]))
	if reason, blocked := blockedExecutables[executable]; blocked {
		return fmt.Errorf("command %q is blocked: %s", executable, reason)
	}
	if isShell(executable) {
		for _, arg := range command[1:] {
			if arg == "-c" || arg == "--command" {
				return fmt.Errorf("shell command strings are blocked; provide executable and arguments separately")
			}
		}
	}
	if executable == "git" && dangerousGit(command[1:]) {
		return errors.New("destructive Git command is blocked")
	}
	return nil
}

func isShell(executable string) bool {
	switch executable {
	case "sh", "bash", "zsh", "fish", "cmd", "cmd.exe", "powershell", "pwsh":
		return true
	default:
		return false
	}
}

func dangerousGit(args []string) bool {
	joined := strings.ToLower(strings.Join(args, " "))
	return strings.Contains(joined, "reset --hard") || strings.Contains(joined, "clean -f") || strings.Contains(joined, "checkout --") || strings.Contains(joined, "push --force")
}
