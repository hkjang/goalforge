package policy

import "strings"

// GateFailureKind classifies why a verification gate failed. Provider and
// infrastructure failures are classified separately by ClassifyFailure; this
// covers the gates themselves, where the useful distinction is whether another
// AI session could plausibly fix it.
type GateFailureKind string

const (
	GateTestFailure     GateFailureKind = "test_failure"
	GateBuildFailure    GateFailureKind = "build_failure"
	GateThresholdNotMet GateFailureKind = "threshold_not_met"
	GateEnvironment     GateFailureKind = "environment"
	GateDependency      GateFailureKind = "dependency"
	GateAuth            GateFailureKind = "auth"
	GateTimeout         GateFailureKind = "timeout"
	GateMisconfigured   GateFailureKind = "misconfigured"
	GateUnknown         GateFailureKind = "unknown"
)

// RepairMode is what would actually address the failure. Separating a code fix
// from an environment problem matters because re-running a model against a
// missing binary or an expired credential only spends budget.
type RepairMode string

const (
	RepairCodeFix     RepairMode = "CODE_FIX"
	RepairEnvironment RepairMode = "ENVIRONMENT"
	RepairHuman       RepairMode = "HUMAN"
)

type GateFailure struct {
	Kind    GateFailureKind
	Mode    RepairMode
	Summary string
}

// ClassifyGateFailure reads a failed gate's status and output. Environment,
// dependency, and credential problems are checked first: a broken environment
// also makes tests print failures, and treating that as a test failure sends an
// AI session to fix code that was never wrong.
func ClassifyGateFailure(status string, output string) GateFailure {
	text := strings.ToLower(output)
	if status == "TIMEOUT" {
		return GateFailure{Kind: GateTimeout, Mode: RepairHuman, Summary: summaries[GateTimeout]}
	}
	switch {
	case containsAny(text, "value pattern matched no measurement", "value pattern is invalid", "is not a number"):
		return GateFailure{Kind: GateMisconfigured, Mode: RepairHuman, Summary: summaries[GateMisconfigured]}
	case strings.Contains(text, "below the threshold"):
		return GateFailure{Kind: GateThresholdNotMet, Mode: RepairCodeFix, Summary: summaries[GateThresholdNotMet]}
	case containsAny(text, "401", "403 forbidden", "unauthorized", "authentication failed", "invalid token", "permission denied (publickey)", "not logged in"):
		return GateFailure{Kind: GateAuth, Mode: RepairEnvironment, Summary: summaries[GateAuth]}
	case containsAny(text, "command not found", "executable file not found", "no such file or directory", "permission denied", "no space left on device", "cannot allocate memory", "connection refused", "could not resolve host", "network is unreachable", "dial tcp"):
		return GateFailure{Kind: GateEnvironment, Mode: RepairEnvironment, Summary: summaries[GateEnvironment]}
	case containsAny(text, "cannot find module", "module not found", "npm err! 404", "could not resolve dependency", "no required module provides package", "unknown revision", "checksum mismatch", "go: downloading"):
		return GateFailure{Kind: GateDependency, Mode: RepairEnvironment, Summary: summaries[GateDependency]}
	case containsAny(text, "--- fail:", "fail\t", "test failed", "assertion", "assert", "expected", "panic: "):
		return GateFailure{Kind: GateTestFailure, Mode: RepairCodeFix, Summary: summaries[GateTestFailure]}
	case containsAny(text, "syntax error", "undefined:", "cannot use", "declared and not used", "build failed", "compilation failed", "error ts", "cannot find name"):
		return GateFailure{Kind: GateBuildFailure, Mode: RepairCodeFix, Summary: summaries[GateBuildFailure]}
	}
	return GateFailure{Kind: GateUnknown, Mode: RepairHuman, Summary: summaries[GateUnknown]}
}

// summaries keeps the explanation for a classification available after it has
// been persisted as a bare kind.
var summaries = map[GateFailureKind]string{
	GateTestFailure:     "테스트가 실패했습니다. 테스트를 삭제하거나 완화하지 않고 원인을 고쳐야 합니다.",
	GateBuildFailure:    "빌드가 실패했습니다. 컴파일 오류를 수정하면 해결될 가능성이 높습니다.",
	GateThresholdNotMet: "명령은 성공했지만 측정값이 기준에 미치지 못했습니다. 기준을 낮추지 말고 값을 올려야 합니다.",
	GateEnvironment:     "실행 환경 문제입니다. 도구·경로·네트워크를 먼저 복구해야 하며 모델 재실행은 낭비입니다.",
	GateDependency:      "의존성을 가져오지 못했습니다. 잠금 파일이나 레지스트리 접근을 먼저 확인해야 합니다.",
	GateAuth:            "인증이 거부되었습니다. 자격 증명을 갱신해야 하며 코드를 고쳐도 해결되지 않습니다.",
	GateTimeout:         "게이트가 제한 시간 안에 끝나지 않았습니다. 무한 대기인지 단순히 느린 것인지 사람이 판단해야 합니다.",
	GateMisconfigured:   "게이트가 측정값을 뽑아내지 못했습니다. 명령이나 --value-pattern 설정을 고쳐야 합니다.",
	GateUnknown:         "실패 원인을 분류하지 못했습니다. 출력 전체를 확인해야 합니다.",
}

// ClassifyGateFailureSummary explains a stored classification.
func ClassifyGateFailureSummary(kind string) string {
	if summary, ok := summaries[GateFailureKind(kind)]; ok {
		return summary
	}
	return summaries[GateUnknown]
}
