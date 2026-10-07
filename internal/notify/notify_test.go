package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests that replace the process-wide suppressor or environment must not run
// in parallel. Restore the original suppressor even when a test fails.
func isolateSuppressor(t *testing.T) *suppressor {
	t.Helper()
	previous := defaultSuppressor
	s := &suppressor{seen: map[string]*reservation{}, now: time.Now}
	defaultSuppressor = s
	t.Cleanup(func() { defaultSuppressor = previous })
	t.Setenv(EnvRepeatWindow, "30m")
	return s
}

func TestPostSendsRedactedSlackCompatiblePayload(t *testing.T) {
	isolateSuppressor(t)
	var received Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv(EnvWebhookURL, server.URL)
	err := Post(context.Background(), Event{Project: "P1", State: "WAITING_QUOTA", Reason: "api_key=sk-abc123 quota exhausted"})
	if err != nil {
		t.Fatal(err)
	}
	if received.Project != "P1" || received.State != "WAITING_QUOTA" {
		t.Fatalf("payload=%+v", received)
	}
	if strings.Contains(received.Reason, "sk-abc123") || strings.Contains(received.Text, "sk-abc123") {
		t.Fatalf("secret leaked into payload: %+v", received)
	}
	if !strings.Contains(received.Text, "GoalForge WAITING_QUOTA") || !strings.Contains(received.Text, "P1") {
		t.Fatalf("text=%q", received.Text)
	}
}

func TestPostIsNoOpWithoutURLAndSurfacesServerErrors(t *testing.T) {
	isolateSuppressor(t)
	t.Setenv(EnvWebhookURL, "")
	if err := Post(context.Background(), Event{Project: "P1", State: "BLOCKED"}); err != nil {
		t.Fatalf("unset URL must be a no-op: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(EnvWebhookURL, server.URL)
	if err := Post(context.Background(), Event{Project: "P1", State: "BLOCKED"}); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPostFailureAllowsNextAttempt(t *testing.T) {
	for _, failure := range []string{"server error", "invalid URL", "canceled context"} {
		t.Run(failure, func(t *testing.T) {
			isolateSuppressor(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) == 1 && failure == "server error" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			t.Setenv(EnvWebhookURL, server.URL)
			ctx := context.Background()
			if failure == "invalid URL" {
				t.Setenv(EnvWebhookURL, "://invalid")
			}
			if failure == "canceled context" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			event := Event{Project: "P1", State: "BLOCKED", Reason: "loop"}
			if err := Post(ctx, event); err == nil {
				t.Fatal("first Post must report the failure")
			}
			want := int32(0)
			if failure == "server error" {
				want = 1
			}
			if got := requests.Load(); got != want {
				t.Fatalf("requests after failure = %d, want %d", got, want)
			}
			t.Setenv(EnvWebhookURL, server.URL)
			if err := Post(context.Background(), event); err != nil {
				t.Fatalf("second Post must succeed: %v", err)
			}
			want++
			if got := requests.Load(); got != want {
				t.Fatalf("requests after retry = %d, want %d", got, want)
			}
			if err := Post(context.Background(), event); err != nil {
				t.Fatalf("third Post must be suppressed: %v", err)
			}
			if got := requests.Load(); got != want {
				t.Fatalf("requests after repeat = %d, want %d", got, want)
			}
		})
	}
}

func TestPostSuppressesConcurrentRepeat(t *testing.T) {
	isolateSuppressor(t)
	started := make(chan struct{}, 1)
	finish := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-finish
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(finish)
	t.Setenv(EnvWebhookURL, server.URL)
	event := Event{Project: "P1", State: "BLOCKED", Reason: "loop"}
	first := make(chan error, 1)
	go func() { first <- Post(context.Background(), event) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach the server")
	}
	// The first request is still in flight. Suppression must return without
	// waiting for its HTTP I/O to finish.
	repeat := make(chan error, 1)
	go func() { repeat <- Post(context.Background(), event) }()
	select {
	case err := <-repeat:
		if err != nil {
			t.Fatalf("concurrent repeat: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent repeat waited for HTTP I/O")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("concurrent requests = %d, want 1", got)
	}
	finish <- struct{}{}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestPostPreservesRepeatWindowBehavior(t *testing.T) {
	s := isolateSuppressor(t)
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv(EnvWebhookURL, server.URL)
	event := Event{Project: "P1", State: "BLOCKED", Reason: "loop"}
	post := func(want int32) {
		t.Helper()
		if err := Post(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if got := requests.Load(); got != want {
			t.Fatalf("requests = %d, want %d", got, want)
		}
	}
	post(1)
	post(1)
	now = now.Add(30 * time.Minute)
	post(2)
	event.Reason = "quota"
	post(3)
	t.Setenv(EnvRepeatWindow, "0")
	post(4)
	post(5)
}
