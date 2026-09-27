// Package diagnostics runs the environment checks that otherwise only surface
// mid-run: missing tools, unauthenticated or incompatible provider CLIs, and
// an unregistered project. It returns structured results so the CLI and the
// dashboard's setup flow report the same findings.
package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Levels, ordered by severity. Only FAIL blocks readiness.
const (
	LevelOK   = "OK"
	LevelWarn = "WARN"
	LevelFail = "FAIL"
)

type Check struct {
	Level, Name, Detail string
}

type Report struct {
	Checks []Check
	Failed int
}

func (r *Report) add(level, name, detail string) {
	if level == LevelFail {
		r.Failed++
	}
	r.Checks = append(r.Checks, Check{Level: level, Name: name, Detail: detail})
}

// Ready reports whether nothing blocking was found.
func (r Report) Ready() bool { return r.Failed == 0 }

// Options selects what to check. Providers empty means every supported
// provider, and with no project registered a missing provider CLI is only a
// warning: only the provider a project actually uses can block readiness.
type Options struct {
	Providers []string
	StrictCLI bool
	ProbeAuth bool
	// ProjectNote and ProjectLevel describe the target directory. A path that
	// is not a repository is a blocking finding, not a warning: nothing can
	// run there.
	ProjectNote  string
	ProjectLevel string
}

// RequiredFlags are the CLI flags each adapter passes; a provider binary whose
// help output lacks one would fail on every run, so they are verified up front.
var RequiredFlags = map[string][]string{
	"claude":   {"--output-format", "--resume", "--settings", "--permission-mode", "--json-schema", "--no-session-persistence"},
	"codex":    {"--json", "--sandbox", "--output-schema"},
	"qwen":     {"--output-format", "--resume", "--approval-mode", "--model"},
	"opencode": {"run", "--format", "--session", "--agent", "--model"},
}

var Supported = []string{"codex", "claude", "qwen", "opencode"}

func IsSupported(name string) bool {
	for _, candidate := range Supported {
		if candidate == name {
			return true
		}
	}
	return false
}

// Binary resolves the executable for a provider, honouring the per-provider
// environment override used in tests and custom installs.
func Binary(providerName string) string {
	overrides := map[string]string{"claude": "GOALFORGE_CLAUDE_BIN", "codex": "GOALFORGE_CODEX_BIN", "qwen": "GOALFORGE_QWEN_BIN", "opencode": "GOALFORGE_OPENCODE_BIN"}
	if env, ok := overrides[providerName]; ok {
		if bin := os.Getenv(env); bin != "" {
			return bin
		}
	}
	return providerName
}

func Run(ctx context.Context, options Options) Report {
	var report Report
	if output, err := exec.CommandContext(ctx, "git", "--version").Output(); err != nil {
		report.add(LevelFail, "git", "git is required but not found: "+err.Error())
	} else {
		report.add(LevelOK, "git", strings.TrimSpace(string(output)))
	}
	report.add(LevelOK, "database", "state store opened")
	if options.ProjectNote != "" {
		level := options.ProjectLevel
		if level == "" {
			level = LevelWarn
		}
		report.add(level, "project", options.ProjectNote)
	}
	providers := options.Providers
	if len(providers) == 0 {
		providers = Supported
	}
	missingLevel := LevelWarn
	if options.StrictCLI {
		missingLevel = LevelFail
	}
	for _, name := range providers {
		checkProvider(ctx, &report, name, missingLevel, options.ProbeAuth)
	}
	return report
}

func checkProvider(ctx context.Context, report *Report, name, missingLevel string, probeAuth bool) {
	binary := Binary(name)
	resolved, err := exec.LookPath(binary)
	if err != nil {
		report.add(missingLevel, name+" cli", fmt.Sprintf("%s not found in PATH", binary))
		return
	}
	version := "version unknown"
	if output, versionErr := exec.CommandContext(ctx, resolved, "--version").Output(); versionErr == nil {
		version = strings.TrimSpace(strings.Split(string(output), "\n")[0])
	}
	report.add(LevelOK, name+" cli", resolved+" ("+version+")")
	help, helpErr := exec.CommandContext(ctx, resolved, "--help").CombinedOutput()
	if helpErr != nil {
		report.add(LevelWarn, name+" flags", "could not read CLI help to verify flag support")
	} else {
		var missing []string
		for _, flagName := range RequiredFlags[name] {
			if !strings.Contains(string(help), flagName) {
				missing = append(missing, flagName)
			}
		}
		if len(missing) > 0 {
			report.add(LevelFail, name+" flags", "CLI does not support required flags: "+strings.Join(missing, ", "))
		} else {
			report.add(LevelOK, name+" flags", "all adapter flags supported")
		}
	}
	if !probeAuth || name != "claude" {
		return
	}
	probe := exec.CommandContext(ctx, resolved, "-p", "--output-format", "json", "--model", "haiku")
	probe.Stdin = strings.NewReader("reply with the single word ok")
	output, probeErr := probe.CombinedOutput()
	switch {
	case strings.Contains(string(output), "\"is_error\":true") || strings.Contains(string(output), "401"):
		report.add(LevelFail, name+" auth", "authentication failed; run `claude /login` in a terminal")
	case probeErr != nil:
		report.add(LevelFail, name+" auth", "probe failed: "+probeErr.Error())
	default:
		report.add(LevelOK, name+" auth", "authenticated")
	}
}
