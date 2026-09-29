package observer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// SupplyPolicy caps what one pass may put on the board.
type SupplyPolicy struct {
	// PerRun is the most work items one pass may file.
	PerRun int
	// MaxOutstanding stops supply entirely once this many supplied items are
	// still unfinished. A board nobody is working through does not need more
	// findings on it; it needs the ones it has closed.
	MaxOutstanding int
}

// DefaultSupplyPolicy is the specification's recommended starting point.
func DefaultSupplyPolicy() SupplyPolicy { return SupplyPolicy{PerRun: 3, MaxOutstanding: 10} }

// ErrSupplyPaused means the board already holds as much supplied work as the
// policy allows.
var ErrSupplyPaused = errors.New("supply paused: too much supplied work is still outstanding")

// SupplyResult is what one pass did.
type SupplyResult struct {
	// Filed are the work items created.
	Filed []string
	// AlreadyFiled maps a criterion to the work item that already covers the
	// same defect. It is reported rather than silently skipped so a reader can
	// see the pass found the gap and chose not to duplicate it.
	AlreadyFiled map[string]string
	// Deferred are findings that did not fit this pass's cap, held rather than
	// lost.
	Deferred []string
	// Unchecked carries the observation's list through, so the report says what
	// nobody looked at as plainly as what was found.
	Unchecked []string
}

// Supply turns an observation's findings into work items.
//
// Findings are ranked before the cap is applied, not while walking them. A
// detector that happens to run first would otherwise spend the whole pass's
// budget regardless of what it found.
func Supply(ctx context.Context, db *store.Store, goalID string, observation Observation, pack standards.Pack, policy SupplyPolicy) (SupplyResult, error) {
	result := SupplyResult{AlreadyFiled: map[string]string{}, Unchecked: observation.Unchecked}
	if policy.PerRun <= 0 {
		policy = DefaultSupplyPolicy()
	}
	outstanding, err := db.OutstandingSuppliedWork(ctx, observation.ProjectID)
	if err != nil {
		return result, err
	}
	if policy.MaxOutstanding > 0 && outstanding >= policy.MaxOutstanding {
		return result, fmt.Errorf("%w: %d건", ErrSupplyPaused, outstanding)
	}
	actionable := make([]Finding, 0, len(observation.Findings))
	for _, finding := range observation.Findings {
		// UNKNOWN is an investigation, not a defect. Filing it as work would
		// put "go and look at this" on the board as if it were a fix, and the
		// board would fill with things nobody established were wrong.
		if finding.Result == standards.ResultUnmet || finding.Result == standards.ResultPartial {
			actionable = append(actionable, finding)
		}
	}
	sort.SliceStable(actionable, func(i, j int) bool {
		return severityRank(pack, actionable[i].StandardID) > severityRank(pack, actionable[j].StandardID)
	})
	for _, finding := range actionable {
		existing, lookupErr := db.FindingFor(ctx, observation.ProjectID, finding.StandardID, finding.DefectKind, finding.TargetScope)
		switch {
		case lookupErr == nil:
			result.AlreadyFiled[finding.StandardID] = existing
			continue
		case !errors.Is(lookupErr, store.ErrNotFound):
			return result, lookupErr
		}
		if len(result.Filed) >= policy.PerRun {
			// Held, not lost. A finding dropped on the floor is one the next
			// pass has to rediscover, and the pass after that.
			result.Deferred = append(result.Deferred, finding.StandardID)
			continue
		}
		item, createErr := db.CreateWorkItem(ctx, workItemFor(goalID, pack, finding, observation))
		if createErr != nil {
			return result, createErr
		}
		if err = db.FileFinding(ctx, observation.ProjectID, finding.StandardID, finding.DefectKind,
			finding.TargetScope, item.ID); err != nil {
			return result, err
		}
		result.Filed = append(result.Filed, item.ID)
	}
	return result, nil
}

func severityRank(pack standards.Pack, standardID string) int {
	if standard, ok := pack.Standard(standardID); ok && standard.Severity == standards.SeverityRequired {
		return 1
	}
	return 0
}

// workItemFor writes the card. It carries the criterion, what was observed and
// what would settle it, because a card that only says "UX-004 미충족" sends
// whoever picks it up back to the catalogue to find out what that means.
func workItemFor(goalID string, pack standards.Pack, finding Finding, observation Observation) model.WorkItem {
	standard, _ := pack.Standard(finding.StandardID)
	var observed []string
	for _, item := range finding.Evidence {
		observed = append(observed, item.Detail)
	}
	objective := finding.Detail
	if len(observed) > 0 {
		objective += "\n\n관측: " + strings.Join(observed, "\n      ")
	}
	objective += "\n\n기준: " + standard.Intent
	acceptance := acceptanceFrom(standard)
	priority := 50.0
	if standard.Severity == standards.SeverityRequired {
		priority = 90
	}
	return model.WorkItem{GoalID: goalID, Type: "IMPLEMENT",
		Title:       finding.StandardID + ": " + standard.Title,
		Objective:   objective,
		Acceptance:  acceptance,
		ChangeScope: finding.TargetScope,
		Priority:    priority, Weight: 1, Risk: "medium", Status: "BACKLOG"}
}

// acceptanceFrom turns the criterion's own checks into the card's acceptance
// condition, so the thing that files the work and the thing that judges it are
// reading the same sentence.
func acceptanceFrom(standard standards.Standard) string {
	var lines []string
	for _, check := range standard.Checks {
		lines = append(lines, "- ["+check.Type+"] "+check.Assertion)
	}
	lines = append(lines, "근거: "+strings.Join(standard.EvidenceRequired, ", "))
	return strings.Join(lines, "\n")
}

// ObserveAndSupply is the whole pass: read the repository at a commit, judge
// each criterion in force, record the assessments, and file what is missing.
func ObserveAndSupply(ctx context.Context, db *store.Store, projectID, goalID, repository, sha, toolVersion string,
	pack standards.Pack, profile standards.Profile, detectors []Detector, policy SupplyPolicy) (SupplyResult, error) {
	tree, err := Open(ctx, repository, sha)
	if err != nil {
		return SupplyResult{}, err
	}
	now := time.Now().UTC()
	observation, err := Observe(tree, standards.InForce(pack, profile, now), detectors)
	if err != nil {
		return SupplyResult{}, err
	}
	observation.ProjectID, observation.CommitSHA, observation.ToolVersion = projectID, sha, toolVersion
	for _, finding := range observation.Findings {
		standard, _ := pack.Standard(finding.StandardID)
		if _, err = db.RecordAssessment(ctx, standard, standards.Assessment{ProjectID: projectID,
			StandardID: finding.StandardID, CommitSHA: sha, Result: finding.Result, Detail: finding.Detail,
			Evidence: finding.Evidence, AssessedAt: now, ToolVersion: toolVersion}, false); err != nil {
			return SupplyResult{}, err
		}
	}
	return Supply(ctx, db, goalID, observation, pack, policy)
}
