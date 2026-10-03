package policy

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LocalModulePath is the import path a repository's own packages live under, or
// "" when there is nothing to read it from.
//
// Go-specific and written down as such, the same way the gate templates know
// what a Go project is built with. It exists because Go reports a package
// missing from the module's own tree exactly as it reports one missing from a
// registry, and only the module path tells them apart — one is written here,
// the other fetched from somewhere else.
//
// A repository with no go.mod returns "", which leaves the classification where
// it was: the conservative answer, since telling the loop to write a package it
// cannot write spends a round to reach the same place.
func LocalModulePath(repository string) string {
	if strings.TrimSpace(repository) == "" {
		return ""
	}
	file, err := os.Open(filepath.Join(repository, "go.mod"))
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// Split into words so that "module" has to be one: trimming it as a
		// prefix read "modulefoo bar" as the directive and returned "foo",
		// which would classify somebody else's package as local and send the
		// loop to write it.
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "module" {
			continue
		}
		return strings.Trim(fields[1], `"`)
	}
	return ""
}
