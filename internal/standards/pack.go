// Package standards holds the common development criteria a project is
// measured against, the project's own profile of which of them apply, and the
// assessment of where the repository currently stands.
//
// The point of writing criteria down as data rather than as a prompt is that a
// prompt cannot be checked. A criterion here carries what would count as
// evidence and what result is allowed to follow from it, so "이 항목은
// 충족되었다" is a claim the program can refuse.
package standards

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Severity says how a criterion behaves when it is not met.
const (
	// SeverityRequired is a criterion a project cannot ship without. Excepting
	// one needs a decider and a review condition, not just a reason.
	SeverityRequired = "required"
	// SeverityRecommended is a criterion whose absence is a finding, not a
	// blocker.
	SeverityRecommended = "recommended"
)

// Assessment results.
//
// The five are distinguished because the action each one calls for is
// different: UNMET is work, PARTIAL is smaller work, UNKNOWN is an
// investigation, and NOT_APPLICABLE is a decision somebody has to have made.
// Collapsing UNKNOWN into UNMET files work nobody verified was needed;
// collapsing it into MET ships on a guess.
const (
	ResultMet           = "MET"
	ResultUnmet         = "UNMET"
	ResultPartial       = "PARTIAL"
	ResultUnknown       = "UNKNOWN"
	ResultNotApplicable = "NOT_APPLICABLE"
)

// executedEvidence lists the evidence kinds that can only come from running or
// producing something.
//
// The rest — a commit SHA, a route — locate the thing under discussion without
// saying anything about whether it works. A required criterion whose evidence
// is all locators is settled by reading, and reading is how a screen with a
// save button wired to a stub passes a criterion about saving.
var executedEvidence = map[string]bool{
	"browser_test_result":     true,
	"integration_test_result": true,
	"security_test_result":    true,
	"build_log":               true,
	"release_asset":           true,
	"screenshot":              true,
	"test_result":             true,
	"performance_result":      true,
}

// ExecutedEvidence reports whether a kind of evidence requires having run or
// produced something rather than having read it.
func ExecutedEvidence(kind string) bool {
	return executedEvidence[strings.ToLower(strings.TrimSpace(kind))]
}

// Check is one way a criterion can be settled.
type Check struct {
	// Type names the kind of evidence: static, browser_journey, build, test,
	// integration, security, performance, manual.
	Type string `json:"type" yaml:"type"`
	// Assertion is what the check has to show, written so a person reading a
	// failure knows what was expected.
	Assertion string `json:"assertion" yaml:"assertion"`
}

// Standard is one criterion.
type Standard struct {
	ID       string `json:"id" yaml:"id"`
	Revision int    `json:"revision" yaml:"revision"`
	Title    string `json:"title" yaml:"title"`
	Category string `json:"category" yaml:"category"`
	Severity string `json:"severity" yaml:"severity"`
	Intent   string `json:"intent" yaml:"intent"`
	// AppliesWhen narrows the criterion to projects whose profile matches every
	// key. An empty map applies everywhere.
	AppliesWhen map[string]string `json:"applies_when,omitempty" yaml:"applies_when,omitempty"`
	Checks      []Check           `json:"checks" yaml:"checks"`
	// EvidenceRequired lists what an assessment must carry before this
	// criterion may be called MET. It is the difference between reading a
	// README and running the thing.
	EvidenceRequired []string `json:"evidence_required" yaml:"evidence_required"`
	// ChangeScopeHint is where work on this criterion is expected to land, so a
	// generated candidate arrives with a scope instead of the whole repository.
	ChangeScopeHint []string `json:"change_scope_hint,omitempty" yaml:"change_scope_hint,omitempty"`
}

