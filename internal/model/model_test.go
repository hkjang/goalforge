package model

import "testing"

func TestParseCriterion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Criterion
	}{
		{"without kind", "build_passed=true", Criterion{Type: "build_passed", ExpectedValue: "true"}},
		{"whitespace and mixed case kind", " SaveNote @ JoUrNeY = true ", Criterion{Type: "SaveNote", ExpectedValue: "true", RequiredKind: "journey"}},
		{"comparison value", "latency@test=<=200ms", Criterion{Type: "latency", ExpectedValue: "<=200ms", RequiredKind: "test"}},
		{"delimiters in value", "payload@review=a=b@c", Criterion{Type: "payload", ExpectedValue: "a=b@c", RequiredKind: "review"}},
		{"preserve type and value case", " SaveNote = TrUe ", Criterion{Type: "SaveNote", ExpectedValue: "TrUe"}},
		// Syntax parsing accepts unknown kinds; the store validates policy.
		{"unknown kind", "x@unregistered=true", Criterion{Type: "x", ExpectedValue: "true", RequiredKind: "unregistered"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCriterion(tt.raw)
			if err != nil {
				t.Fatalf("ParseCriterion(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseCriterion(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}

	for _, raw := range []string{"", "x", "=true", "x=", "x=   ", "@test=true", "x@=true"} {
		t.Run("reject/"+raw, func(t *testing.T) {
			if _, err := ParseCriterion(raw); err == nil {
				t.Errorf("ParseCriterion(%q) must reject malformed syntax", raw)
			}
		})
	}
}
