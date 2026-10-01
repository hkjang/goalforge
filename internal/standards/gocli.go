package standards

// GoCommandLineTool is the common development pack for a Go command-line tool
// that ships binaries.
//
// It exists because running the web-service pack against one reported that it
// was missing a React dependency, a Dockerfile and a four-variable runtime
// contract — none of which is a defect in a command-line tool. A catalogue
// that describes the wrong shape of project does not merely say nothing
// useful; it teaches the operator to read past the report.
//
// What a tool like this is actually judged on is different: whether the same
// commit produces the same binary, whether every command explains itself,
// whether exit codes mean what a script assumes they mean, and whether the
// release carries something a person can run.
func GoCommandLineTool() Pack {
	return Pack{
		ID:      "go-cli-tool",
		Version: "0.1",
		Title:   "Go 명령줄 도구 공통 개발팩",
		Source:  "docs/STANDARDS.md",
		Standards: []Standard{
			{ID: "CLI-001", Revision: 1, Title: "모듈과 툴체인 고정", Category: "build",
				Intent:   "Go 모듈과 툴체인 버전을 저장소에 고정해 같은 커밋이 같은 바이너리를 만든다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "static", Assertion: "go.mod 에 go 지시자가 있고 외부 경로를 가리키는 replace 가 없다"}, {Type: "build", Assertion: "깨끗한 환경에서 go build 가 성공한다"}},
				EvidenceRequired: []string{"commit_sha", "build_log"},
				ChangeScopeHint:  []string{"go.mod", "go.sum"}},
			{ID: "CLI-002", Revision: 1, Title: "모든 명령에 도움말", Category: "usability",
				Intent:   "모든 하위 명령이 도움말을 내고, 알 수 없는 플래그는 0이 아닌 코드로 끝난다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "test", Assertion: "각 하위 명령의 --help 가 0으로 끝나고 플래그를 설명한다"}, {Type: "test", Assertion: "알 수 없는 플래그는 0이 아닌 코드와 설명을 낸다"}},
				EvidenceRequired: []string{"commit_sha", "test_result"},
				ChangeScopeHint:  []string{"cmd/**"}},
			{ID: "CLI-003", Revision: 1, Title: "종료 코드로 성공과 실패를 말한다", Category: "usability",
				Intent:   "성공은 0, 실패는 0이 아닌 코드로 끝나 스크립트와 CI 게이트에서 쓸 수 있다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "test", Assertion: "실패하는 호출이 0이 아닌 코드로 끝난다"}, {Type: "test", Assertion: "성공하는 호출이 0으로 끝난다"}},
				EvidenceRequired: []string{"commit_sha", "test_result"},
				ChangeScopeHint:  []string{"cmd/**"}},
			{ID: "CLI-004", Revision: 1, Title: "표준 출력은 데이터, 표준 오류는 진단", Category: "usability",
				Intent:   "기계가 읽을 출력은 stdout 으로, 사람이 읽을 진단은 stderr 로 보내 파이프로 쓸 수 있게 한다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "test", Assertion: "stdout 만 받아도 파싱 가능한 출력이 나온다"}, {Type: "static", Assertion: "진단과 경고가 stdout 을 오염시키지 않는다"}},
				EvidenceRequired: []string{"commit_sha", "test_result"},
				ChangeScopeHint:  []string{"cmd/**", "internal/**"}},
			{ID: "CLI-005", Revision: 1, Title: "버전을 말한다", Category: "usability",
				Intent:   "version 명령이 릴리즈 태그와 같은 값을 내고, 설정 없는 디렉터리에서도 동작한다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "test", Assertion: "version 이 릴리즈 태그와 일치한다"}, {Type: "test", Assertion: "프로젝트가 등록되지 않은 디렉터리에서도 version 이 동작한다"}},
				EvidenceRequired: []string{"commit_sha", "test_result"},
				ChangeScopeHint:  []string{"cmd/**"}},
			{ID: "CLI-006", Revision: 1, Title: "상태 파일 위치를 명시한다", Category: "configuration",
				Intent:   "상태와 설정을 어디에 쓰는지 문서와 명령이 같은 값을 말하고, 기본 경로가 예측 가능하다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "static", Assertion: "기본 상태 경로가 문서에 적혀 있다"}, {Type: "test", Assertion: "경로를 지정하면 그곳에만 쓴다"}},
				EvidenceRequired: []string{"commit_sha", "test_result"},
				ChangeScopeHint:  []string{"cmd/**", "docs/**"}},
			{ID: "CLI-007", Revision: 1, Title: "플랫폼별 실행 가능한 릴리즈", Category: "release",
				Intent:   "지원 플랫폼마다 실행 가능한 바이너리와 체크섬을 릴리즈에 붙인다",
				Severity: SeverityRequired, AppliesWhen: map[string]string{"release": "binaries"},
				Checks:           []Check{{Type: "build", Assertion: "릴리즈 워크플로가 여러 GOOS/GOARCH 로 빌드한다"}, {Type: "release_asset", Assertion: "릴리즈에 플랫폼별 자산과 체크섬이 있다"}},
				EvidenceRequired: []string{"commit_sha", "release_asset"},
				ChangeScopeHint:  []string{".github/**"}},
			{ID: "CLI-008", Revision: 1, Title: "릴리즈 전 세 플랫폼에서 검증", Category: "release",
				Intent:   "태그 시점에 리눅스·macOS·윈도우에서 시험을 돌린 뒤 게시한다",
				Severity: SeverityRequired, AppliesWhen: map[string]string{"release": "binaries"},
				Checks:           []Check{{Type: "build", Assertion: "게시 작업이 세 플랫폼 검증 작업에 의존한다"}},
				EvidenceRequired: []string{"commit_sha", "build_log"},
				ChangeScopeHint:  []string{".github/**"}},
			{ID: "CLI-009", Revision: 1, Title: "비밀값을 출력에 남기지 않는다", Category: "security",
				Intent:   "토큰·키·자격증명이 출력·로그·오류 메시지에 평문으로 나오지 않는다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "security", Assertion: "자격증명을 설정한 상태에서 출력에 그 값이 나오지 않는다"}},
				EvidenceRequired: []string{"commit_sha", "security_test_result"},
				ChangeScopeHint:  []string{"cmd/**", "internal/**"}},
			{ID: "CLI-010", Revision: 1, Title: "변경마다 시험이 돈다", Category: "quality",
				Intent:   "모든 변경에서 go test ./... · go vet · gofmt 가 돌고 통과해야 병합된다",
				Severity: SeverityRequired, AppliesWhen: nil,
				Checks:           []Check{{Type: "build", Assertion: "CI 가 모든 PR 에서 test·vet·gofmt 를 돌린다"}},
				EvidenceRequired: []string{"commit_sha", "build_log"},
				ChangeScopeHint:  []string{".github/**"}},
			{ID: "CLI-011", Revision: 1, Title: "설치가 한 줄이다", Category: "documentation",
				Intent:   "내려받아 설치하는 방법이 README 와 릴리즈 본문에 같은 형태로 적혀 있다",
				Severity: SeverityRecommended, AppliesWhen: map[string]string{"release": "binaries"},
				Checks:           []Check{{Type: "static", Assertion: "README 에 설치 명령이 있다"}},
				EvidenceRequired: []string{"commit_sha"},
				ChangeScopeHint:  []string{"README.md", "docs/**"}},
			{ID: "CLI-012", Revision: 1, Title: "사용 가이드", Category: "documentation",
				Intent:   "명령 목록과 실제 사용 흐름을 담은 가이드를 제공한다",
				Severity: SeverityRecommended, AppliesWhen: nil,
				Checks:           []Check{{Type: "static", Assertion: "docs 에 가이드가 있고 명령 목록이 실제 명령과 일치한다"}},
				EvidenceRequired: []string{"commit_sha"},
				ChangeScopeHint:  []string{"docs/**", "README.md"}},
		},
	}
}
