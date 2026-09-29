package standards

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Exception is a criterion a project has decided not to apply.
//
// It carries a decider and a review condition because an exception without
// them is indistinguishable from an omission. "우리는 이걸 안 합니다" said by
// nobody, reviewable never, is how a required criterion quietly stops being
// required.
type Exception struct {
	StandardID string `json:"standard_id"`
	Reason     string `json:"reason"`
	Decider    string `json:"decider"`
	// Scope narrows the exception to part of the repository. Empty means the
	// whole project.
	Scope string `json:"scope,omitempty"`
	// ReviewBy is when the exception stops being taken on trust. One of
	// ReviewBy and ReviewWhen is required for a required-severity criterion.
	ReviewBy   time.Time `json:"review_by,omitempty"`
	ReviewWhen string    `json:"review_when,omitempty"`
	DecidedAt  time.Time `json:"decided_at"`
}

// Expired reports whether a dated exception has passed its review date.
func (e Exception) Expired(now time.Time) bool {
	return !e.ReviewBy.IsZero() && now.After(e.ReviewBy)
}

// Profile is what a project has declared about itself and which pack version
// it is pinned to.
type Profile struct {
	ProjectID string `json:"project_id"`
	// PackRef is "id@version". It is pinned: publishing a new pack version
	// proposes a difference rather than re-judging this project.
	PackRef string `json:"pack_ref"`
	// Attributes are the declared facts a criterion's applies_when is matched
	// against — frontend, network, auth, ai, and so on.
	Attributes map[string]string `json:"attributes"`
	Exceptions []Exception       `json:"exceptions"`
}

// Validate refuses a profile that cannot be applied.
func (p Profile) Validate(pack Pack) error {
	if strings.TrimSpace(p.ProjectID) == "" {
		return errors.New("project is required")
	}
	if p.PackRef != pack.Ref() {
		return fmt.Errorf("profile pins %s but the pack is %s", p.PackRef, pack.Ref())
	}
	for _, exception := range p.Exceptions {
		standard, known := pack.Standard(exception.StandardID)
		if !known {
			// An exception for a criterion the pack does not contain excuses
			// nothing and hides a typo that looks like a decision.
			return fmt.Errorf("%s: no such criterion in %s", exception.StandardID, pack.Ref())
		}
		if err := validateException(standard, exception); err != nil {
			return err
		}
	}
	return nil
}

func validateException(standard Standard, exception Exception) error {
	if strings.TrimSpace(exception.Reason) == "" {
		return fmt.Errorf("%s: an exception needs a reason", exception.StandardID)
	}
	if strings.TrimSpace(exception.Decider) == "" {
		return fmt.Errorf("%s: an exception needs a decider — one nobody owns is an omission", exception.StandardID)
	}
	if standard.Severity == SeverityRequired && exception.ReviewBy.IsZero() && strings.TrimSpace(exception.ReviewWhen) == "" {
		// A required criterion may be excepted, but not permanently by
		// default. Without a review condition the exception outlives the
		// circumstances that justified it and nobody notices.
		return fmt.Errorf("%s: a required criterion's exception needs a review date or condition", exception.StandardID)
	}
	return nil
}

// ExceptionFor returns the live exception for a criterion, if there is one.
func (p Profile) ExceptionFor(standardID string, now time.Time) (Exception, bool) {
	for _, exception := range p.Exceptions {
		if exception.StandardID != standardID {
			continue
		}
		if exception.Expired(now) {
			// An expired exception is not an exception. Treating it as one is
			// how a six-week waiver becomes permanent.
			return Exception{}, false
		}
		return exception, true
	}
	return Exception{}, false
}

// Applicable is one criterion as it applies to this project.
type Applicable struct {
	Standard  Standard
	Excepted  bool
	Exception Exception
	// OutOfProfile is true when the criterion's applies_when does not match
	// this project. It is reported separately from an exception because the
	// two mean different things: one is a fact about the project, the other is
	// a decision somebody made.
	OutOfProfile bool
}

// Apply works out which criteria apply to a project and why the rest do not.
func Apply(pack Pack, profile Profile, now time.Time) []Applicable {
	result := make([]Applicable, 0, len(pack.Standards))
	for _, standard := range pack.Standards {
		entry := Applicable{Standard: standard}
		if !standard.AppliesTo(profile.Attributes) {
			entry.OutOfProfile = true
		} else if exception, ok := profile.ExceptionFor(standard.ID, now); ok {
			entry.Excepted, entry.Exception = true, exception
		}
		result = append(result, entry)
	}
	return result
}

// InForce lists the criteria this project is actually held to.
func InForce(pack Pack, profile Profile, now time.Time) []Standard {
	var result []Standard
	for _, entry := range Apply(pack, profile, now) {
		if !entry.OutOfProfile && !entry.Excepted {
			result = append(result, entry.Standard)
		}
	}
	return result
}
