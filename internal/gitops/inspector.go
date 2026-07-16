package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Snapshot struct {
	CommitSHA, Branch, DirtyFingerprint string
	DirtyFiles                          []string
}
type Inspector interface {
	Snapshot(context.Context, string) (Snapshot, error)
}
type GitInspector struct{ Binary string }

func (g GitInspector) Snapshot(ctx context.Context, repository string) (Snapshot, error) {
	binary := g.Binary
	if binary == "" {
		binary = "git"
	}
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, binary, append([]string{"-C", repository}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	sha, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		if _, repositoryErr := run("rev-parse", "--git-dir"); repositoryErr != nil {
			return Snapshot{}, err
		}
		sha = ""
	}
	branch, err := run("branch", "--show-current")
	if err != nil {
		return Snapshot{}, err
	}
	cmd := exec.CommandContext(ctx, binary, "-C", repository, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	out, err := cmd.Output()
	if err != nil {
		return Snapshot{}, fmt.Errorf("git status: %w", err)
	}
	files, err := parsePorcelainZ(out)
	if err != nil {
		return Snapshot{}, err
	}
	fingerprint, err := fingerprintDirty(repository, files)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{CommitSHA: sha, Branch: branch, DirtyFiles: files, DirtyFingerprint: fingerprint}, nil
}

func parsePorcelainZ(out []byte) ([]string, error) {
	parts := bytes.Split(out, []byte{0})
	files := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		entry := parts[i]
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 4 {
			return nil, errors.New("invalid Git porcelain output")
		}
		status := string(entry[:2])
		path := string(entry[3:])
		if status[0] == 'R' || status[0] == 'C' {
			i++
			if i >= len(parts) || len(parts[i]) == 0 {
				return nil, errors.New("missing rename source path")
			}
		}
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func fingerprintDirty(repository string, files []string) (string, error) {
	hash := sha256.New()
	for _, name := range files {
		_, _ = hash.Write([]byte(name + "\x00"))
		path := filepath.Join(repository, filepath.FromSlash(name))
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			_, _ = hash.Write([]byte("<deleted>\x00"))
			continue
		}
		if err != nil {
			return "", fmt.Errorf("fingerprint dirty file %s: %w", name, err)
		}
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func EqualSnapshot(saved Snapshot, current Snapshot) error {
	if saved.CommitSHA != "" && saved.CommitSHA != current.CommitSHA {
		return fmt.Errorf("commit changed: saved %s current %s", saved.CommitSHA, current.CommitSHA)
	}
	if saved.Branch != "" && saved.Branch != current.Branch {
		return fmt.Errorf("branch changed: saved %s current %s", saved.Branch, current.Branch)
	}
	a, b := append([]string(nil), saved.DirtyFiles...), append([]string(nil), current.DirtyFiles...)
	sort.Strings(a)
	sort.Strings(b)
	if !equalStrings(a, b) {
		return fmt.Errorf("dirty files changed: saved %v current %v", a, b)
	}
	if saved.DirtyFingerprint != "" && saved.DirtyFingerprint != current.DirtyFingerprint {
		return fmt.Errorf("dirty file contents changed")
	}
	return nil
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
