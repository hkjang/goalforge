package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// The module path is what tells a package of this repository apart from one
// that has to be fetched. Read from go.mod rather than guessed, because the
// two look identical in Go's error message.
func TestTheModulePathIsReadFromGoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/notes\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LocalModulePath(dir); got != "example.com/notes" {
		t.Fatalf("got %q", got)
	}
}

// A repository with no go.mod has nothing to say about local paths, and the
// answer must be empty rather than a guess — that is what keeps a non-Go
// project's classification unchanged.
func TestARepositoryWithNoGoModHasNoModulePath(t *testing.T) {
	if got := LocalModulePath(t.TempDir()); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := LocalModulePath(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := LocalModulePath("/definitely/not/here"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// Lines that merely start with the same letters are not the module directive,
// and a quoted path is unquoted. Reading the wrong thing here would classify
// third-party packages as local and send the loop to write them.
func TestOnlyTheModuleDirectiveIsRead(t *testing.T) {
	cases := map[string]string{
		"modulefoo bar\nmodule example.com/real\n":                 "example.com/real",
		"// module example.com/comment\nmodule example.com/real\n": "example.com/real",
		"module \"example.com/quoted\"\n":                          "example.com/quoted",
		"go 1.24\n":                                                "",
		"module\n":                                                 "",
	}
	for content, want := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LocalModulePath(dir); got != want {
			t.Fatalf("%q -> %q, want %q", content, got, want)
		}
	}
}
