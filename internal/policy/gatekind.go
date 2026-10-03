package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Gate kinds describe what a check actually establishes. A build succeeding
// says the code compiles; it says nothing about whether the feature works.
// Recording the difference is what lets a criterion insist on evidence of the
// kind that would settle it.
const (
	// KindBuild means it compiles, installs, or type-checks.
	KindBuild = "build"
	// KindTest means unit or component behaviour was exercised.
	KindTest = "test"
	// KindIntegration means parts were exercised together.
	KindIntegration = "integration"
	// KindJourney means a user's actual task was carried out end to end —
	// the only kind that catches a finished screen in front of a stub.
	KindJourney = "journey"
	// KindSecurity and KindPerformance are measured properties rather than
	// behaviour.
	KindSecurity    = "security"
	KindPerformance = "performance"
	// KindReview is a human or model judgement. It can support a criterion but
	// never substitutes for an objective check that exists.
	KindReview = "review"
)

// gateKindStrength orders the kinds that form one behavioural ladder by what
// they can establish. A criterion asking for journey evidence is not satisfied
// by a build, but one asking for build evidence is satisfied by a journey that
// necessarily compiled first.
var gateKindStrength = map[string]int{
	KindBuild: 1, KindTest: 2, KindIntegration: 3, KindJourney: 4,
}

// offLadderKinds are the kinds that are not rungs on that ladder, so nothing
// else establishes them and they establish nothing else. Security and
// performance are measured properties rather than degrees of behaviour. Review
// belongs here for a sharper reason: it is a judgement, and the session being
// judged is usually the one that wrote the code, so letting a judgement stand
// in for an objective check is how a run certifies its own work. Listing them
// here instead of giving them a rung number is deliberate — a number invites
// the comparison, and the numbers they used to carry were never read.
var offLadderKinds = map[string]bool{KindSecurity: true, KindPerformance: true, KindReview: true}

// KnownGateKinds lists the kinds a gate may declare.
func KnownGateKinds() []string {
	kinds := make([]string, 0, len(gateKindStrength)+len(offLadderKinds))
	for kind := range gateKindStrength {
		kinds = append(kinds, kind)
	}
	for kind := range offLadderKinds {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// ValidGateKind reports whether a kind is one GoalForge understands. An empty
// kind is allowed and means "unclassified", which is what every gate written
// before kinds existed is.
func ValidGateKind(kind string) error {
	if strings.TrimSpace(kind) == "" {
		return nil
	}
	kind = strings.ToLower(kind)
	if _, onLadder := gateKindStrength[kind]; onLadder || offLadderKinds[kind] {
		return nil
	}
	return fmt.Errorf("gate kind must be one of %s", strings.Join(KnownGateKinds(), ", "))
}

// EvidenceSatisfies reports whether evidence produced by a gate of one kind
// can settle a criterion that asked for another.
//
// The asymmetry is the point. A criterion that wants a user journey is not
// settled by a build: a screen can be finished, the build green, and the save
// button wired to nothing. A criterion that only wants a build is settled by a
// journey, because the journey could not have run otherwise.
//
// A kind that is not on that ladder settles nothing but itself, in either
// direction. That is what keeps a review — a judgement, usually by the same
// session that wrote the code — from standing in for a check that was never
// run, and keeps a check that ran from claiming anyone looked at it.
func EvidenceSatisfies(required, produced string) bool {
	required, produced = strings.ToLower(strings.TrimSpace(required)), strings.ToLower(strings.TrimSpace(produced))
	if required == "" {
		return true
	}
	if produced == "" {
		// Evidence from an unclassified gate cannot be shown to be of the kind
		// the criterion needs. Saying otherwise would let every pre-existing
		// gate satisfy a journey requirement by default.
		return false
	}
	if required == produced {
		return true
	}
	if offLadderKinds[required] || offLadderKinds[produced] {
		return false
	}
	return gateKindStrength[produced] >= gateKindStrength[required]
}
