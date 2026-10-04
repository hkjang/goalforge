package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/goalforge/goalforge/internal/model"
)

func TestMCPCriterionContract(t *testing.T) {
	ctx := context.Background()
	server, db := fixture(t)
	setGoal := func(t *testing.T, criteria []string) bool {
		t.Helper()
		input, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{
				"name": "goal_set",
				"arguments": map[string]any{
					"project": "mcp-demo", "title": "criterion contract", "objective": "preserve required proof",
					"reason": "exercise criterion contract", "criteria": criteria,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := server.Serve(ctx, bytes.NewReader(append(input, '\n')), &output); err != nil {
			t.Fatal(err)
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
			t.Fatalf("decode response %q: %v", output.String(), err)
		}
		if string(envelope.ID) != "1" || envelope.Error != nil {
			t.Fatalf("goal_set must return a tool result: %+v", envelope)
		}
		var result struct {
			IsError *bool `json:"isError"`
		}
		if err := json.Unmarshal(envelope.Result, &result); err != nil || result.IsError == nil {
			t.Fatalf("invalid tool result %s: %v", envelope.Result, err)
		}
		return *result.IsError
	}

	raw := []string{" SaveNote @ JoUrNeY = true ", "latency@test=<=200ms"}
	want := []model.Criterion{
		{Type: "SaveNote", ExpectedValue: "true", RequiredKind: "journey"},
		{Type: "latency", ExpectedValue: "<=200ms", RequiredKind: "test"},
	}
	if setGoal(t, raw) {
		t.Fatal("goal_set rejected valid criteria")
	}
	before, err := db.CurrentGoal(ctx, "P-MCP")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Criteria, want) {
		t.Fatalf("stored criteria = %+v, want %+v", before.Criteria, want)
	}

	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"empty kind", "x@=true"},
		{"unknown kind", "x@unregistered=true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// The valid entry comes first to exercise partial-write rollback
			// when the store rejects an unknown kind.
			if !setGoal(t, []string{raw[0], tt.raw}) {
				t.Errorf("goal_set must reject %q with result.isError", tt.raw)
			}
			after, err := db.CurrentGoal(ctx, "P-MCP")
			if err != nil {
				t.Fatal(err)
			}
			// This includes the active goal's ID, version and full criteria.
			if !reflect.DeepEqual(after, before) {
				t.Errorf("rejected change altered the active goal:\nbefore: %+v\nafter: %+v", before, after)
			}
		})
	}
}
