package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/provider"
)

type StopFailure struct {
	SessionID, Error, ErrorDetails, LastAssistantMessage string
}

func DecodeStopFailure(raw []byte, now time.Time) (StopFailure, provider.QuotaSnapshot, error) {
	var payload struct {
		SessionID            string `json:"session_id"`
		HookEventName        string `json:"hook_event_name"`
		Error                string `json:"error"`
		ErrorDetails         string `json:"error_details"`
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return StopFailure{}, provider.QuotaSnapshot{}, fmt.Errorf("decode Claude StopFailure: %w", err)
	}
	if payload.HookEventName != "StopFailure" || payload.Error == "" {
		return StopFailure{}, provider.QuotaSnapshot{}, errors.New("not a Claude StopFailure payload")
	}
	failure := StopFailure{SessionID: payload.SessionID, Error: payload.Error, ErrorDetails: payload.ErrorDetails, LastAssistantMessage: payload.LastAssistantMessage}
	message := strings.TrimSpace(strings.Join([]string{payload.ErrorDetails, payload.LastAssistantMessage}, " "))
	quota := provider.QuotaSnapshot{Provider: "claude", LimitType: "session", Source: "stop_failure", Confidence: "high", RawMessage: message}
	if payload.Error != "rate_limit" {
		return failure, quota, nil
	}
	quota.LimitReached = true
	quota.UsedPercent = 100
	if reset := ParseResetTime(message, now); reset != nil {
		quota.ResetAt = reset
	} else {
		quota.Confidence = "low"
	}
	return failure, quota, nil
}

var rfc3339Time = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})`)
var retryDuration = regexp.MustCompile(`(?i)(?:try again|retry|resets?)\s+in\s+([0-9]+(?:\.[0-9]+)?\s*(?:seconds?|minutes?|hours?|days?|[smhd])(?:\s+[0-9]+(?:\.[0-9]+)?\s*(?:seconds?|minutes?|hours?|days?|[smhd]))*)`)
var clockTime = regexp.MustCompile(`(?i)resets?(?:\s+at)?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`)

func ParseResetTime(message string, now time.Time) *time.Time {
	if value := rfc3339Time.FindString(message); value != "" {
		if parsed, err := time.Parse(time.RFC3339, normalizeOffset(value)); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	if match := retryDuration.FindStringSubmatch(message); len(match) == 2 {
		if duration, ok := parseHumanDuration(match[1]); ok {
			value := now.Add(duration).UTC()
			return &value
		}
	}
	if match := clockTime.FindStringSubmatch(message); len(match) == 4 {
		hour, _ := strconv.Atoi(match[1])
		minute, _ := strconv.Atoi(match[2])
		if hour == 12 {
			hour = 0
		}
		if strings.EqualFold(match[3], "pm") {
			hour += 12
		}
		local := now.In(now.Location())
		value := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location())
		if !value.After(local) {
			value = value.Add(24 * time.Hour)
		}
		value = value.UTC()
		return &value
	}
	return nil
}

func normalizeOffset(value string) string {
	if len(value) >= 5 && (value[len(value)-5] == '+' || value[len(value)-5] == '-') && value[len(value)-3] != ':' {
		return value[:len(value)-2] + ":" + value[len(value)-2:]
	}
	return value
}

func parseHumanDuration(value string) (time.Duration, bool) {
	parts := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(seconds?|minutes?|hours?|days?|[smhd])`).FindAllStringSubmatch(value, -1)
	if len(parts) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, part := range parts {
		number, err := strconv.ParseFloat(part[1], 64)
		if err != nil {
			return 0, false
		}
		unit := time.Second
		switch strings.ToLower(part[2])[0] {
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		case 'd':
			unit = 24 * time.Hour
		}
		total += time.Duration(number * float64(unit))
	}
	return total, total > 0
}
