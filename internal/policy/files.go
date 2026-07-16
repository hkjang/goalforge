package policy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ProtectedSnapshot map[string]string

type ProtectedFile struct {
	Content []byte
	Mode    os.FileMode
}

type ProtectedBaseline map[string]ProtectedFile

func CaptureProtectedBaseline(ctx context.Context, repository string) (ProtectedBaseline, error) {
	baseline := make(ProtectedBaseline)
	err := filepath.WalkDir(repository, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".goalforge") {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if entry.IsDir() || !IsProtectedPath(relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("protected path must be a regular file: %s", filepath.ToSlash(relative))
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		baseline[filepath.ToSlash(relative)] = ProtectedFile{Content: content, Mode: info.Mode().Perm()}
		return nil
	})
	return baseline, err
}

func (b ProtectedBaseline) Snapshot() ProtectedSnapshot {
	result := make(ProtectedSnapshot, len(b))
	for path, file := range b {
		hash := sha256.Sum256(file.Content)
		result[path] = fmt.Sprintf("%x", hash[:])
	}
	return result
}

// Restore removes protected files created by an unapproved run and restores
// every pre-run protected file byte-for-byte before verification can proceed.
func (b ProtectedBaseline) Restore(ctx context.Context, repository string) error {
	var current []string
	err := filepath.WalkDir(repository, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".goalforge") {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if !entry.IsDir() && IsProtectedPath(relative) {
			current = append(current, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range current {
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for relative, file := range b {
		path := filepath.Join(repository, filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err = os.WriteFile(path, file.Content, file.Mode); err != nil {
			return err
		}
	}
	return nil
}

func CaptureProtected(ctx context.Context, repository string) (ProtectedSnapshot, error) {
	result := make(ProtectedSnapshot)
	err := filepath.WalkDir(repository, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".goalforge") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !IsProtectedPath(relative) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		result[filepath.ToSlash(relative)] = fmt.Sprintf("%x", hash.Sum(nil))
		return nil
	})
	return result, err
}

func IsProtectedPath(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(normalized)
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "credentials.json" || base == "authorized_keys" || base == "known_hosts" || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	if strings.Contains("/"+normalized, "/.ssh/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks":
		return true
	default:
		return false
	}
}

func ChangedProtected(before, after ProtectedSnapshot) []string {
	changed := make([]string, 0)
	seen := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		seen[path] = struct{}{}
	}
	for path := range after {
		seen[path] = struct{}{}
	}
	for path := range seen {
		if before[path] != after[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}
