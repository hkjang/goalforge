package capture

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// How a declared page stands.
const (
	// StatusCurrent means the screenshot still depicts this commit.
	StatusCurrent = "CURRENT"
	// StatusStale means the screen has changed since the picture was taken.
	StatusStale = "STALE"
	// StatusMissing means nobody has captured this page.
	StatusMissing = "MISSING"
)

// PageStatus is one declared page and its screenshot.
type PageStatus struct {
	Page    Page
	Status  string
	Capture store.PageCapture
	// Reason says why, in the terms the reader has to act on: which files
	// moved under this screen.
	Reason string
}

// Report is the manifest measured against what has been captured.
type Report struct {
	Statuses                []PageStatus
	Current, Stale, Missing int
}

// Complete reports whether every required page has a current screenshot.
func (r Report) Complete() bool {
	for _, status := range r.Statuses {
		if status.Page.Required && status.Status != StatusCurrent {
			return false
		}
	}
	return true
}

// Shortfall lists the required pages that are not current, which is what a
// documentation gate fails on.
func (r Report) Shortfall() []string {
	var short []string
	for _, status := range r.Statuses {
		if status.Page.Required && status.Status != StatusCurrent {
			short = append(short, fmt.Sprintf("%s (%s)", status.Page.Label(), status.Status))
		}
	}
	sort.Strings(short)
	return short
}

// Status measures a manifest against the captures on record.
//
// A capture taken at an older commit is not automatically stale. Marking it so
// would make every commit invalidate every screenshot, the report would be red
// permanently, and people would stop reading it — which costs more than the
// stale pictures it was meant to catch. What makes a capture stale is a change
// under the page's own sources.
func Status(ctx context.Context, db *store.Store, repository, projectID, headSHA string, manifest Manifest) (Report, error) {
	var report Report
	captures, err := db.Captures(ctx, projectID)
	if err != nil {
		return report, err
	}
	// The files that moved since each capture's commit, fetched once per
	// distinct commit rather than once per page: a manifest of forty pages
	// captured in one run would otherwise ask git the same question forty
	// times.
	changed := map[string][]string{}
	for _, page := range manifest.Pages {
		capture, captured := captures[page.Key()]
		status := PageStatus{Page: page, Capture: capture}
		switch {
		case !captured:
			status.Status = StatusMissing
			status.Reason = "아직 찍지 않았습니다"
		case capture.CommitSHA == headSHA:
			status.Status = StatusCurrent
		default:
			files, ok := changed[capture.CommitSHA]
			if !ok {
				if files, err = gitops.ChangedSince(ctx, repository, capture.CommitSHA); err != nil {
					return report, fmt.Errorf("%s: %w", page.Label(), err)
				}
				changed[capture.CommitSHA] = files
			}
			if touched := touching(files, page.Sources); len(touched) > 0 {
				status.Status = StatusStale
				status.Reason = fmt.Sprintf("%s 이후 %s 이(가) 바뀌었습니다",
					shortSHA(capture.CommitSHA), strings.Join(touched, ", "))
			} else {
				// Older commit, but nothing under this screen moved. The
				// picture still depicts it.
				status.Status = StatusCurrent
				status.Reason = fmt.Sprintf("%s 에서 찍었고 이 화면을 바꾸는 변경은 없었습니다", shortSHA(capture.CommitSHA))
			}
		}
		switch status.Status {
		case StatusCurrent:
			report.Current++
		case StatusStale:
			report.Stale++
		default:
			report.Missing++
		}
		report.Statuses = append(report.Statuses, status)
	}
	sort.SliceStable(report.Statuses, func(i, j int) bool {
		return statusOrder(report.Statuses[i].Status) < statusOrder(report.Statuses[j].Status)
	})
	return report, nil
}

func statusOrder(status string) int {
	switch status {
	case StatusStale:
		return 0
	case StatusMissing:
		return 1
	}
	return 2
}

// touching is the changed files that fall under a page's declared sources.
//
// It returns at most a handful: a reader needs to know which part of the
// screen moved, not the whole diff.
func touching(changed, sources []string) []string {
	var hits []string
	for _, file := range changed {
		for _, source := range sources {
			if underSource(file, source) {
				hits = append(hits, file)
				break
			}
		}
		if len(hits) >= 5 {
			break
		}
	}
	return hits
}

// underSource supports the trailing "/**" the manifests use, and plain globs.
func underSource(file, source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	if prefix := strings.TrimSuffix(source, "/**"); prefix != source {
		return file == prefix || strings.HasPrefix(file, prefix+"/")
	}
	if matched, _ := path.Match(source, file); matched {
		return true
	}
	// A bare directory is taken to mean everything under it, which is how
	// people write these by hand.
	return strings.HasPrefix(file, strings.TrimSuffix(source, "/")+"/")
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
