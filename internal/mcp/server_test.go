package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

func fixture(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	project := model.Project{ID: "P-MCP", Name: "mcp-demo", RepositoryPath: t.TempDir(), DefaultBranch: "main", Provider: "claude"}
	if err = db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SetGoal(ctx, project.ID, "ship", "objective", "", []model.Criterion{{Type: "build_passed", ExpectedValue: "true"}}); err != nil {
		t.Fatal(err)
	}
	server, err := New(db, "test")
	if err != nil {
		t.Fatal(err)
	}
	return server, db
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func TestStdioProtocolLifecycle(t *testing.T) {
	server, _ := fixture(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"work_add","arguments":{"title":"add exporter","priority":10,"scope":"internal/export"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nonexistent_tool"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"work_set_status","arguments":{"work_item_id":"W-MISSING","status":"RUNNING"}}}`,
	}, "\n") + "\n"
	reader, writer := io.Pipe()
	responses := make(map[string]rpcEnvelope)
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), strings.NewReader(input), writer)
		_ = writer.Close()
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var envelope rpcEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %q: %v", scanner.Text(), err)
		}
		responses[string(envelope.ID)] = envelope
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(responses) != 6 {
		t.Fatalf("expected 6 responses (notification skipped), got %d", len(responses))
	}
	if !strings.Contains(string(responses["1"].Result), `"name":"goalforge"`) || !strings.Contains(string(responses["1"].Result), "2025-06-18") {
		t.Fatalf("initialize=%s", responses["1"].Result)
	}
	var tools struct {
		Tools []toolDescriptor `json:"tools"`
	}
	if err := json.Unmarshal(responses["2"].Result, &tools); err != nil || len(tools.Tools) != 15 {
		t.Fatalf("tools/list=%s err=%v", responses["2"].Result, err)
	}
	if !strings.Contains(string(responses["3"].Result), "mcp-demo") {
		t.Fatalf("list_projects=%s", responses["3"].Result)
	}
	if !strings.Contains(string(responses["4"].Result), "add exporter") || strings.Contains(string(responses["4"].Result), `"isError":true`) {
		t.Fatalf("work_add=%s", responses["4"].Result)
	}
	if responses["5"].Error == nil || !strings.Contains(responses["5"].Error.Message, "unknown tool") {
		t.Fatalf("unknown tool must be a JSON-RPC error: %+v", responses["5"])
	}
	// Tool execution failures surface as isError results, not RPC errors.
	if responses["6"].Error != nil || !strings.Contains(string(responses["6"].Result), `"isError":true`) || !strings.Contains(string(responses["6"].Result), "APPROVED") {
		t.Fatalf("work_set_status=%+v", responses["6"])
	}
}

func TestHTTPTransportAuthAndDispatch(t *testing.T) {
	server, db := fixture(t)
	handler := server.Handler("secret")
	post := func(body, auth, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		if auth != "" {
			request.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, "", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer must be 401: %d", recorder.Code)
	}
	if recorder := post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, "secret", "https://evil.example"); recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign origin must be 403: %d", recorder.Code)
	}
	if recorder := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, "secret", ""); recorder.Code != http.StatusAccepted {
		t.Fatalf("notification must be 202: %d", recorder.Code)
	}
	recorder := post(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"project_status","arguments":{"project":"mcp-demo"}}}`, "secret", "http://localhost:3000")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "build_passed") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := post(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"continue_enqueue","arguments":{}}}`, "secret", ""); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "JOB-") {
		t.Fatalf("enqueue=%d body=%s", recorder.Code, recorder.Body.String())
	}
	jobs, err := db.ListSchedulerJobs(context.Background(), "P-MCP", true)
	if err != nil || len(jobs) != 1 || jobs[0].Type != "CONTINUE" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
}
