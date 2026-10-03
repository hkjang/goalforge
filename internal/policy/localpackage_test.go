package policy

import "testing"

// Go reports a package missing from the module's own tree the same way it
// reports one missing from the registry:
//
//	main.go:11:2: no required module provides package example.com/notes/store;
//	to add it: go get example.com/notes/store
//
// `example.com/notes/store` is a local package of `module example.com/notes`.
// Classified as a dependency problem it told the operator to check the lockfile
// and registry access — and RepairEnvironment means the loop will not retry, so
// the project blocks for a human on something no registry action can fix.
//
// What it actually means is that the package has not been written yet, or that
// the work which wrote it has not reached this tree.
func TestAMissingLocalPackageIsNotARegistryProblem(t *testing.T) {
	output := "main.go:11:2: no required module provides package example.com/notes/store; " +
		"to add it:\n\tgo get example.com/notes/store\n"
	failure := ClassifyGateFailureIn("FAILED", output, "example.com/notes")
	if failure.Mode == RepairEnvironment {
		t.Fatalf("no registry action fixes a package of this module: %+v", failure)
	}
	// The kind, not only the mode: it is what gets stored, and
	// ClassifyGateFailureSummary reads the explanation back from it. A kind
	// that did not match the summary would explain a different failure to
	// whoever read the record later.
	if failure.Kind != GateMissingLocalPackage {
		t.Fatalf("kind=%s", failure.Kind)
	}
	if failure.Summary == "" {
		t.Fatal("the reader has to be told what to do")
	}
	if ClassifyGateFailureSummary(string(failure.Kind)) != failure.Summary {
		t.Fatalf("the stored kind must read back the same explanation: %q vs %q",
			ClassifyGateFailureSummary(string(failure.Kind)), failure.Summary)
	}
}

// A package from somewhere else is still a dependency problem: fetching it is
// exactly what failed, and no amount of writing code in this repository
// supplies it.
func TestAMissingThirdPartyPackageIsStillADependencyProblem(t *testing.T) {
	for _, output := range []string{
		"main.go:5:2: no required module provides package github.com/pkg/errors; to add it:\n\tgo get github.com/pkg/errors\n",
		"go: downloading golang.org/x/sys v0.1.0\ngo: golang.org/x/sys@v0.1.0: Get \"https://proxy.golang.org/...\": dial tcp: lookup proxy.golang.org: no such host\n",
		"npm ERR! 404 Not Found - GET https://registry.npmjs.org/left-pad\n",
	} {
		failure := ClassifyGateFailureIn("FAILED", output, "example.com/notes")
		if failure.Mode != RepairEnvironment {
			t.Fatalf("this needs the registry, not code: %q -> %+v", output[:40], failure)
		}
	}
}

// A directory that is not there is the same story told by the test runner:
// `go test ./store` on a tree without store/ says the package is missing, and
// the remedy is the work that creates it.
func TestAMissingPackageDirectoryIsNotAnEnvironmentProblem(t *testing.T) {
	output := "# ./store\nstat /tmp/wt/IDEA-1/store: directory not found\nFAIL\t./store [setup failed]\nFAIL\n"
	failure := ClassifyGateFailure("FAILED", output)
	if failure.Mode == RepairEnvironment {
		t.Fatalf("no tool or network is broken here: %+v", failure)
	}
}

// A genuinely absent binary is still an environment problem. The two look alike
// in the word "not found" and need opposite responses.
func TestAMissingExecutableIsStillAnEnvironmentProblem(t *testing.T) {
	for _, output := range []string{
		"exec: \"golangci-lint\": executable file not found in $PATH\n",
		"/bin/sh: 1: pytest: command not found\n",
	} {
		failure := ClassifyGateFailure("FAILED", output)
		if failure.Mode != RepairEnvironment {
			t.Fatalf("the tool is missing: %q -> %+v", output[:30], failure)
		}
	}
}

// Without a module path there is nothing to compare against, so a missing
// package stays a dependency problem — the conservative answer, since the
// alternative is telling the loop to write a package it cannot write.
func TestWithNoModulePathAMissingPackageStaysADependencyProblem(t *testing.T) {
	output := "main.go:11:2: no required module provides package example.com/notes/store; to add it:\n\tgo get example.com/notes/store\n"
	failure := ClassifyGateFailureIn("FAILED", output, "")
	if failure.Mode != RepairEnvironment {
		t.Fatalf("nothing said this path is local: %+v", failure)
	}
	// And the plain entry point keeps its old answer, so callers that have no
	// module path are unchanged.
	if plain := ClassifyGateFailure("FAILED", output); plain.Kind != failure.Kind {
		t.Fatalf("plain=%+v in=%+v", plain, failure)
	}
}

// The module path itself, imported directly, is local too.
func TestTheModulePathItselfIsLocal(t *testing.T) {
	output := "main.go:3:8: no required module provides package example.com/notes; to add it:\n\tgo get example.com/notes\n"
	if failure := ClassifyGateFailureIn("FAILED", output, "example.com/notes"); failure.Mode == RepairEnvironment {
		t.Fatalf("that is this module: %+v", failure)
	}
}

// A path that merely begins with the same characters is not inside the module.
// example.com/notesmith is somebody else's.
func TestAPathThatOnlySharesAPrefixIsNotLocal(t *testing.T) {
	output := "main.go:3:8: no required module provides package example.com/notesmith/x; to add it:\n\tgo get example.com/notesmith/x\n"
	if failure := ClassifyGateFailureIn("FAILED", output, "example.com/notes"); failure.Mode != RepairEnvironment {
		t.Fatalf("notesmith is not notes: %+v", failure)
	}
}

// A truncated line must not take the classifier down with it. Gate output is
// whatever a tool printed, including a line cut off mid-sentence by an output
// limit — and the classifier runs on every failure, so a panic here turns a
// failed gate into a crashed run.
func TestATruncatedMissingPackageLineIsSurvivable(t *testing.T) {
	for _, output := range []string{
		"main.go:11:2: no required module provides package ",
		"main.go:11:2: no required module provides package",
		"no required module provides package \n\tgo get",
	} {
		failure := ClassifyGateFailureIn("FAILED", output, "example.com/notes")
		if failure.Kind == "" {
			t.Fatalf("something has to be returned: %q", output)
		}
		if failure.Kind == GateMissingLocalPackage {
			t.Fatalf("no path was named, so nothing says it is local: %q", output)
		}
	}
}
