package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// AutonomyConfig is a project's saved autonomy envelope.
//
// It is stored rather than passed as flags because the envelope's whole
// purpose is to let work proceed while nobody is watching. An envelope that
// only exists as arguments to a command somebody types is an envelope that
// applies exactly when somebody is already there — which is the one time it
// was not needed.
type AutonomyConfig struct {
	ProjectID        string
	Enabled          bool
	AllStandards     bool
	AllowedStandards []string
	AllScopes        bool
	AllowedScopes    []string
	MaxTokens        int64
	DailyLimit       int
	AutoMerge        bool
	UpdatedAt        time.Time
}

// SaveAutonomyConfig records a project's envelope.
func (s *Store) SaveAutonomyConfig(ctx context.Context, config AutonomyConfig) error {
	if config.ProjectID == "" {
		return errors.New("project is required")
	}
	standards, err := json.Marshal(config.AllowedStandards)
	if err != nil {
		return err
	}
	scopes, err := json.Marshal(config.AllowedScopes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO autonomy_policies(project_id,enabled,all_standards,allowed_standards,all_scopes,allowed_scopes,max_tokens,daily_limit,auto_merge,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(project_id) DO UPDATE SET enabled=excluded.enabled,all_standards=excluded.all_standards,
allowed_standards=excluded.allowed_standards,all_scopes=excluded.all_scopes,allowed_scopes=excluded.allowed_scopes,
max_tokens=excluded.max_tokens,daily_limit=excluded.daily_limit,auto_merge=excluded.auto_merge,
updated_at=excluded.updated_at`,
		config.ProjectID, boolToInt(config.Enabled), boolToInt(config.AllStandards), string(standards),
		boolToInt(config.AllScopes), string(scopes), config.MaxTokens, config.DailyLimit,
		boolToInt(config.AutoMerge), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// AutonomyConfigFor reads a project's envelope.
//
// A project with none gets the zero value, which is switched off. The default
// has to be the safe one: an unattended loop reading a missing configuration
// as permission is the worst possible reading of an absence.
func (s *Store) AutonomyConfigFor(ctx context.Context, projectID string) (AutonomyConfig, error) {
	config := AutonomyConfig{ProjectID: projectID}
	var enabled, allStandards, allScopes, autoMerge int
	var standards, scopes, updated string
	err := s.db.QueryRowContext(ctx, `SELECT enabled,all_standards,allowed_standards,all_scopes,allowed_scopes,max_tokens,daily_limit,auto_merge,updated_at
FROM autonomy_policies WHERE project_id=?`, projectID).
		Scan(&enabled, &allStandards, &standards, &allScopes, &scopes, &config.MaxTokens,
			&config.DailyLimit, &autoMerge, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return config, ErrNotFound
	}
	if err != nil {
		return config, err
	}
	config.Enabled, config.AllStandards = enabled == 1, allStandards == 1
	config.AllScopes, config.AutoMerge = allScopes == 1, autoMerge == 1
	if err = json.Unmarshal([]byte(standards), &config.AllowedStandards); err != nil {
		return config, err
	}
	if err = json.Unmarshal([]byte(scopes), &config.AllowedScopes); err != nil {
		return config, err
	}
	config.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return config, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
