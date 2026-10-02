package prompt

import (
	"encoding/json"
	"regexp"
	"testing"
)

// The schema is what makes a prose scope impossible rather than unlikely.
// Asked for as a sentence, it came back as one — and every work item filed
// that way could never run.
func TestTheIdeasSchemaRefusesAProseScope(t *testing.T) {
	raw := IdeasSchema()
	var decoded struct {
		Properties struct {
			Ideas struct {
				Items struct {
					Properties struct {
						Scope struct {
							Pattern     string `json:"pattern"`
							Description string `json:"description"`
						} `json:"expected_change_scope"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"ideas"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	scope := decoded.Properties.Ideas.Items.Properties.Scope
	if scope.Pattern == "" {
		t.Fatalf("no pattern, so a sentence is as valid as a path: %s", raw)
	}
	if scope.Description == "" {
		t.Fatal("the model has to be told what the field is for")
	}
	compiled, err := regexp.Compile(scope.Pattern)
	if err != nil {
		t.Fatalf("the pattern must be a usable regexp: %v", err)
	}
	for _, prose := range []string{"handler.go 신설: JSON 본문 파싱", "store.go 를 바꾼다",
		"내부 저장소를 추가한다"} {
		if compiled.MatchString(prose) {
			t.Fatalf("the pattern must refuse %q", prose)
		}
	}
	for _, path := range []string{"handler.go", "internal/store/**,cmd/app/main.go",
		"web/src/*.tsx", "a_b-c.go"} {
		if !compiled.MatchString(path) {
			t.Fatalf("the pattern must admit %q", path)
		}
	}
}
