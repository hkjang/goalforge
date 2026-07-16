// Package mcp exposes GoalForge management as a Model Context Protocol
// server over stdio, so MCP clients (Claude Code, editors, agents) can
// inspect and steer projects conversationally. The transport is
// newline-delimited JSON-RPC 2.0 per the MCP stdio specification; only the
// tools capability is implemented, and every tool maps onto the same
// authoritative store operations the CLI and HTTP API use.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

const protocolVersion = "2025-06-18"

type Server struct {
	store   *store.Store
	version string
	mu      sync.Mutex
	out     io.Writer
}

func New(s *store.Store, version string) (*Server, error) {
	if s == nil {
		return nil, errors.New("store is required")
	}
	if version == "" {
		version = "dev"
	}
	return &Server{store: s, version: version}, nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve pumps newline-delimited JSON-RPC messages until input closes or ctx
// ends. Notifications (no id) never produce a response.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.reply(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		if req.ID == nil {
			continue // notification
		}
		result, rpcErr := s.dispatch(ctx, req)
		if rpcErr != nil {
			s.reply(response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			continue
		}
		s.reply(response{JSONRPC: "2.0", ID: req.ID, Result: result})
	}
	return scanner.Err()
}

func (s *Server) reply(r response) {
	raw, err := json.Marshal(r)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(append(raw, '\n'))
}

func (s *Server) dispatch(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		negotiated := protocolVersion
		if params.ProtocolVersion != "" && params.ProtocolVersion < protocolVersion {
			negotiated = params.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": negotiated,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "goalforge", "version": s.version},
			"instructions":    "GoalForge management tools. Read state with list_projects/project_status/work_list/usage_report/runs_recent/run_detail, plan with goal_set/work_add/work_set_status, gate side effects with approvals_list/approval_request/approval_decide, and drive execution with continue_enqueue (a running `goalforge worker` processes it) or checkpoint_create.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDescriptors()}, nil
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid tools/call params: " + err.Error()}
		}
		text, err := s.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			if errors.Is(err, errUnknownTool) {
				return nil, &rpcError{Code: -32602, Message: err.Error()}
			}
			return toolResult(fmt.Sprintf("error: %v", err), true), nil
		}
		return toolResult(text, false), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
