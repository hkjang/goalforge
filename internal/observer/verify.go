package observer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/policy"
	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// evidenceForKind is what a gate of each kind produces.
//
// A review is absent on purpose: it is a person's judgement, and recording it
// as executed evidence would let "somebody looked at it" stand where "somebody
// ran it" is required.
var evidenceForKind = map[string]string{
	policy.KindJourney:     "browser_test_result",
	policy.KindIntegration: "integration_test_result",
	policy.KindSecurity:    "security_test_result",
	policy.KindBuild:       "build_log",
	policy.KindTest:        "test_result",
	policy.KindPerformance: "performance_result",
}

// SettleFromGates turns the project's gate results into assessments.
//
// This is the only path by which a criterion can reach MET. The static
// observer proves absences and cannot prove that anything works, so until
// something is actually run every criterion it cannot see is UNKNOWN. Running
// the gates is what moves them.
//
// Every result still goes through the judge: the gate supplies evidence, and
// whether that evidence is enough remains the judge's decision.
func SettleFromGates(ctx context.Context, db *store.Store, projectID, goalID, commitSHA string,
	pack standards.Pack, profile standards.Profile, toolVersion string) (map[string]string, error) {
	settled := map[string]string{}
	claims, err := db.GateClaims(ctx, projectID)
	if err != nil {
		return nil, err
	}
	results, err := db.LatestGateResults(ctx, goalID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	// Gather per criterion first: several gates may each settle part of one
	// criterion, and judging after the first would report a criterion unmet
	// because the gate that covers its other half had not been read yet.
	evidence := map[string][]standards.Evidence{}
	failures := map[string][]string{}
	for checkType, claim := range claims {
		result, ran := results[checkType]
		if !ran {
			// A gate that claims a criterion and has not run leaves it exactly
			// where it was. Treating "not run" as a failure would report work
			// that nobody has checked as work that is broken.
			continue
		}
		for _, standardID := range claim.Settles {
			if _, known := pack.Standard(standardID); !known {
				continue
			}
			if result.Status != "PASSED" {
				failures[standardID] = append(failures[standardID],
					fmt.Sprintf("%s 게이트가 %s (%s)", checkType, result.Status, firstLine(result.Output)))
				continue
			}
			for _, kind := range producedKinds(claim, result.EvidenceKind) {
				// The behavioural check. A gate declaring it produces a
				// browser test result is only believed when it is a journey
				// gate: someone wiring a build gate to a journey criterion is
				// making the original mistake explicitly, and the explicit
				// version has to be refused too. Locators carry no such claim
				// and pass through.
				if standards.ExecutedEvidence(kind) && !policy.EvidenceSatisfies(kindForEvidence(kind), result.EvidenceKind) {
					continue
				}
				evidence[standardID] = append(evidence[standardID], standards.Evidence{
					Kind: kind, Detail: checkType + ": " + firstLine(result.Output), Observed: true})
			}
		}
	}
	touched := map[string]bool{}
	for standardID := range evidence {
		touched[standardID] = true
	}
	for standardID := range failures {
		touched[standardID] = true
	}
	ordered := make([]string, 0, len(touched))
	for standardID := range touched {
		ordered = append(ordered, standardID)
	}
	sort.Strings(ordered)
	for _, standardID := range ordered {
		standard, known := pack.Standard(standardID)
		if !known {
			continue
		}
		_, excepted := profile.ExceptionFor(standardID, now)
		proposed, detail := standards.ResultMet, ""
		if reasons := failures[standardID]; len(reasons) > 0 {
			proposed, detail = standards.ResultUnmet, strings.Join(reasons, "; ")
		}
		items := evidence[standardID]
		// The commit is something this pass genuinely knows, so it supplies it
		// rather than making every gate repeat it.
		if commitSHA != "" {
			items = append(items, standards.Evidence{Kind: "commit_sha", Detail: commitSHA, Observed: true})
		}
		saved, saveErr := db.RecordAssessment(ctx, standard, standards.Assessment{ProjectID: projectID,
			StandardID: standardID, CommitSHA: commitSHA, Result: proposed, Detail: detail,
			Evidence: items, AssessedAt: now, ToolVersion: toolVersion}, excepted)
		if saveErr != nil {
			return nil, saveErr
		}
		settled[standardID] = saved.Result
	}
	return settled, nil
}

// producedKinds is the evidence a gate result carries: the one its kind
// implies, plus anything the gate declared it also produces.
func producedKinds(claim store.GateClaim, gateKind string) []string {
	var kinds []string
	if implied, ok := evidenceForKind[strings.ToLower(strings.TrimSpace(gateKind))]; ok {
		kinds = append(kinds, implied)
	}
	return append(kinds, claim.Produces...)
}

// kindForEvidence inverts evidenceForKind.
func kindForEvidence(evidenceKind string) string {
	for kind, produced := range evidenceForKind {
		if produced == evidenceKind {
			return kind
		}
	}
	return ""
}

func firstLine(output string) string {
	output = strings.TrimSpace(output)
	if index := strings.IndexByte(output, '\n'); index >= 0 {
		output = output[:index]
	}
	if len(output) > 160 {
		output = output[:160]
	}
	if output == "" {
		return "출력 없음"
	}
	return output
}