// Validate refuses a criterion that cannot be judged.
//
// A criterion with no check is a sentence in a document: it can be quoted at
// people and never settled, and a catalogue full of them looks like coverage
// while measuring nothing.
func (s Standard) Validate() error {
	switch {
	case strings.TrimSpace(s.ID) == "":
		return errors.New("criterion ID is required")
	case strings.TrimSpace(s.Title) == "":
		return fmt.Errorf("%s: title is required", s.ID)
	case s.Revision < 1:
		return fmt.Errorf("%s: revision must start at 1", s.ID)
	case s.Severity != SeverityRequired && s.Severity != SeverityRecommended:
		return fmt.Errorf("%s: severity must be %s or %s", s.ID, SeverityRequired, SeverityRecommended)
	case strings.TrimSpace(s.Intent) == "":
		// The intent is what a generated work card quotes to explain why the
		// work exists. Without it the card says "NET-002 미충족" and sends
		// whoever picks it up back to the catalogue.
		return fmt.Errorf("%s: intent is required — a criterion that cannot say what it is for cannot explain itself on a card", s.ID)
	case len(s.Checks) == 0:
		return fmt.Errorf("%s: a criterion with no check cannot be judged, only quoted", s.ID)
	case len(s.EvidenceRequired) == 0:
		return fmt.Errorf("%s: name what evidence would settle this, or it will be settled by opinion", s.ID)
	}
	for i, check := range s.Checks {
		if strings.TrimSpace(check.Type) == "" || strings.TrimSpace(check.Assertion) == "" {
			return fmt.Errorf("%s: check %d needs both a type and an assertion", s.ID, i+1)
		}
	}
	if s.Severity == SeverityRequired && !s.asksForExecutedEvidence() {
		return fmt.Errorf("%s: a required criterion must ask for evidence that comes from running something, not only %s",
			s.ID, strings.Join(s.EvidenceRequired, ", "))
	}
	return nil
}

func (s Standard) asksForExecutedEvidence() bool {
	for _, kind := range s.EvidenceRequired {
		if ExecutedEvidence(kind) {
			return true
		}
	}
	return false
}

// AppliesTo reports whether this criterion applies to a project whose profile
// declares the given attributes.
func (s Standard) AppliesTo(attributes map[string]string) bool {
	for key, want := range s.AppliesWhen {
		if !strings.EqualFold(strings.TrimSpace(attributes[key]), strings.TrimSpace(want)) {
			return false
		}
	}
	return true
}

// Pack is a versioned set of criteria.
//
// The version is what a project pins. A new version proposes a difference and
// does not silently re-judge work that was done under the old one — a
// catalogue that changes underneath a project turns yesterday's finished work
// into today's finding, with nothing in the repository having changed.
type Pack struct {
	ID        string     `json:"id" yaml:"id"`
	Version   string     `json:"version" yaml:"version"`
	Title     string     `json:"title" yaml:"title"`
	Source    string     `json:"source,omitempty" yaml:"source,omitempty"`
	Standards []Standard `json:"standards" yaml:"standards"`
}

// Ref is the "id@version" a project pins.
func (p Pack) Ref() string { return p.ID + "@" + p.Version }

// Validate refuses a pack that cannot be applied.
func (p Pack) Validate() error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Version) == "" {
		return errors.New("pack ID and version are required")
	}
	if len(p.Standards) == 0 {
		return fmt.Errorf("%s: a pack with no criteria applies nothing", p.Ref())
	}
	seen := map[string]bool{}
	for _, standard := range p.Standards {
		if err := standard.Validate(); err != nil {
			return fmt.Errorf("%s: %w", p.Ref(), err)
		}
		if seen[standard.ID] {
			// Two criteria with one ID means an assessment cannot say which one
			// it settled, and an exception cannot say which one it excuses.
			return fmt.Errorf("%s: %s appears twice", p.Ref(), standard.ID)
		}
		seen[standard.ID] = true
	}
	return nil
}

// Standard finds a criterion by ID.
func (p Pack) Standard(id string) (Standard, bool) {
	for _, standard := range p.Standards {
		if standard.ID == id {
			return standard, true
		}
	}
	return Standard{}, false
}

// Checksum is a stable digest of the pack's content, so a project can tell
// whether the catalogue it pinned is the catalogue it is being judged by.
func (p Pack) Checksum() string { return contentDigest(p) }

// Diff is what changed between two versions of a pack, expressed as the three
// things a project has to decide about.
type Diff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	// Revised lists criteria whose revision number moved, which is the set a
	// project's existing assessments have to be re-run against.
	Revised []string `json:"revised"`
}

// Empty reports whether the two versions ask for the same things.
func (d Diff) Empty() bool { return len(d.Added)+len(d.Removed)+len(d.Revised) == 0 }

// DiffPacks compares two pack versions.
func DiffPacks(from, to Pack) Diff {
	diff := Diff{Added: []string{}, Removed: []string{}, Revised: []string{}}
	previous := map[string]int{}
	for _, standard := range from.Standards {
		previous[standard.ID] = standard.Revision
	}
	for _, standard := range to.Standards {
		revision, existed := previous[standard.ID]
		switch {
		case !existed:
			diff.Added = append(diff.Added, standard.ID)
		case revision != standard.Revision:
			diff.Revised = append(diff.Revised, standard.ID)
		}
		delete(previous, standard.ID)
	}
	for id := range previous {
		diff.Removed = append(diff.Removed, id)
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Revised)
	return diff
}
