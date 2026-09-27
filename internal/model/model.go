package model

import (
	"fmt"
	"strings"
	"time"
)

// Task types map short user commands onto structured internal work so runs
// stay auditable by intent.
const (
	TaskDiscoverIdeas     = "DISCOVER_IDEAS"
	TaskImplementSelected = "IMPLEMENT_SELECTED"
	TaskContinueGoal      = "CONTINUE_GOAL"
	TaskAuditAndImprove   = "AUDIT_AND_IMPROVE"
	TaskVerifyAndRepair   = "VERIFY_AND_REPAIR"
	TaskReplanGoal        = "REPLAN_GOAL"
	TaskCreateCheckpoint  = "CREATE_CHECKPOINT"
)

type Project struct {
	ID, Name, RepositoryPath, DefaultBranch, Provider, Model string
	// FallbackModel is the approved substitute used when the configured
	// model is rejected by the provider (retry matrix: model_unsupported).
	FallbackModel     string
	State             string
	WorktreeEnabled   bool
	AutoCommitEnabled bool
	// WIPLimit is how many work items may be implemented at once. Above one
	// it only applies to items whose declared change scopes are disjoint.
	WIPLimit  int
	CreatedAt time.Time
}

type Goal struct {
	ID, ProjectID, Title, Objective, Status, ChangeReason string
	Version                                               int
	CreatedAt                                             time.Time
	Criteria                                              []Criterion
}

type Criterion struct {
	Type, ExpectedValue string
	// RequiredKind names the kind of check that can settle this criterion
	// (build, test, integration, journey, security, performance, review). A
	// criterion about a user being able to save their work is not settled by
	// the code compiling, so the criterion records what would count.
	RequiredKind string
}

type Milestone struct {
	ID, GoalID, Title, Status string
	Weight                    float64
}

type WorkItem struct {
	ID, GoalID, MilestoneID, Type, Title, Status, Risk string
	ChangeScope                                        string
	// Dependencies are every work item that must be DONE first. One
	// predecessor could not express "this needs both the schema and the
	// client", which is the shape most real work has.
	Dependencies []string
	// Objective states why the item exists and Acceptance states what has to
	// be true for it to be done. A title alone is not a specification an
	// execution session or a reviewer can work from.
	Objective, Acceptance string
	// BlockedReason explains a BLOCKED status in the user's terms.
	BlockedReason    string
	Priority, Weight float64
	EstimatedTokens  int64
}

type IdeaScore struct {
	GoalContribution, UserValue, OperationalNeed float64
	Feasibility, RiskReduction, Difficulty       float64
	PriorityScore                                float64
	ExpectedChangeScope                          string
	Fingerprint                                  string
	ScopeExpansion, ApprovalRequired             bool
}

type GoalView struct {
	Project  Project
	Goal     Goal
	Progress float64
	Complete bool
}

// ParseCriterion reads a criterion written as "type=value" with an optional
// "@kind" on the type, as in "checkout_saves@journey=true". The kind is part
// of the criterion rather than a separate flag because a criterion without the
// kind of proof it needs is the thing that lets a build stand in for a working
// feature.
func ParseCriterion(raw string) (Criterion, error) {
	name, value, found := strings.Cut(strings.TrimSpace(raw), "=")
	if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
		return Criterion{}, fmt.Errorf("criterion %q must be type=value or type@kind=value", raw)
	}
	criterion := Criterion{Type: strings.TrimSpace(name), ExpectedValue: strings.TrimSpace(value)}
	if plain, kind, hasKind := strings.Cut(criterion.Type, "@"); hasKind {
		criterion.Type = strings.TrimSpace(plain)
		criterion.RequiredKind = strings.ToLower(strings.TrimSpace(kind))
		if criterion.Type == "" || criterion.RequiredKind == "" {
			return Criterion{}, fmt.Errorf("criterion %q must name both a type and a kind around the @", raw)
		}
	}
	return criterion, nil
}
