// Package notify delivers best-effort webhook notifications for project
// state transitions a human should hear about (quota waits, blocks, goal
// completion). It is a no-op unless GOALFORGE_WEBHOOK_URL is set.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/goalforge/goalforge/internal/audit"
)

// EnvWebhookURL configures the JSON webhook endpoint.
const EnvWebhookURL = "GOALFORGE_WEBHOOK_URL"

// Event describes a state transition. Text doubles as a Slack-compatible
// message body; when empty it is derived from the other fields.
type Event struct {
	Project string `json:"project"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Text    string `json:"text"`
}

// Post sends event to the configured webhook. It is best-effort: an unset
// URL is a no-op, secrets are redacted, and callers are expected to ignore
// the returned error rather than fail the transition that triggered it.
func Post(ctx context.Context, event Event) error {
	url := os.Getenv(EnvWebhookURL)
	if url == "" {
		return nil
	}
	event.Reason = audit.RedactString(event.Reason)
	if event.Text == "" {
		event.Text = fmt.Sprintf("GoalForge %s: project %s", event.State, event.Project)
		if event.Reason != "" {
			event.Text += " — " + event.Reason
		}
	} else {
		event.Text = audit.RedactString(event.Text)
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", response.StatusCode)
	}
	return nil
}
