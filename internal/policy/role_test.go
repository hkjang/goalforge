package policy

import (
	"errors"
	"strings"
	"testing"
)

// The operator is the default: a marker has to be present for authority to be
// reduced, not absent for it to be granted.
func TestRoleDefaultsToOperator(t *testing.T) {
	t.Setenv(EnvRole, "")
	if CurrentRole() != RoleOperator {
		t.Fatalf("role=%s", CurrentRole())
	}
	if err := RequireOperator("승인"); err != nil {
		t.Fatalf("the operator must be able to approve: %v", err)
	}
	t.Setenv(EnvRole, RoleImplementation)
	if CurrentRole() != RoleImplementation {
		t.Fatalf("role=%s", CurrentRole())
	}
	var refusal *PrivilegedOperationError
	err := RequireOperator("승인")
	if !errors.As(err, &refusal) || refusal.Operation != "승인" {
		t.Fatalf("an implementation session must be refused: %v", err)
	}
	if !strings.Contains(err.Error(), "구현") {
		t.Fatalf("the refusal must explain the separation: %v", err)
	}
}

// A session is handed a named environment rather than everything the operator
// exported: an unrelated deployment token is not something to pass along just
// because it was in scope.
func TestSessionEnvironmentWithholdsUnrelatedSecrets(t *testing.T) {
	t.Setenv(EnvInherit, "")
	t.Setenv(EnvPassthrough, "")
	host := []string{
		"PATH=/usr/bin",
		"HOME=/home/dev",
		"ANTHROPIC_API_KEY=sk-provider",
		"AWS_SECRET_ACCESS_KEY=leak-me",
		"DEPLOY_TOKEN=leak-me-too",
		"DATABASE_URL=postgres://user:pass@host/db",
	}
	session := SessionEnvironment(host, RoleImplementation)
	joined := strings.Join(session, "\n")
	for _, expected := range []string{"PATH=/usr/bin", "HOME=/home/dev", "ANTHROPIC_API_KEY=sk-provider", EnvRole + "=" + RoleImplementation} {
		if !strings.Contains(joined, expected) {
			t.Errorf("session environment must keep %q", expected)
		}
	}
	for _, withheld := range []string{"AWS_SECRET_ACCESS_KEY", "DEPLOY_TOKEN", "DATABASE_URL"} {
		if strings.Contains(joined, withheld) {
			t.Errorf("session environment leaked %s", withheld)
		}
	}
}

// Widening the set stays possible, because a tool that genuinely needs a
// variable must not be impossible to run.
func TestSessionEnvironmentCanBeWidenedDeliberately(t *testing.T) {
	t.Setenv(EnvInherit, "")
	t.Setenv(EnvPassthrough, "DEPLOY_TOKEN")
	host := []string{"PATH=/usr/bin", "DEPLOY_TOKEN=needed", "OTHER=no"}
	joined := strings.Join(SessionEnvironment(host, RoleImplementation), "\n")
	if !strings.Contains(joined, "DEPLOY_TOKEN=needed") {
		t.Error("an explicitly passed variable must survive")
	}
	if strings.Contains(joined, "OTHER=no") {
		t.Error("only the named variable should be added")
	}
	t.Setenv(EnvInherit, "all")
	joined = strings.Join(SessionEnvironment(host, RoleImplementation), "\n")
	if !strings.Contains(joined, "OTHER=no") {
		t.Error("inherit=all must restore the previous behaviour")
	}
	if !strings.Contains(joined, EnvRole+"="+RoleImplementation) {
		t.Error("the role marker is not optional")
	}
}
