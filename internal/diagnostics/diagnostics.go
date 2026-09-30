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
	"time"

	"github.com/goalforge/goalforge/internal/browser"
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

// helpPages are the help screens to search for each provider, in order. These
// CLIs are subcommand-based and document a subcommand's flags only on that
// subcommand's page: codex's --json and --output-schema live behind
// `codex exec --help`, opencode's --format behind `opencode run --help`.
// Reading only the top-level page reported working installations as broken.
var helpPages = map[string][][]string{
	"claude":   {{"--help"}},
	"codex":    {{"--help"}, {"exec", "--help"}},
	"qwen":     {{"--help"}},
	"opencode": {{"--help"}, {"run", "--help"}},
}

// helpPagesFor returns the pages to search, defaulting to the top-level help
// for a provider that has not named any.
func helpPagesFor(name string) [][]string {
	if pages, ok := helpPages[name]; ok {
		return pages
	}
	return [][]string{{"--help"}}
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
	checkBrowserService(ctx, &report)
	return report
}

// checkBrowserService reports whether the browser service can be reached.
//
// Without it a project whose journey criteria all sit at UNKNOWN gives an
// operator nothing to go on: the board looks the same whether nobody has
// written the scripts yet or one container is down. It is a warning rather
// than a failure — a project with no browser criteria does not need the
// service, and blocking those projects on a container they never use would
// teach people to ignore the diagnostic.
func checkBrowserService(ctx context.Context, report *Report) {
	base := strings.TrimSpace(os.Getenv("GOALFORGE_BROWSER_URL"))
	if base == "" {
		report.add(LevelOK, "browser service",
			"설정되지 않았습니다 — 브라우저 여정 기준을 쓰려면 GOALFORGE_BROWSER_URL 에 playwright-player 주소를 넣으세요")
		return
	}
	health, err := browser.Client{BaseURL: base, Timeout: 10 * time.Second}.Health(ctx)
	if err != nil {
		report.add(LevelWarn, "browser service", err.Error())
		return
	}
	report.add(LevelOK, "browser service",
		fmt.Sprintf("%s %s (%s) — 스크립트 %d개", health.Service, health.Version, base, health.ScriptCount))
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
	checkFlags(ctx, report, name, resolved)
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

// checkFlags verifies the flags an adapter passes against the CLI's own help,
// reading every page the adapter's commands live on.
//
// Absence from the help text is deliberately not treated as proof of absence.
// qwen accepts --approval-mode (plan, default, auto-edit, auto, yolo) and
// documents it on no page at all; calling that "unsupported" told users with a
// working install that three of four providers were unusable. A flag that
// cannot be found is reported as unverified, naming the pages that were read,
// so the finding says what is actually known.
func checkFlags(ctx context.Context, report *Report, name, resolved string) {
	pages := helpPagesFor(name)
	var text strings.Builder
	var read []string
	for _, page := range pages {
		output, err := exec.CommandContext(ctx, resolved, page...).CombinedOutput()
		if err != nil && len(output) == 0 {
			continue
		}
		text.Write(output)
		text.WriteString("\n")
		read = append(read, describeHelpPage(name, page))
	}
	if len(read) == 0 {
		report.add(LevelWarn, name+" flags", "could not read CLI help to verify flag support")
		return
	}
	var missing []string
	for _, flagName := range RequiredFlags[name] {
		if !strings.Contains(text.String(), flagName) {
			missing = append(missing, flagName)
		}
	}
	if len(missing) == 0 {
		report.add(LevelOK, name+" flags", "all adapter flags supported")
		return
	}
	report.add(LevelWarn, name+" flags", fmt.Sprintf(
		"도움말에서 확인하지 못한 플래그: %s (읽은 도움말: %s). 도움말에 없지만 동작하는 플래그도 있으므로 차단하지 않습니다 — 실행이 이 플래그로 실패하면 CLI 버전을 확인하세요",
		strings.Join(missing, ", "), strings.Join(read, ", ")))
}

// describeHelpPage names a help page the way the user would run it, so the
// finding can be reproduced by hand.
func describeHelpPage(name string, page []string) string {
	return strings.TrimSpace(name + " " + strings.Join(page, " "))
}
