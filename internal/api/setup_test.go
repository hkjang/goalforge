package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/goalforge/goalforge/internal/diagnostics"
	"github.com/goalforge/goalforge/internal/model"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, output)
		}
	}
	return dir
}

// The setup flow refuses a directory that is not a repository, and says so as
// a blocking finding rather than a warning the wizard would step past.
func TestDoctorBlocksNonRepository(t *testing.T) {
	server, db := apiFixture(t, "operator-secret")
	defer db.Close()
	plain := t.TempDir()
	var report diagnostics.Report
	get(t, server, "/api/v1/doctor?repo="+plain, &report)
	found := false
	for _, check := range report.Checks {
		if check.Name == "project" {
			found = true
			if check.Level != diagnostics.LevelFail {
				t.Fatalf("a directory with no repository must block: %+v", check)
			}
		}
	}
	if !found || report.Ready() {
		t.Fatalf("report=%+v", report)
	}
	repo := gitRepo(t)
	report = diagnostics.Report{}
	get(t, server, "/api/v1/doctor?repo="+repo, &report)
	for _, check := range report.Checks {
		if check.Name == "project" && check.Level == diagnostics.LevelFail {
			t.Fatalf("a real repository must not block: %+v", check)
		}
	}
}

// Setup creates a project, its first goal version, and its budget, refusing a
// goal with no completion criteria: a goal nothing can judge complete is not one.
func TestSetupFlowCreatesProjectGoalAndPolicy(t *testing.T) {
	server, db := apiFixture(t, "operator-secret")
	defer db.Close()
	repo := gitRepo(t)
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects",
		`{"name":"wizard","repository_path":"`+repo+`","provider":"nope"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported provider status=%d", response.Code)
	}
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects",
		`{"name":"wizard","repository_path":"`+filepath.Join(repo, "missing")+`","provider":"claude"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("non-repository status=%d", response.Code)
	}
	response := mutate(t, server, http.MethodPost, "/api/v1/projects",
		`{"name":"wizard","repository_path":"`+repo+`","provider":"claude","model":"haiku","worktrees":true,"auto_commit":true}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Project model.Project `json:"project"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Project.ID == "" || created.Project.DefaultBranch != "main" {
		t.Fatalf("project=%+v", created.Project)
	}
	id := created.Project.ID
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/"+id+"/goal",
		`{"title":"ship","objective":"do it","criteria":[]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("a goal without criteria must be refused: %d", response.Code)
	}
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/"+id+"/goal",
		`{"title":"ship","objective":"do it","criteria":[{"type":"build_passed","expected_value":"true"}]}`); response.Code != http.StatusOK {
		t.Fatalf("goal status=%d body=%s", response.Code, response.Body.String())
	}
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/"+id+"/policy",
		`{"token_limit":2000000,"cost_limit_usd":50,"daily_run_limit":20,"turn_timeout":"30m","run_timeout":"2h"}`); response.Code != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", response.Code, response.Body.String())
	}
	// A turn timeout longer than the run timeout is contradictory.
	if response := mutate(t, server, http.MethodPost, "/api/v1/projects/"+id+"/policy",
		`{"turn_timeout":"5h","run_timeout":"1h"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("contradictory timeouts status=%d", response.Code)
	}
	budget, err := db.ProjectBudgetConfig(t.Context(), id)
	if err != nil || budget.TokenLimit != 2000000 || budget.CostLimitUSD != 50 || budget.DailyRunLimit != 20 {
		t.Fatalf("a rejected partial update must not wipe the budget: %+v err=%v", budget, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
}
