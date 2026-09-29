// Package observer reads a repository at a pinned commit and reports where its
// criteria stand, without changing anything.
//
// What it can do is prove absences. A CDN URL in a bundled asset, a required
// environment variable beyond the four the contract allows, a missing
// Dockerfile — these are visible in the source and a finding can be made from
// them. What it cannot do is prove that something works: a route in the router
// says nothing about whether the page restores its filters, and a test file
// says nothing about whether the test passes.
//
// So the observer reports UNMET where it can see an absence and UNKNOWN
// everywhere else, and never MET for anything a person would have to run. The
// judge in the standards package enforces that independently; this package
// simply does not produce the evidence that would satisfy it.
package observer

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/goalforge/goalforge/internal/gitops"
	"github.com/goalforge/goalforge/internal/standards"
)

// Tree is the repository at one commit. It is an interface so a detector can
// be tested against a composed repository rather than a real clone.
type Tree interface {
	Paths() []string
	File(path string) (string, error)
}

// ErrExecutedEvidence is returned when something tries to record a test result
// it did not run.
var ErrExecutedEvidence = errors.New("a file read cannot produce executed evidence")

// StaticEvidence records something read from the repository.
//
// It refuses an executed evidence kind. A detector that labels a file read as
// a browser_test_result launders a guess into a pass, and it would do so past
// every other guard because the judge only checks the kind, not who produced
// it.
func StaticEvidence(kind, detail string) (standards.Evidence, error) {
	if standards.ExecutedEvidence(kind) {
		return standards.Evidence{}, fmt.Errorf("%w: %s", ErrExecutedEvidence, kind)
	}
	if strings.TrimSpace(detail) == "" {
		return standards.Evidence{}, errors.New("evidence with no content is not evidence")
	}
	return standards.Evidence{Kind: kind, Detail: detail, Observed: true}, nil
}

// Finding is what a detector concluded about one criterion.
type Finding struct {
	StandardID string
	// Result is UNMET, PARTIAL or UNKNOWN. A static read cannot settle a
	// criterion upward, so MET is not among them.
	Result string
	// DefectKind is a stable slug naming what is wrong. It goes into the dedup
	// key, so it must describe the defect and not the wording of this report.
	DefectKind string
	// TargetScope is where the defect lives, which is both the second half of
	// the dedup key and the change scope a generated work item arrives with.
	TargetScope string
	Detail      string
	Evidence    []standards.Evidence
}

// Detector examines a tree for one criterion.
type Detector interface {
	StandardID() string
	Detect(tree Tree) (Finding, error)
}

// Observation is one pass over a repository.
type Observation struct {
	ProjectID   string
	CommitSHA   string
	ToolVersion string
	Findings    []Finding
	// Unchecked lists the criteria in force that no detector covers, so the
	// report distinguishes "we looked and found nothing wrong" from "nobody
	// looked". A list that silently omits the second reads as coverage.
	Unchecked []string
}

// gitTree reads a real repository at a commit.
type gitTree struct {
	ctx        context.Context
	repository string
	sha        string
	paths      []string
	cache      map[string]string
}

// Open pins a repository at a commit for reading.
func Open(ctx context.Context, repository, sha string) (Tree, error) {
	paths, err := gitops.ListTree(ctx, repository, sha)
	if err != nil {
		return nil, err
	}
	return &gitTree{ctx: ctx, repository: repository, sha: sha, paths: paths, cache: map[string]string{}}, nil
}

func (t *gitTree) Paths() []string { return t.paths }

func (t *gitTree) File(name string) (string, error) {
	if body, ok := t.cache[name]; ok {
		return body, nil
	}
	body, err := gitops.FileAt(t.ctx, t.repository, t.sha, name, 0)
	if err != nil {
		return "", err
	}
	t.cache[name] = body
	return body, nil
}

// MemoryTree is a repository composed in a test.
type MemoryTree map[string]string

func (m MemoryTree) Paths() []string {
	paths := make([]string, 0, len(m))
	for name := range m {
		paths = append(paths, name)
	}
	return paths
}

func (m MemoryTree) File(name string) (string, error) {
	body, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%s: %w", name, gitops.ErrPathNotInTree)
	}
	return body, nil
}

// Observe runs every detector whose criterion is in force.
//
// A detector for a criterion that does not apply is not run at all: reporting
// a gap a project does not have buries the gaps it does.
func Observe(tree Tree, inForce []standards.Standard, detectors []Detector) (Observation, error) {
	byID := map[string]Detector{}
	for _, detector := range detectors {
		byID[detector.StandardID()] = detector
	}
	var observation Observation
	for _, standard := range inForce {
		detector, covered := byID[standard.ID]
		if !covered {
			observation.Unchecked = append(observation.Unchecked, standard.ID)
			continue
		}
		finding, err := detector.Detect(tree)
		if err != nil {
			return observation, fmt.Errorf("%s: %w", standard.ID, err)
		}
		finding.StandardID = standard.ID
		switch finding.Result {
		case standards.ResultUnmet, standards.ResultPartial, standards.ResultUnknown:
		default:
			// A static detector claiming MET or NOT_APPLICABLE is a bug in the
			// detector, and one that would otherwise pass quietly into an
			// assessment.
			return observation, fmt.Errorf("%s: a static detector may not return %s", standard.ID, finding.Result)
		}
		observation.Findings = append(observation.Findings, finding)
	}
	return observation, nil
}

// matches reports whether any path in the tree matches a glob, supporting the
// trailing "/**" the criteria use for "anything under here".
func matches(tree Tree, pattern string) []string {
	var found []string
	recursive := strings.HasSuffix(pattern, "/**")
	prefix := strings.TrimSuffix(pattern, "/**")
	for _, candidate := range tree.Paths() {
		if recursive {
			if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
				found = append(found, candidate)
			}
			continue
		}
		if ok, _ := path.Match(pattern, candidate); ok {
			found = append(found, candidate)
		}
	}
	return found
}

// hasPath reports whether an exact path exists.
func hasPath(tree Tree, name string) bool {
	for _, candidate := range tree.Paths() {
		if candidate == name {
			return true
		}
	}
	return false
}
