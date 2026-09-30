// Package browser drives a playwright-player service.
//
// On an isolated network the browser cannot be bundled with GoalForge: the
// image would have to carry Chromium and its dependencies, and the first thing
// an operator does with a closed network is stop that image from fetching
// anything. So the browser lives in its own service inside the network and
// GoalForge calls it.
//
// The whole point of this package is that a browser check can fail in ways
// that look like nothing at all. The service is unreachable, the run never
// finishes, the script matched no tests — each of those exits without a
// failure to report, and a client that took "no failures" for "passed" would
// turn every criterion it settles green at exactly the moment nothing is being
// checked.
package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one playwright-player.
type Client struct {
	// BaseURL is the service root, e.g. http://playwright-player:3000.
	BaseURL string
	// Timeout bounds one whole run, polling included.
	Timeout time.Duration
	// PollInterval is how often the run is asked whether it has finished.
	PollInterval time.Duration
	HTTP         *http.Client
}

// RunRequest is one script execution.
type RunRequest struct {
	ScriptKey string            `json:"scriptKey"`
	Project   string            `json:"project,omitempty"`
	BaseURL   string            `json:"baseURL,omitempty"`
	Grep      string            `json:"grep,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
	// Screenshot, Trace and Video follow the service's own vocabulary rather
	// than being reinvented here, so the operator configures one thing.
	Screenshot string `json:"screenshot,omitempty"`
	Trace      string `json:"trace,omitempty"`
	Video      string `json:"video,omitempty"`
}

// Result is what a run established.
type Result struct {
	RunID  string
	Status string
	// Passed is true only when the run finished, something actually ran, and
	// nothing failed. Each of those is a separate way to be green for the
	// wrong reason.
	Passed   bool
	Expected int
	Failed   int
	Skipped  int
	Flaky    int
	ExitCode int
	Detail   string
	// Screenshots are artifact paths, relative to the run's directory on the
	// service.
	Screenshots []string
	Artifacts   []Artifact
	Duration    time.Duration
}

// Artifact is one file the run produced.
type Artifact struct {
	FileName     string `json:"fileName"`
	RelativePath string `json:"relativePath"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// Health is what the service says about itself.
type Health struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	ScriptCount int    `json:"scriptCount"`
	RunCount    int    `json:"runCount"`
}

// envelope is the service's {success, data} wrapper.
type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const defaultTimeout = 10 * time.Minute

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c Client) endpoint(path string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", fmt.Errorf("playwright-player 주소가 설정되지 않았습니다")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("playwright-player 주소가 올바르지 않습니다: %q", c.BaseURL)
	}
	return base + path, nil
}

