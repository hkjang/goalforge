package api

import (
	"fmt"
	"net/http"
	"strings"
)

type metricFamily struct {
	name, help, kind string
	value            func(summary ProjectSummary) float64
}

// metricFamilies maps the section-15 observability metrics onto Prometheus
// exposition families. Everything is derived from authoritative store state,
// so scrapes are consistent with the dashboard and CLI.
var metricFamilies = []metricFamily{
	{"goalforge_runs_total", "AI executions started", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.RunsTotal) }},
	{"goalforge_runs_successful_total", "AI executions that reached verification or completion", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.RunsSuccessful) }},
	{"goalforge_runs_failed_total", "AI executions that failed", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.RunsFailed) }},
	{"goalforge_run_duration_seconds_avg", "Average completed run duration", "gauge", func(s ProjectSummary) float64 { return s.Metrics.AverageRunSeconds }},
	{"goalforge_work_items_done_total", "Work items completed", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.WorkDone) }},
	{"goalforge_work_items_blocked", "Work items awaiting user review", "gauge", func(s ProjectSummary) float64 { return float64(s.Metrics.WorkBlocked) }},
	{"goalforge_verifications_total", "Verification gate executions", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.VerificationTotal) }},
	{"goalforge_verifications_passed_total", "Verification gate passes", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.VerificationPassed) }},
	{"goalforge_sessions_total", "Provider sessions created", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.SessionCount) }},
	{"goalforge_input_tokens_total", "Input tokens consumed", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.InputTokens) }},
	{"goalforge_output_tokens_total", "Output tokens produced", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.OutputTokens) }},
	{"goalforge_cached_input_tokens_total", "Input tokens served from cache", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.CachedInputTokens) }},
	{"goalforge_reasoning_tokens_total", "Reasoning tokens consumed", "counter", func(s ProjectSummary) float64 { return float64(s.Metrics.ReasoningTokens) }},
	{"goalforge_cost_usd_total", "Provider cost in USD", "counter", func(s ProjectSummary) float64 { return s.Metrics.CostUSD }},
	{"goalforge_goal_progress_percent", "Weighted goal progress", "gauge", func(s ProjectSummary) float64 { return s.Progress }},
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summaries := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		summary, summaryErr := s.summary(r.Context(), project)
		if summaryErr != nil {
			writeError(w, http.StatusInternalServerError, summaryErr.Error())
			return
		}
		summaries = append(summaries, summary)
	}
	var b strings.Builder
	for _, family := range metricFamilies {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", family.name, family.help, family.name, family.kind)
		for _, summary := range summaries {
			// %q escapes quotes, backslashes, and newlines exactly as the
			// Prometheus exposition format requires.
			fmt.Fprintf(&b, "%s{project=%q,state=%q} %g\n", family.name, summary.Project.Name, summary.Project.State, family.value(summary))
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
