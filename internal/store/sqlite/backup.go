package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// BackupManifest describes what a backup contains, so a restore can say
// whether the records it brought back are the ones that were taken rather than
// leaving that to be assumed.
type BackupManifest struct {
	TakenAt   time.Time
	Digest    string
	SizeBytes int64
	Projects  int
	Runs      int
	Effects   int
	Approvals int
	Evidence  int
}

// Backup writes a consistent copy of the state database. SQLite's own backup
// API is used rather than copying the file, because a copy taken while a
// worker is writing is a copy of a half-applied transaction.
func (s *Store) Backup(ctx context.Context, path string) (BackupManifest, error) {
	var manifest BackupManifest
	if path == "" {
		return manifest, errors.New("backup path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return manifest, err
	}
	// VACUUM INTO produces a single consistent file without blocking readers.
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return manifest, fmt.Errorf("write backup: %w", err)
	}
	manifest.TakenAt = time.Now().UTC()
	info, err := os.Stat(path)
	if err != nil {
		return manifest, err
	}
	manifest.SizeBytes = info.Size()
	if manifest.Digest, err = fileDigest(path); err != nil {
		return manifest, err
	}
	counts := map[string]*int{
		"projects": &manifest.Projects, "runs": &manifest.Runs,
		"external_effects": &manifest.Effects, "approvals": &manifest.Approvals,
		"verification_results": &manifest.Evidence,
	}
	for table, target := range counts {
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(target); err != nil {
			return manifest, err
		}
	}
	return manifest, nil
}

// Inventory counts the same records in an existing store, so a restored copy
// can be compared against the manifest of the backup it came from.
func (s *Store) Inventory(ctx context.Context) (BackupManifest, error) {
	var manifest BackupManifest
	counts := map[string]*int{
		"projects": &manifest.Projects, "runs": &manifest.Runs,
		"external_effects": &manifest.Effects, "approvals": &manifest.Approvals,
		"verification_results": &manifest.Evidence,
	}
	for table, target := range counts {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(target); err != nil {
			return manifest, err
		}
	}
	return manifest, nil
}

// Matches reports whether a restored inventory holds the same records the
// backup did. The digest is not compared: restoring opens the database, which
// migrates and rewrites pages, so identical bytes is the wrong question.
func (m BackupManifest) Matches(other BackupManifest) []string {
	var differences []string
	compare := func(name string, expected, actual int) {
		if expected != actual {
			differences = append(differences, fmt.Sprintf("%s: %d → %d", name, expected, actual))
		}
	}
	compare("projects", m.Projects, other.Projects)
	compare("runs", m.Runs, other.Runs)
	compare("external effects", m.Effects, other.Effects)
	compare("approvals", m.Approvals, other.Approvals)
	compare("evidence", m.Evidence, other.Evidence)
	return differences
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}
