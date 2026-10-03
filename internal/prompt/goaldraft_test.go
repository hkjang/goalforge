package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

// The command policy blocks the shell, and the side that fills gate_command was
// never told. A drafter needing two commands reached for sh -c "a && b" and
// every criterion was refused — three repair rounds spent on a rule it had not
// been given.
func TestTheDraftSchemaForbidsShellCommands(t *testing.T) {
	// Decoded rather than matched against the raw JSON: the quotes in the
	// example are escaped there, and a test that searched the encoded form
	// would pass or fail on the encoding rather than on what the model reads.
	var decoded struct {
		Properties struct {
			Criteria struct {
				Items struct {
					Properties struct {
						GateCommand struct {
							Description string `json:"description"`
						} `json:"gate_command"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"criteria"`
			Health struct {
				Properties struct {
					GateCommand struct {
						Description string `json:"description"`
					} `json:"gate_command"`
				} `json:"properties"`
			} `json:"health"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(GoalDraftSchema()), &decoded); err != nil {
		t.Fatal(err)
	}
	for name, description := range map[string]string{
		"criteria": decoded.Properties.Criteria.Items.Properties.GateCommand.Description,
		"health":   decoded.Properties.Health.Properties.GateCommand.Description,
	} {
		if !strings.Contains(description, "셸") {
			t.Fatalf("%s must say the shell is blocked: %q", name, description)
		}
		// An example of the shape wanted, because "arguments separately" is
		// advice and an array is a shape.
		if !strings.Contains(description, `["go",`) {
			t.Fatalf("%s must show the shape: %q", name, description)
		}
	}
}

// The rules say it too, with the reason a second command means a second
// criterion rather than a shell.
func TestTheDraftRulesExplainWhyOneGateMeasuresOneThing(t *testing.T) {
	text := GoalDraft("주제", "cmd/ (3)", nil)
	for _, want := range []string{"셸을 쓸 수 없다", "기준이 둘"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the rules must say %q:\n%s", want, text)
		}
	}
}
