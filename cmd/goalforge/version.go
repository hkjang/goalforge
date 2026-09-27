package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version, commit, and buildDate are set at link time by the release build.
// They default to values that say "this was not built by a release" rather than
// to a number that would be wrong, because a binary that misreports itself is
// worse than one that admits it does not know.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// versionInfo is what a downloaded binary can say about itself without
// touching a state database: which build this is, what it was built from, and
// on which platform. A release artifact that cannot be identified cannot be
// matched to a bug report.
func versionInfo() string {
	revision, modified := buildRevision()
	if commit == "" {
		commit = revision
	}
	line := "goalforge " + version
	if commit != "" {
		short := commit
		if len(short) > 12 {
			short = short[:12]
		}
		line += " (" + short
		if modified {
			line += ", 작업 트리 변경 포함"
		}
		line += ")"
	}
	if buildDate != "" {
		line += " built " + buildDate
	}
	return fmt.Sprintf("%s\n%s %s/%s\n", line, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// buildRevision reads the commit Go stamps into the binary, so a `go install`
// build identifies itself even though it never passed through the release
// script.
func buildRevision() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return revision, modified
}
