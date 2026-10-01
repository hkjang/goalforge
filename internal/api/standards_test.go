package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	"github.com/goalforge/goalforge/internal/patterns"
	"github.com/goalforge/goalforge/internal/standards"
)

// A project that never pinned a pack gets a screen that says so. An empty
// table reads as "nothing wrong", which is the opposite of the truth.
func TestAProjectOutsideTheProgrammeSaysSo(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	var view StandardsView
	get(t, server, "/api/v1/projects/P-API/standards", &view)
	if view.Enrolled {
		t.Fatalf("view=%+v", view)
	}
	if view.Criteria == nil || view.Counts == nil {
		t.Fatal("the screen must get empty collections rather than null")
	}
}

// The screen reads snake_case. A struct served under its Go names gives the
// screen undefined for every column, which renders as an empty table rather
// than an error — so nothing says anything is wrong.
func TestTheStandardsScreenGetsTheNamesItReads(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := t.Context()
	pack := standards.GoReactOfflineService()
	profile := standards.Profile{ProjectID: "P-API", PackRef: pack.Ref(),
		Attributes: map[string]string{"frontend": "react", "network": "offline", "deployment": "service"}}
	if err := db.SaveStandardProfile(ctx, profile, pack); err != nil {
		t.Fatal(err)
	}
	var view StandardsView
	get(t, server, "/api/v1/projects/P-API/standards", &view)
	if !view.Enrolled || view.PackRef != pack.Ref() {
		t.Fatalf("view=%+v", view)
	}
	if len(view.Criteria) == 0 {
		t.Fatal("a react offline service is held to most of this pack")
	}
	first := view.Criteria[0]
	if first.StandardID == "" || first.Title == "" || first.Severity == "" {
		t.Fatalf("every column the screen reads must be populated: %+v", first)
	}
	if len(view.OutOfProfile) == 0 {
		t.Fatal("criteria that do not apply are named, so a reader can tell them from not-done")
	}
}

// The fleet view is served to a screen too, and the same naming applies.
func TestTheFleetViewGetsTheNamesItReads(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	ctx := t.Context()
	pack := standards.GoReactOfflineService()
	for _, id := range []string{"P-API", "P-TWO"} {
		if id != "P-API" {
			if err := db.CreateProject(ctx, model.Project{ID: id, Name: id, RepositoryPath: "/r",
				DefaultBranch: "main", Provider: "codex"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.SaveStandardProfile(ctx, standards.Profile{ProjectID: id, PackRef: pack.Ref(),
			Attributes: map[string]string{"frontend": "react", "deployment": "service"}}, pack); err != nil {
			t.Fatal(err)
		}
	}
	var view FleetView
	get(t, server, "/api/v1/fleet", &view)
	if len(view.Projects) != 2 || len(view.Criteria) == 0 {
		t.Fatalf("view=%+v", view)
	}
	if view.Proposals == nil {
		t.Fatal("an empty proposal list must be a list, not null")
	}
	// Decoding into the same struct round-trips whatever the field names are,
	// so it cannot see a naming mismatch — the screen reads raw keys and gets
	// undefined, which renders as an empty table rather than an error. This
	// class of bug has reached a browser twice; the keys are asserted here.
	raw := rawJSON(t, server, "/api/v1/fleet")
	for _, key := range []string{"pack_ref", "projects", "criteria", "proposals"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("the screen reads %q: %v", key, keysOf(raw))
		}
	}
	criteria, _ := raw["criteria"].([]any)
	if len(criteria) == 0 {
		t.Fatal("criteria must not be empty")
	}
	first, _ := criteria[0].(map[string]any)
	for _, key := range []string{"standard_id", "title", "severity", "applicable", "met", "unmet", "unknown", "excepted"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("the screen reads %q from each criterion: %v", key, keysOf(first))
		}
	}
}

// rawJSON reads a response as plain keys rather than into a struct.
func rawJSON(t *testing.T, server *Server, path string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s: status=%d", path, recorder.Code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func keysOf(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// The patterns screen reads the same way.
func TestThePatternsScreenGetsTheNamesItReads(t *testing.T) {
	server, db := apiFixture(t, "")
	defer db.Close()
	if err := db.SavePattern(t.Context(), patterns.Pattern{ID: "PAT-1", StandardID: "NET-002",
		Problem: "p", Approach: "a", Status: patterns.StatusCandidate}); err != nil {
		t.Fatal(err)
	}
	raw := rawJSON(t, server, "/api/v1/patterns")
	list, _ := raw["patterns"].([]any)
	if len(list) != 1 {
		t.Fatalf("patterns=%v", raw)
	}
	entry, _ := list[0].(map[string]any)
	for _, key := range []string{"id", "standard_id", "problem", "approach", "status", "evidence"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("the screen reads %q: %v", key, keysOf(entry))
		}
	}
	evidence, _ := entry["evidence"].(map[string]any)
	for _, key := range []string{"projects", "passed", "failed", "recent_failures"} {
		if _, ok := evidence[key]; !ok {
			t.Fatalf("the screen reads evidence.%q: %v", key, keysOf(evidence))
		}
	}
}
