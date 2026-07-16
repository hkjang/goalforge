package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type WorkspaceSnapshot map[string]string

type FileChange struct {
	Path, ChangeType, BeforeHash, AfterHash string
}

// CaptureWorkspace records content hashes without retaining source content.
// Git's internal directory is excluded because provider/session activity must
// not be mistaken for a project file change.
func CaptureWorkspace(ctx context.Context, root string) (WorkspaceSnapshot, error) {
	result := WorkspaceSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			content = []byte("symlink\x00" + target)
		} else if info.Mode().IsRegular() {
			content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		} else {
			return nil
		}
		hash := sha256.Sum256(content)
		result[relative] = fmt.Sprintf("%x", hash[:])
		return nil
	})
	return result, err
}

func ChangedFiles(before, after WorkspaceSnapshot) []FileChange {
	paths := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		paths[path] = struct{}{}
	}
	for path := range after {
		paths[path] = struct{}{}
	}
	changes := make([]FileChange, 0)
	for path := range paths {
		oldHash, existed := before[path]
		newHash, exists := after[path]
		if existed && exists && strings.EqualFold(oldHash, newHash) {
			continue
		}
		changeType := "MODIFIED"
		if !existed {
			changeType = "ADDED"
		} else if !exists {
			changeType = "DELETED"
		}
		changes = append(changes, FileChange{Path: path, ChangeType: changeType, BeforeHash: oldHash, AfterHash: newHash})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}
