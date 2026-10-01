package rrsi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Change is the machine-applicable part of an edit.
//
// Most of what a proposer writes is prose — reword this instruction, add that
// section — and a person applies it. A few things are settings with a value,
// and those can be applied without anybody, but only because they can also be
// put back.
type Change struct {
	// Field names the setting. It is a fixed list because a change to
	// something nobody enumerated cannot be reverted: there is no code that
	// knows how to put it back.
	Field string `json:"field"`
	// From is what the setting was. A change that cannot record what it
	// replaced is one nobody can undo, and an automatic change nobody can undo
	// is a configuration that drifts on every failure.
	From string `json:"from"`
	To   string `json:"to"`
}

// Applicable fields, with the component each belongs to.
//
// Deliberately few. Every entry here is something automation may alter while
// nobody is watching, so the list grows only when both halves exist: a way to
// set it and a way to put it back.
var applicableFields = map[string]string{
	"wip_limit":         "concurrency",
	"model":             "model",
	"daily_run_limit":   "budget",
	"daily_token_limit": "budget",
	"daily_cost_limit":  "budget",
}

// ApplicableFields lists what automation may change.
func ApplicableFields() []string {
	fields := make([]string, 0, len(applicableFields))
	for field := range applicableFields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

// ComponentFor is the component a field belongs to, so an applied change is
// attributed to the same component the proposal claimed.
func ComponentFor(field string) (string, bool) {
	component, ok := applicableFields[strings.ToLower(strings.TrimSpace(field))]
	return component, ok
}

// Validate refuses a change automation cannot carry out or undo.
func (c Change) Validate(component string) error {
	field := strings.ToLower(strings.TrimSpace(c.Field))
	owner, known := applicableFields[field]
	if !known {
		return fmt.Errorf("%q 는 자동으로 바꿀 수 있는 설정이 아닙니다 — 가능한 것: %s",
			c.Field, strings.Join(ApplicableFields(), ", "))
	}
	if component != "" && owner != component {
		// A change filed under the wrong component is attributed to the wrong
		// component, and the history that decides what to try next is built
		// from exactly that attribution.
		return fmt.Errorf("%s 은 %s 구성의 설정인데 %s 편집에 들어 있습니다", field, owner, component)
	}
	if strings.TrimSpace(c.To) == "" {
		return fmt.Errorf("%s: 바꿀 값이 없습니다", field)
	}
	if strings.TrimSpace(c.From) == "" {
		return fmt.Errorf("%s: 이전 값이 없어 되돌릴 수 없습니다", field)
	}
	if c.From == c.To {
		return fmt.Errorf("%s: 바뀌는 것이 없습니다 (%s)", field, c.From)
	}
	if numericField(field) {
		if _, err := strconv.ParseFloat(strings.TrimSpace(c.To), 64); err != nil {
			return fmt.Errorf("%s 는 수치여야 합니다: %q", field, c.To)
		}
	}
	return nil
}

func numericField(field string) bool {
	switch field {
	case "wip_limit", "daily_run_limit", "daily_token_limit", "daily_cost_limit":
		return true
	}
	return false
}

// Applicable reports whether an edit carries a change automation can make.
func (e Edit) Applicable() bool { return e.Change != nil }
