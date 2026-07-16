package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostSendsRedactedSlackCompatiblePayload(t *testing.T) {
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
