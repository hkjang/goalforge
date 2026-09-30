package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// player is a stand-in for playwright-player: the same envelope, the same
// status values, the same report shape.
type player struct {
	createStatus int
	statuses     []string
	stats        map[string]any
	artifacts    []map[string]any
	calls        int
	lastBody     map[string]any
}

func (p *player) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, 200, map[string]any{"service": "playwright-player", "version": "1.0.0"})
	})
	mux.HandleFunc("/api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&p.lastBody)
		status := p.createStatus
		if status == 0 {
			status = 201
		}
		writeOK(w, status, map[string]any{"runId": "RUN-1", "status": "running"})
	})
	mux.HandleFunc("/api/runs/RUN-1", func(w http.ResponseWriter, r *http.Request) {
		status := "completed"
		if p.calls < len(p.statuses) {
			status = p.statuses[p.calls]
		} else if len(p.statuses) > 0 {
			status = p.statuses[len(p.statuses)-1]
		}
		p.calls++
		writeOK(w, 200, map[string]any{"runId": "RUN-1", "status": status, "exitCode": 0,
			"summary": map[string]any{"stats": p.stats}})
	})
	mux.HandleFunc("/api/runs/RUN-1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		writeOK(w, 200, p.artifacts)
	})
	return mux
}

func writeOK(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func passing() *player {
	return &player{statuses: []string{"running", "completed"},
		stats: map[string]any{"expected": float64(3), "unexpected": float64(0), "flaky": float64(0), "skipped": float64(0)},
		artifacts: []map[string]any{
			{"fileName": "home.png", "relativePath": "screenshots/home.png", "sizeBytes": float64(4096)},
			// A file the browser opened and never wrote to. Claiming it as
			// evidence would be claiming a picture nobody took.
			{"fileName": "empty.png", "relativePath": "screenshots/empty.png", "sizeBytes": float64(0)},
			{"fileName": "trace.zip", "relativePath": "trace.zip", "sizeBytes": float64(120)},
		}}
}

func clientFor(t *testing.T, p *player) (Client, func()) {
	t.Helper()
	server := httptest.NewServer(p.handler(t))
	return Client{BaseURL: server.URL, PollInterval: time.Millisecond, Timeout: 5 * time.Second}, server.Close
}

// The failure this whole client exists to avoid. On an isolated network the
// service is the first thing that stops being reachable, and a client that
// reported "no failures" when it could not connect would turn every criterion
// it settles green at exactly the moment nothing is being checked.
func TestAnUnreachableServiceIsNotAPass(t *testing.T) {
	client := Client{BaseURL: "http://127.0.0.1:1", PollInterval: time.Millisecond, Timeout: time.Second}
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore"})
	if err == nil {
		t.Fatal("an unreachable service must be an error, not an empty pass")
	}
	if result.Passed {
		t.Fatal("result must not read as passed")
	}
	if !strings.Contains(err.Error(), "playwright-player") {
		t.Fatalf("the error must name what could not be reached: %v", err)
	}
}

// A run that executed nothing is the classic false green: the process exits
// zero because there was nothing to fail.
func TestARunThatExecutedNothingIsNotAPass(t *testing.T) {
	p := passing()
	p.stats = map[string]any{"expected": float64(0), "unexpected": float64(0), "skipped": float64(0)}
	client, done := clientFor(t, p)
	defer done()
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatal("zero tests executed is not a pass")
	}
	if !strings.Contains(result.Detail, "실행된 검사가 없습니다") {
		t.Fatalf("detail=%q", result.Detail)
	}
}

// A run everything skipped is the same failure wearing a different number.
func TestARunThatOnlySkippedIsNotAPass(t *testing.T) {
	p := passing()
	p.stats = map[string]any{"expected": float64(0), "unexpected": float64(0), "skipped": float64(4)}
	client, done := clientFor(t, p)
	defer done()
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatal("four skipped tests established nothing")
	}
}

// A real pass.
func TestAPassingRunReportsWhatItRanAndWhatItProduced(t *testing.T) {
	p := passing()
	client, done := clientFor(t, p)
	defer done()
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore",
		Project: "chromium", BaseURL: "http://internal.example", Variables: map[string]string{"locale": "ko-KR"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatalf("result=%+v", result)
	}
	if result.Expected != 3 {
		t.Fatalf("expected=%d", result.Expected)
	}
	if len(result.Screenshots) != 1 || result.Screenshots[0] != "screenshots/home.png" {
		t.Fatalf("a zero-byte file is not a screenshot and a trace is not one either: %v", result.Screenshots)
	}
	if len(result.Artifacts) != 3 {
		t.Fatalf("every artifact is still reported: %+v", result.Artifacts)
	}
	// The request reaches the service in the shape it documents.
	if p.lastBody["scriptKey"] != "ux/route-restore" || p.lastBody["project"] != "chromium" {
		t.Fatalf("body=%+v", p.lastBody)
	}
	if p.lastBody["baseURL"] != "http://internal.example" {
		t.Fatalf("body=%+v", p.lastBody)
	}
}

// A failing run is a failing result, with the count of what went wrong.
func TestAFailingRunIsReportedAsFailing(t *testing.T) {
	p := passing()
	p.statuses = []string{"failed"}
	p.stats = map[string]any{"expected": float64(2), "unexpected": float64(1), "skipped": float64(0)}
	client, done := clientFor(t, p)
	defer done()
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatal("one unexpected failure is a failure")
	}
	if !strings.Contains(result.Detail, "1") {
		t.Fatalf("the detail must carry how many failed: %q", result.Detail)
	}
}

// A run still going when the clock runs out has not passed. Returning the last
// seen state would report "running" as a result, and a caller looking only at
// the error would take the zero value for a pass.
func TestARunThatNeverFinishesIsNotAPass(t *testing.T) {
	p := passing()
	p.statuses = []string{"running"}
	client, done := clientFor(t, p)
	defer done()
	client.Timeout = 50 * time.Millisecond
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore"})
	if err == nil {
		t.Fatal("a run that never finishes must be an error")
	}
	if result.Passed {
		t.Fatal("result must not read as passed")
	}
}

// A cancelled run is not a passing one either.
func TestACancelledRunIsNotAPass(t *testing.T) {
	p := passing()
	p.statuses = []string{"cancelled"}
	client, done := clientFor(t, p)
	defer done()
	result, err := client.Run(context.Background(), RunRequest{ScriptKey: "ux/route-restore"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed {
		t.Fatal("cancelled is not passed")
	}
}

// The service refusing the request is an error, not a silent empty result.
func TestARefusedRequestIsAnError(t *testing.T) {
	p := passing()
	p.createStatus = 400
	client, done := clientFor(t, p)
	defer done()
	if _, err := client.Run(context.Background(), RunRequest{ScriptKey: "nope"}); err == nil {
		t.Fatal("a refused request must be an error")
	}
}

// Health answers whether the service is there at all, so `doctor` can say the
// browser service is unreachable rather than leaving every journey criterion
// mysteriously unsettled.
func TestHealthDistinguishesReachableFromNot(t *testing.T) {
	p := passing()
	client, done := clientFor(t, p)
	defer done()
	info, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Service != "playwright-player" || info.Version != "1.0.0" {
		t.Fatalf("info=%+v", info)
	}
	unreachable := Client{BaseURL: "http://127.0.0.1:1", Timeout: time.Second}
	if _, err = unreachable.Health(context.Background()); err == nil {
		t.Fatal("an unreachable service must not report healthy")
	}
}
