package policy

import (
	"path/filepath"
	"strings"
)

// subcommandTools are tools whose first non-flag word selects a mode, so what
// follows it is paths or filters.
//
// It decides only whether a gate command may be shortened to "executable
// subcommand". The subcommand itself always comes from the project's own gate —
// this does not decide that `test` is a safe thing to run, the project did by
// making it the thing that judges its work. Shortening is what lets a session
// run the same kind of check on a narrower target: `go test ./shortener` while
// it is working on that package.
//
// A tool not listed here is allowed only as the exact gate command. A script
// takes arguments that are not a mode, so dropping them would permit calling it
// with anything.
var subcommandTools = map[string]bool{
	"go": true, "cargo": true, "npm": true, "pnpm": true, "yarn": true,
	"dotnet": true, "mvn": true, "gradle": true, "make": true, "bundle": true,
	"composer": true, "swift": true, "dart": true, "flutter": true,
}

// AllowedCommandsFromGates is the set of shell commands a session may run
// without confirmation, derived from the gates the project declared.
//
// A session running headless cannot be asked to approve anything, and Qwen Code
// gates shell commands even with edits auto-approved. So a session that wanted
// to run the project's tests before saying it was finished could not: it wrote
// code, reported done, and the gates found out afterwards.
//
// Derived rather than invented. These commands are going to run anyway as the
// gates, so allowing the session to run them grants no authority it did not
// already have — while a list of commands somebody decided were "safe" would be
// a security decision nobody asked for, in a place nobody would look.
func AllowedCommandsFromGates(commands [][]string) []string {
	var allowed []string
	seen := map[string]bool{}
	add := func(pattern string) {
		if pattern == "" || seen[pattern] {
			return
		}
		seen[pattern] = true
		allowed = append(allowed, pattern)
	}
	for _, command := range commands {
		if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
			continue
		}
		// Refused here as well as where the gate runs. A gate built on a
		// blocked executable is already refused when it executes; allowing it
		// here would hand the session the command the policy exists to stop.
		if err := ValidateCommand(command); err != nil {
			continue
		}
		add(strings.Join(command, " "))
		if len(command) < 2 || strings.HasPrefix(command[1], "-") {
			continue
		}
		if subcommandTools[strings.ToLower(filepath.Base(command[0]))] {
			add(command[0] + " " + command[1])
		}
	}
	return allowed
}
