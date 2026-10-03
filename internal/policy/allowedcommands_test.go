package policy

import (
	"reflect"
	"testing"
)

// A session running headless cannot be asked to approve anything. Qwen Code
// gates shell commands even with edits auto-approved, so a session that wanted
// to run the project's tests before finishing could not — it wrote code, said
// it was done, and GoalForge found out from the gates afterwards.
//
// The allowlist is derived from the gates the project already declared. That
// grants no authority it did not have: those commands are going to run anyway,
// as the gates. Inventing a list of "safe" commands here would be a security
// decision nobody asked for.
func TestAllowedCommandsComeFromTheGates(t *testing.T) {
	allowed := AllowedCommandsFromGates([][]string{
		{"go", "build", "./..."},
		{"go", "test", "-count=1", "./..."},
	})
	// The exact gate command, so the session can run precisely what will judge
	// it.
	for _, want := range []string{"go build ./...", "go test -count=1 ./..."} {
		if !contains(allowed, want) {
			t.Fatalf("the gate itself must be allowed (%q): %v", want, allowed)
		}
	}
	// And the executable with its subcommand, so the session can run the same
	// kind of check on a narrower target — `go test ./shortener` while working
	// on that package. The subcommand came from the project's own gate; this
	// does not decide that "test" is safe, the project did.
	for _, want := range []string{"go build", "go test"} {
		if !contains(allowed, want) {
			t.Fatalf("the subcommand form must be allowed (%q): %v", want, allowed)
		}
	}
}

// A tool whose first argument is a flag is not shortened. `python -m pytest -q`
// shortened to `python` would permit `python -c "anything"`, which is every
// command there is.
func TestAToolWhoseFirstArgumentIsAFlagIsNotShortened(t *testing.T) {
	allowed := AllowedCommandsFromGates([][]string{{"python", "-m", "pytest", "-q"}})
	if contains(allowed, "python") {
		t.Fatalf("`python` permits python -c, which is anything: %v", allowed)
	}
	if !contains(allowed, "python -m pytest -q") {
		t.Fatalf("the gate itself is still allowed: %v", allowed)
	}
}

// The same rule holds for a tool that is in the table. `make -j4 test` shortened
// to `make -j4` would permit every target in the Makefile, when the project only
// said that one of them judges its work — a flag is not a mode.
func TestAFlagIsNotASubcommandEvenForAListedTool(t *testing.T) {
	allowed := AllowedCommandsFromGates([][]string{{"make", "-j4", "test"}})
	if contains(allowed, "make -j4") {
		t.Fatalf("that permits any target: %v", allowed)
	}
	if contains(allowed, "make") {
		t.Fatalf("and that permits more: %v", allowed)
	}
	if !contains(allowed, "make -j4 test") {
		t.Fatalf("the gate itself is allowed: %v", allowed)
	}
	// With the mode first it does shorten, because then the next words are
	// targets of the same kind of check.
	withMode := AllowedCommandsFromGates([][]string{{"make", "test", "-j4"}})
	if !contains(withMode, "make test") {
		t.Fatalf("allowed=%v", withMode)
	}
}

// Shortening only happens for tools where the first non-flag word selects a
// mode and what follows is paths or filters. A script takes arguments that are
// not a mode, so `./verify.sh` with its argument dropped would permit calling
// it with anything.
func TestAScriptIsNotShortened(t *testing.T) {
	allowed := AllowedCommandsFromGates([][]string{{"./scripts/verify.sh", "--fast"}})
	if !contains(allowed, "./scripts/verify.sh --fast") {
		t.Fatalf("allowed=%v", allowed)
	}
	if contains(allowed, "./scripts/verify.sh") {
		t.Fatalf("its argument is not a subcommand: %v", allowed)
	}
}

// A blocked executable is never allowed, however it got into a gate. A gate
// built on one would already be refused when it ran; allowing it here would
// hand the session a command the policy exists to stop.
func TestABlockedExecutableIsNeverAllowed(t *testing.T) {
	allowed := AllowedCommandsFromGates([][]string{
		{"rm", "-rf", "build"},
		{"go", "test", "./..."},
	})
	for _, entry := range allowed {
		if entry == "rm -rf build" || entry == "rm" {
			t.Fatalf("rm is blocked by the command policy: %v", allowed)
		}
	}
	if !contains(allowed, "go test") {
		t.Fatalf("the usable gate survives: %v", allowed)
	}
}

// The same pattern is not emitted twice, and the order is stable: an allowlist
// that reshuffles between runs makes two identical runs look different.
func TestTheListIsDeduplicatedAndStable(t *testing.T) {
	gates := [][]string{{"go", "test", "./..."}, {"go", "test", "./..."}, {"go", "vet", "./..."}}
	first := AllowedCommandsFromGates(gates)
	second := AllowedCommandsFromGates(gates)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("%v != %v", first, second)
	}
	seen := map[string]int{}
	for _, entry := range first {
		seen[entry]++
		if seen[entry] > 1 {
			t.Fatalf("%q repeated: %v", entry, first)
		}
	}
}

// No gates means no allowlist rather than a default one. A project that
// declared nothing has told us nothing about what it is safe to run.
func TestNoGatesMeansNoAllowlist(t *testing.T) {
	if allowed := AllowedCommandsFromGates(nil); len(allowed) != 0 {
		t.Fatalf("allowed=%v", allowed)
	}
	if allowed := AllowedCommandsFromGates([][]string{{}, {""}}); len(allowed) != 0 {
		t.Fatalf("allowed=%v", allowed)
	}
}

func contains(list []string, want string) bool {
	for _, entry := range list {
		if entry == want {
			return true
		}
	}
	return false
}
