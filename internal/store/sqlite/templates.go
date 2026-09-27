package sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GateTemplate is a starting set of verification gates for a kind of project.
// A project with no gates cannot complete a goal, and picking gates from
// nothing is where most setups stall.
type GateTemplate struct {
	Name, Description string
	Gates             []GateConfig
}

// gateTemplates are deliberately conservative: commands that exist in a stock
// toolchain, thresholds only where a measurement is meaningful. Every template
// gate is build or test kind, because a journey check has to exercise this
// project's actual user task and no template can guess it — readiness warns
// about the gap rather than a template pretending to fill it.
var gateTemplates = map[string]GateTemplate{
	"go-api": {
		Name:        "go-api",
		Description: "Go 서비스: 빌드, 테스트, 정적 분석, 커버리지",
		Gates: []GateConfig{
			{Type: "build_passed", Command: []string{"go", "build", "./..."}, Timeout: 5 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
			{Type: "tests_passed", Command: []string{"go", "test", "./..."}, Timeout: 15 * time.Minute, Required: true, SuccessValue: "true", Kind: "test"},
			{Type: "vet_clean", Command: []string{"go", "vet", "./..."}, Timeout: 5 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
			{Type: "coverage", Command: []string{"go", "test", "-cover", "./..."}, Timeout: 15 * time.Minute, Required: false,
				SuccessValue: "70", ValuePattern: `coverage:\s+([0-9.]+)%`, Kind: "test"},
		},
	},
	"node-frontend": {
		Name:        "node-frontend",
		Description: "Node 프런트엔드: 타입 검사, 린트, 테스트, 빌드",
		Gates: []GateConfig{
			{Type: "typecheck_passed", Command: []string{"npm", "run", "typecheck"}, Timeout: 10 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
			{Type: "lint_clean", Command: []string{"npm", "run", "lint"}, Timeout: 10 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
			{Type: "tests_passed", Command: []string{"npm", "test", "--", "--run"}, Timeout: 20 * time.Minute, Required: true, SuccessValue: "true", Kind: "test"},
			{Type: "build_passed", Command: []string{"npm", "run", "build"}, Timeout: 15 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
		},
	},
	"python-library": {
		Name:        "python-library",
		Description: "Python 라이브러리: 테스트, 린트, 타입 검사",
		Gates: []GateConfig{
			{Type: "tests_passed", Command: []string{"python", "-m", "pytest", "-q"}, Timeout: 20 * time.Minute, Required: true, SuccessValue: "true", Kind: "test"},
			{Type: "lint_clean", Command: []string{"python", "-m", "ruff", "check", "."}, Timeout: 5 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
			{Type: "typecheck_passed", Command: []string{"python", "-m", "mypy", "."}, Timeout: 10 * time.Minute, Required: false, SuccessValue: "true", Kind: "build"},
		},
	},
	"docs": {
		Name:        "docs",
		Description: "문서 중심 저장소: 링크와 형식 검사",
		Gates: []GateConfig{
			{Type: "build_passed", Command: []string{"make", "docs"}, Timeout: 10 * time.Minute, Required: true, SuccessValue: "true", Kind: "build"},
		},
	},
}

// GateTemplateNames lists the available templates.
func GateTemplateNames() []string {
	names := make([]string, 0, len(gateTemplates))
	for name := range gateTemplates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func LookupGateTemplate(name string) (GateTemplate, error) {
	template, ok := gateTemplates[name]
	if !ok {
		return template, fmt.Errorf("unknown template %q; available: %s", name, strings.Join(GateTemplateNames(), ", "))
	}
	return template, nil
}

// ApplyGateTemplate installs a template's gates. Existing gates of the same
// type are left alone unless overwrite is set, so applying a template cannot
// silently replace a threshold someone chose deliberately.
func (s *Store) ApplyGateTemplate(ctx context.Context, projectID, name string, overwrite bool) ([]string, []string, error) {
	template, err := LookupGateTemplate(name)
	if err != nil {
		return nil, nil, err
	}
	existing, err := s.ListGates(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	present := map[string]bool{}
	for _, gate := range existing {
		present[gate.Type] = true
	}
	var added, skipped []string
	for _, gate := range template.Gates {
		if present[gate.Type] && !overwrite {
			skipped = append(skipped, gate.Type)
			continue
		}
		if err = s.UpsertGate(ctx, projectID, gate); err != nil {
			return added, skipped, err
		}
		added = append(added, gate.Type)
	}
	return added, skipped, nil
}

// PolicyProfile expresses an operating posture as the settings that implement
// it, so "팀 개발" is a set of limits rather than a dozen separate decisions.
type PolicyProfile struct {
	Name, Description string
	TokenLimit        int64
	CostLimitUSD      float64
	DailyRunLimit     int64
	WIPLimit          int
	Repair            RepairPolicy
	AutoCommit        bool
	Worktrees         bool
}

var policyProfiles = map[string]PolicyProfile{
	"personal": {Name: "personal", Description: "개인 실험: 넉넉한 반복, 낮은 비용 상한",
		TokenLimit: 500000, CostLimitUSD: 10, DailyRunLimit: 30, WIPLimit: 1,
		Repair: RepairPolicy{MaxAttempts: 3, MaxCostUSD: 2}, AutoCommit: true, Worktrees: true},
	"team": {Name: "team", Description: "팀 개발: 병렬 작업 허용, 중간 예산",
		TokenLimit: 5000000, CostLimitUSD: 200, DailyRunLimit: 60, WIPLimit: 2,
		Repair: RepairPolicy{MaxAttempts: 2, MaxCostUSD: 5}, AutoCommit: true, Worktrees: true},
	"production": {Name: "production", Description: "운영 시스템: 자동 복구 최소화, 승인 중심",
		TokenLimit: 2000000, CostLimitUSD: 100, DailyRunLimit: 20, WIPLimit: 1,
		Repair: RepairPolicy{MaxAttempts: 1, MaxCostUSD: 2}, AutoCommit: false, Worktrees: true},
}

func PolicyProfileNames() []string {
	names := make([]string, 0, len(policyProfiles))
	for name := range policyProfiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func LookupPolicyProfile(name string) (PolicyProfile, error) {
	profile, ok := policyProfiles[name]
	if !ok {
		return profile, fmt.Errorf("unknown profile %q; available: %s", name, strings.Join(PolicyProfileNames(), ", "))
	}
	return profile, nil
}

// ApplyPolicyProfile writes a profile's limits onto a project.
func (s *Store) ApplyPolicyProfile(ctx context.Context, projectID, name string) (PolicyProfile, error) {
	profile, err := LookupPolicyProfile(name)
	if err != nil {
		return profile, err
	}
	if err = s.SetProjectBudget(ctx, projectID, profile.TokenLimit, profile.CostLimitUSD); err != nil {
		return profile, err
	}
	if err = s.SetDailyLimits(ctx, projectID, profile.DailyRunLimit, 0, 0); err != nil {
		return profile, err
	}
	if err = s.SetWIPLimit(ctx, projectID, profile.WIPLimit); err != nil {
		return profile, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE projects SET auto_commit_enabled=?,worktree_enabled=? WHERE id=?`, profile.AutoCommit, profile.Worktrees, projectID)
	return profile, err
}
