package policy

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
)

// GateCommand is the part of a verification gate that can live in the
// repository under test.
type GateCommand struct {
	Type    string
	Command []string
}

// VerificationSurface is the set of repository files that decide whether work
// passes: gate scripts that live inside the tree being changed. An
// implementation session that can rewrite these can certify its own work, so
// they are tracked separately from ordinary source.
//
// Gate commands that resolve outside the repository (a system binary such as
// `go` or `npm`) are not part of the surface: the session cannot reach them,
// and treating every gate as in-repo would make the check meaningless noise.
func VerificationSurface(repository string, gates []GateCommand) []string {
	root, err := filepath.Abs(repository)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var surface []string
	for _, gate := range gates {
		for _, argument := range gate.Command {
			relative, ok := insideRepository(root, argument)
			if !ok || seen[relative] {
				continue
			}
			seen[relative] = true
			surface = append(surface, relative)
		}
	}
	sort.Strings(surface)
	return surface
}

// insideRepository reports whether an argument names a file inside the
// repository, returning its repository-relative path.
func insideRepository(root, argument string) (string, bool) {
	if argument == "" || strings.HasPrefix(argument, "-") {
		return "", false
	}
	candidate := argument
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	resolved, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return "", false
	}
	if info, statErr := os.Stat(resolved); statErr != nil || info.IsDir() {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

// SurfaceBaseline is the content of the verification surface before a run,
// kept so the gates that judge the work are the ones that were agreed rather
// than the ones the session just wrote.
type SurfaceBaseline struct {
	Repository string
	Files      map[string][]byte
}

// CaptureSurface reads the current content of every surface file.
func CaptureSurface(repository string, surface []string) (SurfaceBaseline, error) {
	baseline := SurfaceBaseline{Repository: repository, Files: map[string][]byte{}}
	for _, relative := range surface {
		content, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(relative)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return baseline, fmt.Errorf("read verification file %s: %w", relative, err)
		}
		baseline.Files[relative] = content
	}
	return baseline, nil
}

// Changed reports which surface files differ from the baseline, including any
// that were deleted.
func (b SurfaceBaseline) Changed(repository string) ([]string, error) {
	var changed []string
	for relative, original := range b.Files {
		current, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(relative)))
		if err != nil {
			if os.IsNotExist(err) {
				changed = append(changed, relative+" (삭제됨)")
				continue
			}
			return nil, err
		}
		if sha256.Sum256(current) != sha256.Sum256(original) {
			changed = append(changed, relative)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// Restore puts the agreed verification files back. The change is not thrown
// away silently: callers record what was reverted so a deliberate improvement
// to a gate can be re-applied through the normal path instead of arriving
// inside the run it would judge.
func (b SurfaceBaseline) Restore(repository string) error {
	for relative, original := range b.Files {
		path := filepath.Join(repository, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		mode := os.FileMode(0o700)
		if err == nil {
			mode = info.Mode().Perm()
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err = os.WriteFile(path, original, mode); err != nil {
			return fmt.Errorf("restore verification file %s: %w", relative, err)
		}
	}
	return nil
}

// RemovedTests reports existing test files a change deleted or emptied.
// Adding tests is ordinary work; removing the ones that were already there is
// the cheapest way to make a failing gate pass.
func RemovedTests(changes []gitops.FileChange) []string {
	var removed []string
	for _, change := range changes {
		if !IsTestPath(change.Path) {
			continue
		}
		switch {
		case strings.EqualFold(change.ChangeType, "deleted"), strings.EqualFold(change.ChangeType, "removed"):
			removed = append(removed, change.Path)
		}
	}
	sort.Strings(removed)
	return removed
}
