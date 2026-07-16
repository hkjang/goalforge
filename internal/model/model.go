package model

import "time"

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
	State                                                    string
	WorktreeEnabled                                          bool
	AutoCommitEnabled                                        bool
	CreatedAt                                                time.Time
}

type Goal struct {
	ID, ProjectID, Title, Objective, Status, ChangeReason string
	Version                                               int
	CreatedAt                                             time.Time
	Criteria                                              []Criterion
}

type Criterion struct {
	Type, ExpectedValue string
}

type Milestone struct {
	ID, GoalID, Title, Status string
	Weight                    float64
}

type WorkItem struct {
	ID, GoalID, MilestoneID, Type, Title, Status, Dependency, Risk string
	ChangeScope                                                    string
	Priority, Weight                                               float64
	EstimatedTokens                                                int64
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
