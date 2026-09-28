package diagnostics

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/testscript"
)

// fakeCLI writes a provider stub whose top-level help lists one set of flags
// and whose subcommand help lists another, which is how the real codex and
// opencode CLIs behave.
func fakeCLI(t *testing.T, dir, name, topLevel, subcommand, sub string) string {
	t.Helper()
	posix := `if [ "$1" = "--version" ]; then echo 1.0.0; exit 0; fi
if [ "$1" = "` + sub + `" ]; then echo "` + subcommand + `"; exit 0; fi
echo "` + topLevel + `"
exit 0`
	windows := `if "%1"=="--version" (echo 1.0.0& exit /b 0)
if "%1"=="` + sub + `" (echo ` + subcommand + `& exit /b 0)
echo ` + topLevel + `
exit /b 0`
	return testscript.Write(t, dir, name, posix, windows)
}

func findCheck(report Report, name string) (Check, bool) {
	for _, check := range report.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return Check{}, false
}

// The defect this replaces: codex documents --json and --output-schema only
// under `codex exec --help`, so reading the top-level page alone reported a
// working install as blocked. A user's first act with a downloaded binary is
// running doctor, and it told them three of four providers were unusable.
func TestFlagsAreFoundOnSubcommandHelpPages(t *testing.T) {
	dir := t.TempDir()
	fakeCLI(t, dir, "codex", "Usage: codex [OPTIONS] --sandbox", "Usage: codex exec --json --output-schema", "exec")
	t.Setenv("GOALFORGE_CODEX_BIN", dir+string(os.PathSeparator)+"codex")
	report := Run(context.Background(), Options{Providers: []string{"codex"}})
	check, ok := findCheck(report, "codex flags")
	if !ok {
		t.Fatalf("no flag check ran: %+v", report.Checks)
	}
	if check.Level != LevelOK {
		t.Fatalf("flags documented on the subcommand page are supported: %+v", check)
	}
}

// Reading only the top-level page is what produced the false report, so the
// test pins that the subcommand page is actually consulted.
func TestTopLevelHelpAloneIsNotEnough(t *testing.T) {
	dir := t.TempDir()
	// A CLI that answers every invocation with the top-level page only.
	testscript.Write(t, dir, "codex", `if [ "$1" = "--version" ]; then echo 1.0.0; exit 0; fi
echo "Usage: codex [OPTIONS] --sandbox"
exit 0`, `if "%1"=="--version" (echo 1.0.0& exit /b 0)
echo Usage: codex [OPTIONS] --sandbox
exit /b 0`)
	t.Setenv("GOALFORGE_CODEX_BIN", dir+string(os.PathSeparator)+"codex")
	report := Run(context.Background(), Options{Providers: []string{"codex"}})
	check, _ := findCheck(report, "codex flags")
	if check.Level != LevelWarn {
		t.Fatalf("flags found nowhere are unverified, not proven absent: %+v", check)
	}
	for _, flagName := range []string{"--json", "--output-schema"} {
		if !strings.Contains(check.Detail, flagName) {
			t.Fatalf("the finding must name the flag it could not find: %q", check.Detail)
		}
	}
	if !strings.Contains(check.Detail, "codex exec --help") {
		t.Fatalf("the finding must name the help pages it read so it can be reproduced: %q", check.Detail)
	}
}

// A flag that is absent from every help page must not be called unsupported.
// qwen accepts --approval-mode and documents it nowhere; blocking on that made
// doctor refuse a working installation.
func TestUndocumentedFlagIsUnverifiedNotUnsupported(t *testing.T) {
	dir := t.TempDir()
	testscript.Write(t, dir, "qwen", `if [ "$1" = "--version" ]; then echo 0.21.9; exit 0; fi
echo "Usage: qwen --output-format --resume --model"
exit 0`, `if "%1"=="--version" (echo 0.21.9& exit /b 0)
echo Usage: qwen --output-format --resume --model
exit /b 0`)
	t.Setenv("GOALFORGE_QWEN_BIN", dir+string(os.PathSeparator)+"qwen")
	report := Run(context.Background(), Options{Providers: []string{"qwen"}})
	check, _ := findCheck(report, "qwen flags")
	if check.Level == LevelFail {
		t.Fatalf("an undocumented flag must not block a working install: %+v", check)
	}
	if check.Level != LevelWarn || !strings.Contains(check.Detail, "--approval-mode") {
		t.Fatalf("the unverified flag must still be reported: %+v", check)
	}
}

// A CLI that cannot be run at all is a different finding from one whose help
// does not mention a flag, and must stay distinguishable.
func TestUnreadableHelpIsReportedSeparately(t *testing.T) {
	dir := t.TempDir()
	testscript.Write(t, dir, "codex", `if [ "$1" = "--version" ]; then echo 1.0.0; exit 0; fi
exit 3`, `if "%1"=="--version" (echo 1.0.0& exit /b 0)
exit /b 3`)
	t.Setenv("GOALFORGE_CODEX_BIN", dir+string(os.PathSeparator)+"codex")
	report := Run(context.Background(), Options{Providers: []string{"codex"}})
	check, _ := findCheck(report, "codex flags")
	if check.Level != LevelWarn || !strings.Contains(check.Detail, "could not read CLI help") {
		t.Fatalf("a CLI whose help cannot be read is its own finding: %+v", check)
	}
}