// call issues one request and unwraps the envelope.
func (c Client) call(ctx context.Context, method, path string, body any, into any) error {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		encoded, encodeErr := json.Marshal(body)
		if encodeErr != nil {
			return encodeErr
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		// Named explicitly. "connection refused" on its own sends the reader
		// looking at GoalForge, and the thing that is down is somewhere else.
		return fmt.Errorf("playwright-player (%s) 에 연결하지 못했습니다: %w", c.BaseURL, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("playwright-player %s %s: %s %s", method, path, response.Status,
			strings.TrimSpace(firstLine(string(payload))))
	}
	var wrapper envelope
	if err = json.Unmarshal(payload, &wrapper); err != nil {
		return fmt.Errorf("playwright-player 응답을 읽지 못했습니다: %w", err)
	}
	if !wrapper.Success {
		message := "알 수 없는 오류"
		if wrapper.Error != nil {
			message = wrapper.Error.Message
		}
		return fmt.Errorf("playwright-player 가 요청을 거절했습니다: %s", message)
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(wrapper.Data, into)
}

// Health asks whether the service is there.
//
// It exists so `doctor` can say the browser service is unreachable rather than
// leaving every journey criterion mysteriously unsettled — an operator staring
// at a board full of UNKNOWN has no way to guess that one container is down.
func (c Client) Health(ctx context.Context) (Health, error) {
	var health Health
	err := c.call(ctx, http.MethodGet, "/health", nil, &health)
	return health, err
}

// runState is the service's serialized run.
type runState struct {
	RunID    string `json:"runId"`
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	Summary  struct {
		Stats struct {
			Expected   int `json:"expected"`
			Unexpected int `json:"unexpected"`
			Flaky      int `json:"flaky"`
			Skipped    int `json:"skipped"`
			Duration   int `json:"duration"`
		} `json:"stats"`
	} `json:"summary"`
}

// Run executes a script and waits for it to finish.
func (c Client) Run(ctx context.Context, request RunRequest) (Result, error) {
	var result Result
	if strings.TrimSpace(request.ScriptKey) == "" {
		return result, fmt.Errorf("실행할 스크립트를 지정해야 합니다")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	poll := c.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var created runState
	if err := c.call(ctx, http.MethodPost, "/api/runs", request, &created); err != nil {
		return result, err
	}
	result.RunID = created.RunID
	state := created
	for {
		if state.Status != "running" && state.Status != "" {
			break
		}
		select {
		case <-ctx.Done():
			// Still going when the clock ran out. Returning the last seen
			// state would report "running" as a result, and a caller looking
			// only at the error would take the zero value for a pass.
			return result, fmt.Errorf("playwright-player 실행 %s 이(가) %s 안에 끝나지 않았습니다",
				result.RunID, timeout)
		case <-time.After(poll):
		}
		if err := c.call(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(result.RunID), nil, &state); err != nil {
			return result, err
		}
	}
	result.Status, result.ExitCode = state.Status, state.ExitCode
	stats := state.Summary.Stats
	result.Expected, result.Failed = stats.Expected, stats.Unexpected
	result.Skipped, result.Flaky = stats.Skipped, stats.Flaky
	result.Duration = time.Duration(stats.Duration) * time.Millisecond
	var artifacts []Artifact
	if err := c.call(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(result.RunID)+"/artifacts", nil, &artifacts); err == nil {
		result.Artifacts = artifacts
		for _, artifact := range artifacts {
			if strings.HasSuffix(strings.ToLower(artifact.FileName), ".png") && artifact.SizeBytes > 0 {
				// Size checked as well as extension: a zero-byte file is a
				// screenshot that was never written, and claiming it as
				// evidence would be claiming a picture nobody took.
				result.Screenshots = append(result.Screenshots, artifact.RelativePath)
			}
		}
	}
	result.Passed, result.Detail = judge(result)
	return result, nil
}

// judge decides whether a finished run established anything.
//
// The three ways to be green for the wrong reason are separate checks because
// they call for different fixes: a cancelled run needs re-running, a run with
// no tests needs its script or filter corrected, and a failing run needs the
// code changed.
func judge(result Result) (bool, string) {
	// Known failures come first: "검사 2건이 실패했습니다" points at the tests,
	// and "실행이 failed 로 끝났습니다" points at the runner. When both are
	// true the first is the one somebody can act on.
	if result.Failed > 0 {
		return false, fmt.Sprintf("검사 %d건이 실패했습니다 (통과 %d, 건너뜀 %d)",
			result.Failed, result.Expected, result.Skipped)
	}
	if result.Status != "completed" {
		return false, fmt.Sprintf("실행이 %s 로 끝났습니다 (exit %d)", result.Status, result.ExitCode)
	}
	if result.Expected == 0 {
		// Exit code zero because there was nothing to fail. This is the
		// failure that looks most like success, and on a criterion it settles
		// it would read as proof.
		return false, fmt.Sprintf("실행된 검사가 없습니다 (건너뜀 %d) — 스크립트나 필터를 확인하세요", result.Skipped)
	}
	detail := fmt.Sprintf("검사 %d건 통과", result.Expected)
	if result.Skipped > 0 {
		detail += fmt.Sprintf(" (건너뜀 %d)", result.Skipped)
	}
	if result.Flaky > 0 {
		detail += fmt.Sprintf(" (불안정 %d)", result.Flaky)
	}
	return true, detail
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
