package capture

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/browser"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// Outcome is what one capture pass did.
type Outcome struct {
	Captured []string
	Failed   map[string]string
	Skipped  []string
}

// Run captures the pages that need it.
//
// Pages already current are skipped rather than re-shot: a run that retook
// every screenshot every time would spend a browser session proving that
// nothing changed, and the pictures would churn in version control for no
// reason.
func Run(ctx context.Context, db *store.Store, client browser.Client, repository, projectID, headSHA, toolVersion string,
	manifest Manifest, only []string) (Outcome, error) {
	outcome := Outcome{Failed: map[string]string{}}
	if err := manifest.Validate(); err != nil {
		return outcome, err
	}
	report, err := Status(ctx, db, repository, projectID, headSHA, manifest)
	if err != nil {
		return outcome, err
	}
	wanted := map[string]bool{}
	for _, key := range only {
		wanted[key] = true
	}
	for _, status := range report.Statuses {
		if len(wanted) > 0 && !wanted[status.Page.Key()] {
			continue
		}
		if status.Status == StatusCurrent {
			outcome.Skipped = append(outcome.Skipped, status.Page.Label())
			continue
		}
		result, runErr := client.Run(ctx, browser.RunRequest{ScriptKey: status.Page.Script,
			BaseURL: manifest.BaseURL, Screenshot: "on", Trace: "retain-on-failure",
			Variables: map[string]string{"role": status.Page.Role, "state": status.Page.State,
				"route": status.Page.Route, "seed": manifest.SeedRef}})
		if runErr != nil {
			outcome.Failed[status.Page.Label()] = runErr.Error()
			continue
		}
		if !result.Passed {
			outcome.Failed[status.Page.Label()] = result.Detail
			continue
		}
		if len(result.Screenshots) == 0 {
			// The script passed and took no picture. Recording the page as
			// captured here would put it in the done column with nothing
			// behind it, and the guide would go out with a gap nobody sees.
			outcome.Failed[status.Page.Label()] = "스크립트는 통과했지만 화면을 찍지 않았습니다"
			continue
		}
		if err = db.RecordCapture(ctx, store.PageCapture{ProjectID: projectID, PageKey: status.Page.Key(),
			Route: status.Page.Route, Role: status.Page.Role, State: status.Page.State,
			CommitSHA: headSHA, ArtifactPath: result.Screenshots[0], RunID: result.RunID,
			ToolVersion: toolVersion, ServiceAt: client.BaseURL, CapturedAt: time.Now().UTC()}); err != nil {
			return outcome, err
		}
		outcome.Captured = append(outcome.Captured, status.Page.Label())
	}
	return outcome, nil
}

// Summary is the outcome as a person reads it.
func (o Outcome) Summary() string {
	parts := []string{fmt.Sprintf("찍음 %d", len(o.Captured))}
	if len(o.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("그대로 %d", len(o.Skipped)))
	}
	if len(o.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("실패 %d", len(o.Failed)))
	}
	return strings.Join(parts, " · ")
}
