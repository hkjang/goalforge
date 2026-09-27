package policy

import "testing"

// The asymmetry is the requirement: a finished screen in front of a stub
// compiles and builds green, so build evidence cannot settle a criterion that
// asked whether the user's task actually completes.
func TestEvidenceSatisfies(t *testing.T) {
	for _, tc := range []struct {
		name, required, produced string
		satisfied                bool
	}{
		{"journey needs more than a build", KindJourney, KindBuild, false},
		{"journey needs more than unit tests", KindJourney, KindTest, false},
		{"a journey settles a build requirement", KindBuild, KindJourney, true},
		{"integration settles a test requirement", KindTest, KindIntegration, true},
		{"a test does not settle integration", KindIntegration, KindTest, false},
		{"exact match", KindJourney, KindJourney, true},
		{"security is its own property", KindSecurity, KindJourney, false},
		{"nothing implies performance", KindPerformance, KindIntegration, false},
		{"an unstated requirement takes anything", "", KindBuild, true},
		{"unclassified evidence cannot prove a kind", KindJourney, "", false},
		{"review does not stand in for a journey", KindJourney, KindReview, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvidenceSatisfies(tc.required, tc.produced); got != tc.satisfied {
				t.Fatalf("EvidenceSatisfies(%q,%q)=%t want %t", tc.required, tc.produced, got, tc.satisfied)
			}
		})
	}
}

func TestValidGateKind(t *testing.T) {
	if err := ValidGateKind(""); err != nil {
		t.Fatalf("an unclassified gate is allowed: %v", err)
	}
	if err := ValidGateKind("journey"); err != nil {
		t.Fatalf("journey is a kind: %v", err)
	}
	if err := ValidGateKind("vibes"); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}
