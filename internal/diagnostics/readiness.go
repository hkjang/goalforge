package diagnostics

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/policy"
)

// GateSpec is a verification gate as readiness checking sees it.
type GateSpec struct {
	Type     string
	Command  []string
	Required bool
	// Kind is what the gate establishes (build, test, journey, ...). Empty
	// means the gate never said.
	Kind string
}

// ReadinessInput describes a project's configuration. Environment checks ask
// "can this machine run anything"; readiness asks the different and more often
// wrong question, "can this project ever finish".
type ReadinessInput struct {
	HasProject bool
	GoalTitle  string
	Criteria   []string
	// CriterionKinds maps a criterion to the kind of check it demands, for the
	// criteria that demand one.
	CriterionKinds     map[string]string
	Gates              []GateSpec
	BudgetConfigured   bool
	IntegrationPending bool
	IntegrationReason  string
	StaleCriteria      []string
}

// CheckReadiness reports configurations that cannot produce a completed goal.
// The common one is silent: gates that never measure a completion criterion, so
// the criterion accumulates no evidence and the goal runs forever.
func CheckReadiness(input ReadinessInput) []Check {
	var checks []Check
	add := func(level, name, detail string) {
		checks = append(checks, Check{Level: level, Name: name, Detail: detail})
	}
	if !input.HasProject {
		return checks
	}
	if strings.TrimSpace(input.GoalTitle) == "" {
		add(LevelFail, "goal", "목표가 없어 실행할 작업을 정할 수 없습니다 (goalforge goal set)")
		return checks
	}
	if len(input.Criteria) == 0 {
		add(LevelFail, "criteria", "완료 조건이 없어 목표가 완료로 판정되지 않습니다 (goal set --criterion build_passed=true)")
	} else {
		add(LevelOK, "criteria", fmt.Sprintf("완료 조건 %d개: %s", len(input.Criteria), strings.Join(input.Criteria, ", ")))
	}
	if len(input.Gates) == 0 {
		add(LevelFail, "gates", "검증 게이트가 없어 어떤 실행도 검증될 수 없습니다 (goalforge verify template go-api)")
		return append(checks, extraChecks(input)...)
	}
	required := 0
	gateTypes := map[string]bool{}
	for _, gate := range input.Gates {
		gateTypes[gate.Type] = true
		if gate.Required {
			required++
		}
	}
	if required == 0 {
		add(LevelFail, "gates", "필수 게이트가 없어 검증이 아무것도 차단하지 못합니다")
	} else {
		add(LevelOK, "gates", fmt.Sprintf("게이트 %d개 (필수 %d개)", len(input.Gates), required))
	}
	// A criterion with no gate of the same type can never be measured. This is
	// the configuration that looks healthy and never completes.
	var unmeasurable []string
	for _, criterion := range input.Criteria {
		if !gateTypes[criterion] {
			unmeasurable = append(unmeasurable, criterion)
		}
	}
	if len(unmeasurable) > 0 {
		sort.Strings(unmeasurable)
		add(LevelFail, "criteria coverage", fmt.Sprintf("완료 조건 %s 측정하는 게이트가 없어 증거가 영원히 쌓이지 않습니다 (같은 이름의 게이트를 추가하세요)", policy.Object(strings.Join(unmeasurable, ", "))))
	} else {
		add(LevelOK, "criteria coverage", "모든 완료 조건에 같은 이름의 게이트가 있습니다")
	}
	checks = append(checks, kindChecks(input)...)
	// A gate whose command is not installed fails every run identically.
	var missing []string
	for _, gate := range input.Gates {
		if len(gate.Command) == 0 {
			missing = append(missing, gate.Type+" (명령 없음)")
			continue
		}
		if _, err := exec.LookPath(gate.Command[0]); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", gate.Type, gate.Command[0]))
		}
	}
	if len(missing) > 0 {
		add(LevelFail, "gate commands", "게이트 명령을 PATH 에서 찾을 수 없습니다: "+strings.Join(missing, ", "))
	} else {
		add(LevelOK, "gate commands", "모든 게이트 명령이 PATH 에 있습니다")
	}
	return append(checks, extraChecks(input)...)
}

// kindChecks reports criteria whose proof cannot come from the gate that is
// supposed to measure them. A criterion asking whether the user can save their
// work, measured by a compile, is a configuration that reports success and
// ships a stub — and unlike a missing gate it looks completely healthy.
func kindChecks(input ReadinessInput) []Check {
	gateKinds := make(map[string]string, len(input.Gates))
	for _, gate := range input.Gates {
		gateKinds[gate.Type] = gate.Kind
	}
	var mismatched []string
	measured := 0
	for _, criterion := range input.Criteria {
		required := input.CriterionKinds[criterion]
		if required == "" {
			continue
		}
		gateKind, covered := gateKinds[criterion]
		if !covered {
			// Measured by nothing, so it is not counted among the criteria a
			// gate answers. Already reported by the coverage check; saying it
			// twice buries the finding that is only visible here.
			continue
		}
		measured++
		if !policy.EvidenceSatisfies(required, gateKind) {
			label := gateKind
			if label == "" {
				label = "종류 미지정"
			}
			mismatched = append(mismatched, fmt.Sprintf("%s (%s 필요, 게이트는 %s)", criterion, required, label))
		}
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		return []Check{{Level: LevelFail, Name: "proof kind",
			Detail: "완료 조건이 요구하는 검증 종류와 게이트가 다릅니다: " + strings.Join(mismatched, ", ") + " (verify gate add --kind 로 게이트 종류를 바로잡거나 알맞은 게이트로 바꾸세요)"}}
	}
	if measured > 0 {
		return []Check{{Level: LevelOK, Name: "proof kind",
			Detail: fmt.Sprintf("검증 종류를 요구하는 완료 조건 %d개가 알맞은 게이트로 측정됩니다", measured)}}
	}
	// No gate answers a criterion's demanded kind — either nothing demanded one
	// or the ones that did are measured by nothing. Either way, if no gate
	// claims to do more than compile, the project can complete with a feature
	// that was never exercised.
	behavioural := false
	for _, gate := range input.Gates {
		if gate.Kind != "" && gate.Kind != policy.KindBuild && gate.Kind != policy.KindReview {
			behavioural = true
			break
		}
	}
	if behavioural {
		return nil
	}
	return []Check{{Level: LevelWarn, Name: "proof kind",
		Detail: "동작을 확인하는 게이트가 없어 빌드만 통과해도 완료로 판정될 수 있습니다 (verify gate add --kind journey 와 goal set --criterion 이름@journey=true)"}}
}

func extraChecks(input ReadinessInput) []Check {
	var checks []Check
	if !input.BudgetConfigured {
		checks = append(checks, Check{Level: LevelWarn, Name: "budget",
			Detail: "예산이 설정되지 않아 실행이 상한 없이 진행됩니다 (goalforge project budget 또는 project profile)"})
	}
	if len(input.StaleCriteria) > 0 {
		checks = append(checks, Check{Level: LevelWarn, Name: "evidence",
			Detail: "재검증이 필요한 완료 조건: " + strings.Join(input.StaleCriteria, ", ")})
	}
	if input.IntegrationPending {
		detail := input.IntegrationReason
		if detail == "" {
			detail = "병합 이후 기본 브랜치가 검증되지 않았습니다"
		}
		checks = append(checks, Check{Level: LevelWarn, Name: "integration", Detail: detail + " (goalforge verify integration)"})
	}
	return checks
}
