package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ServiceUnit describes a systemd unit for the GoalForge worker.
//
// The three things people get wrong when writing one by hand are the binary
// path, the state database path, and the working directory — a worker started
// from the wrong directory registers nothing and drains an empty queue while
// looking healthy. Generating the unit from the running process's own view
// removes all three.
type ServiceUnit struct {
	Binary      string
	StateDB     string
	WorkingDir  string
	User, Group string
	// Environment is passed to the worker process. Provider credentials belong
	// here; what the worker hands to a session is filtered separately.
	Environment map[string]string
	// Scope is "system" or "user".
	Scope string
}

// SecretsForService are the variables the worker needs and a session must
// never receive. They are named here so the generated unit can say where to
// put them: a systemd unit is world-readable, so a token written into it is a
// token published to every account on the machine.
var SecretsForService = []string{
	"GOALFORGE_AUDIT_KEY", "GOALFORGE_API_TOKEN", "GOALFORGE_MCP_TOKEN", "GOALFORGE_POSTGRES_DSN",
}

// Render produces the unit file.
func (u ServiceUnit) Render() (string, error) {
	if strings.TrimSpace(u.Binary) == "" {
		return "", fmt.Errorf("이 유닛이 실행할 goalforge 실행 파일 경로가 필요합니다")
	}
	if strings.TrimSpace(u.WorkingDir) == "" {
		return "", fmt.Errorf("작업 디렉터리가 필요합니다. 워커는 등록된 프로젝트의 저장소에서 실행되어야 합니다")
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=GoalForge worker\n")
	b.WriteString("Documentation=https://github.com/hkjang/goalforge\n")
	// The worker talks to provider CLIs and possibly PostgreSQL, so it waits
	// for the network rather than failing its first job at boot.
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")

	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + u.Binary + " worker\n")
	b.WriteString("WorkingDirectory=" + u.WorkingDir + "\n")
	if u.StateDB != "" {
		b.WriteString("Environment=GOALFORGE_DB=" + u.StateDB + "\n")
	}
	for _, key := range sortedKeys(u.Environment) {
		b.WriteString("Environment=" + key + "=" + u.Environment[key] + "\n")
	}
	// Secrets go in a file systemd reads, not in the unit: units are
	// world-readable and a token written into one is published to every
	// account on the machine.
	b.WriteString("EnvironmentFile=-" + u.credentialsPath() + "\n")
	// A worker that dies mid-goal should come back; the lease it held expires
	// and the work is picked up again rather than stranded.
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=10\n")
	// Give a running session time to finish rather than killing it partway and
	// leaving a half-applied change nobody recorded.
	b.WriteString("TimeoutStopSec=300\n")
	b.WriteString("KillSignal=SIGTERM\n")
	if u.Scope != "user" {
		if u.User != "" {
			b.WriteString("User=" + u.User + "\n")
		}
		if u.Group != "" {
			b.WriteString("Group=" + u.Group + "\n")
		}
		// Hardening that does not get in the worker's way: it needs to write
		// the repository and the state database and to run provider CLIs, so
		// the restrictions stop short of ProtectHome and ReadOnlyPaths.
		b.WriteString("NoNewPrivileges=yes\n")
		b.WriteString("PrivateTmp=yes\n")
		b.WriteString("ProtectSystem=full\n")
		b.WriteString("ProtectControlGroups=yes\n")
		b.WriteString("ProtectKernelModules=yes\n")
	}
	b.WriteString("\n[Install]\n")
	if u.Scope == "user" {
		b.WriteString("WantedBy=default.target\n")
	} else {
		b.WriteString("WantedBy=multi-user.target\n")
	}
	return b.String(), nil
}

func (u ServiceUnit) credentialsPath() string {
	if u.Scope == "user" {
		return filepath.Join(u.WorkingDir, ".goalforge", "worker.env")
	}
	return "/etc/goalforge/worker.env"
}

// InstallNotes tells the operator exactly what to do with the rendered unit,
// including where the secrets go and why they are not in the unit itself.
func (u ServiceUnit) InstallNotes() []string {
	unitPath, reload, enable := "/etc/systemd/system/goalforge-worker.service",
		"sudo systemctl daemon-reload", "sudo systemctl enable --now goalforge-worker"
	if u.Scope == "user" {
		unitPath = "~/.config/systemd/user/goalforge-worker.service"
		reload, enable = "systemctl --user daemon-reload", "systemctl --user enable --now goalforge-worker"
	}
	return []string{
		"1. 위 내용을 " + unitPath + " 에 저장하세요",
		"2. 비밀값은 " + u.credentialsPath() + " 에 KEY=VALUE 로 적고 chmod 600 하세요 " +
			"(" + strings.Join(SecretsForService, ", ") + "). 유닛 파일은 누구나 읽을 수 있으므로 여기에 적으면 안 됩니다",
		"3. " + reload,
		"4. " + enable,
		"5. journalctl -u goalforge-worker -f 로 확인하세요",
	}
}

// DefaultServiceUnit fills the unit from the running process's own view, which
// is the view that is actually correct.
func DefaultServiceUnit(scope string) (ServiceUnit, error) {
	binary, err := os.Executable()
	if err != nil {
		return ServiceUnit{}, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(binary); resolveErr == nil {
		binary = resolved
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return ServiceUnit{}, err
	}
	unit := ServiceUnit{Binary: binary, WorkingDir: workingDir, Scope: scope, Environment: map[string]string{}}
	if db := strings.TrimSpace(os.Getenv("GOALFORGE_DB")); db != "" {
		absolute, absErr := filepath.Abs(db)
		if absErr != nil {
			return unit, absErr
		}
		unit.StateDB = absolute
	}
	return unit, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
