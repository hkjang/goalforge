package mcp

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
)

// Handler serves the MCP Streamable HTTP transport for remote clients: each
// POST /mcp carries one JSON-RPC message and receives one JSON response
// (notifications are acknowledged with 202). The server is stateless — no
// SSE stream is opened because it never initiates messages. When bearer is
// non-empty every request must present it; Origin headers are validated
// against localhost to block DNS-rebinding attacks on locally bound servers.
func (s *Server) Handler(bearer string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !localOrigin(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if bearer != "" && r.Header.Get("Authorization") != "Bearer "+bearer {
			http.Error(w, "valid bearer token required", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPost:
		case http.MethodGet:
			// No server-initiated stream; clients polling for one get a
			// clean signal instead of a hang.
			http.Error(w, "SSE stream not supported; POST JSON-RPC messages", http.StatusMethodNotAllowed)
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var req request
		if err = json.Unmarshal(body, &req); err != nil {
			writeRPC(w, response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result, rpcErr := s.dispatch(r.Context(), req)
		if rpcErr != nil {
			writeRPC(w, response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			return
		}
		writeRPC(w, response{JSONRPC: "2.0", ID: req.ID, Result: result})
	})
}

func writeRPC(w http.ResponseWriter, r response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r)
}

func localOrigin(origin string) bool {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	host := trimmed
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		host = h
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}
