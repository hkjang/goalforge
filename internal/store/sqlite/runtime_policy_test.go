package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goalforge/goalforge/internal/model"
)

func TestRuntimePolicyRoundTripAndValidation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	project := model.Project{ID: "P-RUNTIME", Name: "runtime", RepositoryPath: "/runtime", DefaultBranch: "main", Provider: "codex"}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRuntimePolicy(ctx, project.ID, RuntimePolicy{TurnTimeout: 5 * time.Minute, RunTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	policy, err := s.RuntimePolicy(ctx, project.ID)
	if err != nil || policy.TurnTimeout != 5*time.Minute || policy.RunTimeout != time.Hour {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	if err = s.SetRuntimePolicy(ctx, project.ID, RuntimePolicy{TurnTimeout: 2 * time.Hour, RunTimeout: time.Hour}); err == nil {
		t.Fatal("accepted turn timeout greater than run timeout")
	}
}
