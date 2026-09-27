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

// gateKindStrength orders kinds by what they can establish. A criterion asking
// for journey evidence is not satisfied by a build, but one asking for build
// evidence is satisfied by a journey that necessarily compiled first.
var gateKindStrength = map[string]int{
	KindBuild: 1, KindTest: 2, KindIntegration: 3, KindJourney: 4,
	KindSecurity: 2, KindPerformance: 2, KindReview: 1,
}

// KnownGateKinds lists the kinds a gate may declare.
func KnownGateKinds() []string {
	kinds := make([]string, 0, len(gateKindStrength))
	for kind := range gateKindStrength {
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
	if _, ok := gateKindStrength[strings.ToLower(kind)]; !ok {
		return fmt.Errorf("gate kind must be one of %s", strings.Join(KnownGateKinds(), ", "))
	}
	return nil
}

// EvidenceSatisfies reports whether evidence produced by a gate of one kind
// can settle a criterion that asked for another.
//
// The asymmetry is the point. A criterion that wants a user journey is not
// settled by a build: a screen can be finished, the build green, and the save
// button wired to nothing. A criterion that only wants a build is settled by a
// journey, because the journey could not have run otherwise.
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
	// Security and performance are specific properties rather than a rung on
	// the behavioural ladder: nothing else establishes them.
	if required == KindSecurity || required == KindPerformance {
		return false
	}
	if produced == KindSecurity || produced == KindPerformance {
		return false
	}
	return gateKindStrength[produced] >= gateKindStrength[required]
}
