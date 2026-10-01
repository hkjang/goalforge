package observer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// ProjectTick is what one unattended pass did for one project.
type ProjectTick struct {
	ProjectID, ProjectName string
	Decision               Decision
	Ran                    bool
	Filed                  []string
	Approved               []string
	Merged                 []string
	// Note carries the one-line reason when nothing happened, and Err when
	// something went wrong. They are separate because "nothing was due" is a
	// healthy tick and a failure is not, and a loop that logged them the same
	// way would make a broken project invisible among the quiet ones.
	Note string
	Err  error
}

// TickResult is one sweep across every project in the program.
type TickResult struct {
	Projects []ProjectTick
}

// Acted reports whether the sweep changed anything, so a caller can stay quiet
// when it did not.
func (r TickResult) Acted() bool {
	for _, project := range r.Projects {
		if project.Ran || len(project.Approved) > 0 || len(project.Merged) > 0 || project.Err != nil {
			return true
		}
	}
	return false
}

// Tick runs the supply and autonomy loop across every project in the program,
// once.
//
// It exists because everything the schedule is built from — a daily interval, a
// discovery budget, an idempotency key that survives two workers seeing the
// same commit — is machinery for something that runs while nobody is watching.
// Until something calls it on a timer, all of it describes a loop that only
// turns when a person types a command, which is the one situation none of it
// was for.
func Tick(ctx context.Context, db *store.Store, toolVersion string, detectors []Detector,
	schedule SchedulePolicy, supply SupplyPolicy) (TickResult, error) {
	var result TickResult
	projects, err := db.ListProjects(ctx)
	if err != nil {
		return result, err
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	for _, project := range projects {
		profile, _, profileErr := db.StandardProfile(ctx, project.ID)
		if errors.Is(profileErr, store.ErrNotFound) {
			// Not in the program. A project that never pinned a pack has not
			// asked to be maintained and must not be touched.
			continue
		}
		if profileErr != nil {
			return result, profileErr
		}
		tick := tickProject(ctx, db, project.ID, project.Name, project.RepositoryPath,
			project.DefaultBranch, toolVersion, profile, detectors, schedule, supply)
		result.Projects = append(result.Projects, tick)
	}
	return result, nil
}

// tickProject is one project's turn.
//
// Every failure is captured rather than returned, so one project's missing
// repository does not stop the sweep. An unattended loop that aborts on the
// first problem stops maintaining every project because of one — and the one
// that broke it is the one nobody was watching.
func tickProject(ctx context.Context, db *store.Store, projectID, name, repository, branch, toolVersion string,
	profile standards.Profile, detectors []Detector, schedule SchedulePolicy, supply SupplyPolicy) ProjectTick {
	tick := ProjectTick{ProjectID: projectID, ProjectName: name}
	pack, err := packFor(profile.PackRef)
	if err != nil {
		tick.Err = err
		return tick
	}
	// Drift is checked here and not only on the screens that display it. A
	// pack edited in place keeps its id@version, so it resolves by ref and no
	// revision bump marks anything stale — the project is simply judged by
	// criteria it never agreed to, and an unattended sweep is the one place
	// nobody would notice.
	//
	// Asked of the store rather than compared here. The rule about what counts
	// as drift — including that a project enrolled before checksums were
	// recorded has not drifted — lives in one place, and a second copy of it
	// is two rules waiting to disagree.
	drifted, err := db.PackDrift(ctx, projectID, pack)
	if err != nil {
		tick.Err = err
		return tick
	}
	if drifted {
		tick.Err = fmt.Errorf("고정한 팩 %s 의 체크섬이 다릅니다 — 이 빌드의 팩은 프로젝트가 동의한 것이 아닙니다. `goalforge standards profile --pack %s` 로 다시 고정하세요",
			profile.PackRef, profile.PackRef)
		return tick
	}
	head, err := gitops.HeadCommit(ctx, repository, branch)
	if err != nil {
		tick.Err = fmt.Errorf("저장소를 읽지 못했습니다: %w", err)
		return tick
	}
	goal, err := db.CurrentGoal(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		// Enrolling a project and setting its goal are separate commands, so a
		// project sits in this state for as long as it takes someone to run the
		// second one. There is nothing to supply work against, but nothing is
		// broken either — and calling it a failure makes the sweep print an
		// error every quarter of an hour, forever, about a project that only
		// needs a goal. The loop is the one place that cost compounds silently:
		// nobody is watching, so the noise is discovered as a habit of ignoring
		// the log.
		tick.Note = "목표가 아직 없습니다 — `goalforge goal set` 이후에 공급이 시작됩니다"
		return tick
	}
	if err != nil {
		tick.Err = fmt.Errorf("목표를 읽지 못했습니다: %w", err)
		return tick
	}
	pass, err := RunScheduledPass(ctx, db, PassRequest{ProjectID: projectID, GoalID: goal.ID,
		Repository: repository, HeadSHA: head, ToolVersion: toolVersion, Pack: pack, Profile: profile,
		Detectors: detectors, Schedule: schedule, Supply: supply})
	switch {
	case errors.Is(err, ErrDiscoveryBudgetSpent), errors.Is(err, ErrSupplyPaused):
		// Both are the loop working as configured, not a failure. Recording
		// them as errors would make a correctly throttled project look broken.
		tick.Note = err.Error()
	case err != nil:
		tick.Err = err
		return tick
	default:
		tick.Decision, tick.Ran = pass.Decision, pass.Ran
		tick.Filed = pass.Result.Filed
		if !pass.Ran && tick.Note == "" {
			tick.Note = pass.Detail
		}
	}
	config, err := db.AutonomyConfigFor(ctx, projectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		tick.Err = err
		return tick
	}
	// A project with no saved envelope gets the zero value, which is switched
	// off — an unattended loop reading a missing configuration as permission
	// is the worst possible reading of an absence. Whether an envelope permits
	// anything is AutoApprove's decision and is not repeated here: the same
	// rule in two places is two rules waiting to disagree.
	policy := AutonomyPolicy{Enabled: config.Enabled, AllowedStandards: config.AllowedStandards,
		AllStandards: config.AllStandards, AllowedScopes: config.AllowedScopes, AllScopes: config.AllScopes,
		MaxTokens: config.MaxTokens, DailyLimit: config.DailyLimit, AutoMerge: config.AutoMerge}
	execution, err := AutoApprove(ctx, db, projectID, goal.ID, policy)
	if err != nil {
		tick.Err = err
		return tick
	}
	tick.Approved = execution.Approved
	if !policy.AutoMerge {
		return tick
	}
	merges, err := AutoApproveMerges(ctx, db, projectID, goal.ID, policy)
	if err != nil {
		tick.Err = err
		return tick
	}
	tick.Merged = merges.Approved
	return tick
}

// packFor resolves a pinned pack reference.
//
// There is one shipped pack; a profile naming anything else is refused rather
// than silently measured against whatever happens to be compiled in.
func packFor(ref string) (standards.Pack, error) { return standards.ByRef(ref) }

var _ = time.Now
