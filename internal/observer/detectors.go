package observer

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/standards"
)

// Default is the set of detectors that ship.
//
// It is deliberately small. A detector that guesses is worse than no detector:
// the criterion it covers stops being investigated and starts being answered,
// and the answer is wrong in whichever direction the guess leans.
func Default() []Detector {
	return []Detector{
		goAndReactDetector{},
		runtimeEnvDetector{},
		externalAssetDetector{},
		runtimeBuildDetector{},
	}
}

// unknown is the honest answer when a static read cannot settle something. It
// says what would settle it, so the criterion becomes an investigation with a
// next step rather than a blank.
func unknown(settledBy string) Finding {
	return Finding{Result: standards.ResultUnknown,
		Detail: "정적 읽기로는 판정할 수 없습니다 — " + settledBy}
}

// CORE-001: a Go server and a React web front end.
//
// Both are provable from the tree: a go.mod is a Go module or there is not
// one, and a package.json either depends on react or it does not.
type goAndReactDetector struct{}

func (goAndReactDetector) StandardID() string { return "CORE-001" }

func (goAndReactDetector) Detect(tree Tree) (Finding, error) {
	var missing []string
	var evidence []standards.Evidence
	if goMod := matches(tree, "go.mod"); len(goMod) == 0 {
		missing = append(missing, "Go 모듈(go.mod)")
	} else {
		item, err := StaticEvidence("source_file", goMod[0])
		if err != nil {
			return Finding{}, err
		}
		evidence = append(evidence, item)
	}
	reactFound := false
	for _, name := range tree.Paths() {
		if path.Base(name) != "package.json" {
			continue
		}
		body, err := tree.File(name)
		if err != nil {
			continue
		}
		if strings.Contains(body, `"react"`) {
			reactFound = true
			item, evErr := StaticEvidence("source_file", name+" 가 react 에 의존합니다")
			if evErr != nil {
				return Finding{}, evErr
			}
			evidence = append(evidence, item)
			break
		}
	}
	if !reactFound {
		missing = append(missing, "React 의존성(package.json)")
	}
	if len(missing) > 0 {
		return Finding{Result: standards.ResultUnmet, DefectKind: "missing_runtime_stack",
			TargetScope: ".", Detail: strings.Join(missing, ", ") + " 을(를) 찾지 못했습니다",
			Evidence: evidence}, nil
	}
	// Both are present. That is not the same as the names being used
	// consistently across screens, documents and images, which is the other
	// half of this criterion and which a file listing cannot see.
	finding := unknown("서비스명·로고·제품명이 화면과 문서에서 일치하는지는 실행해서 확인해야 합니다")
	finding.Evidence = evidence
	return finding, nil
}

// CFG-001: only four runtime environment variables.
//
// The contract names exactly four. Anything else the program reads at startup
// is a fifth thing an operator has to know about, and it is visible in the
// source.
type runtimeEnvDetector struct{}

func (runtimeEnvDetector) StandardID() string { return "CFG-001" }

var envRead = regexp.MustCompile(`os\.Getenv\("([A-Z0-9_]+)"\)|os\.LookupEnv\("([A-Z0-9_]+)"\)`)

// allowedRuntimeEnv is the four the contract permits, plus the ones that are
// not application configuration at all: a CI credential and a container's own
// PATH are different things wearing the same shape, and folding them together
// would report every repository as failing.
var allowedRuntimeEnv = map[string]bool{
	"POSTGRES_DSN": true, "BOOTSTRAP_ADMIN": true, "BOOTSTRAP_ADMIN_PASSWORD": true, "ENCRYPTION_KEY": true,
	"PATH": true, "HOME": true, "TZ": true, "PORT": true, "HOSTNAME": true,
}

func (runtimeEnvDetector) Detect(tree Tree) (Finding, error) {
	extra := map[string][]string{}
	for _, name := range tree.Paths() {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := tree.File(name)
		if err != nil {
			continue
		}
		for _, match := range envRead.FindAllStringSubmatch(body, -1) {
			variable := match[1]
			if variable == "" {
				variable = match[2]
			}
			if allowedRuntimeEnv[variable] {
				continue
			}
			extra[variable] = append(extra[variable], name)
		}
	}
	if len(extra) == 0 {
		// Nothing extra is read. Whether the four alone are enough to start is
		// a different question, and only starting it answers that.
		return unknown("네 변수만으로 실제 기동하는지는 기동해서 확인해야 합니다"), nil
	}
	names := make([]string, 0, len(extra))
	for variable := range extra {
		names = append(names, variable)
	}
	sort.Strings(names)
	var evidence []standards.Evidence
	for _, variable := range names {
		item, err := StaticEvidence("source_file", variable+": "+extra[variable][0])
		if err != nil {
			return Finding{}, err
		}
		evidence = append(evidence, item)
	}
	return Finding{Result: standards.ResultUnmet, DefectKind: "extra_runtime_env",
		TargetScope: "**/*.go",
		Detail:      fmt.Sprintf("런타임 환경변수 %s 를 추가로 읽습니다 — 계약은 네 종류입니다", strings.Join(names, ", ")),
		Evidence:    evidence}, nil
}

// NET-002: front-end assets served locally.
//
// A CDN URL in a source file is a runtime dependency on the internet, and on
// an isolated network it is a blank page. It is exactly the kind of thing a
// static read is good at: the string is either there or it is not.
type externalAssetDetector struct{}

func (externalAssetDetector) StandardID() string { return "NET-002" }

var externalHosts = []string{
	"cdn.jsdelivr.net", "unpkg.com", "cdnjs.cloudflare.com", "fonts.googleapis.com",
	"fonts.gstatic.com", "ajax.googleapis.com", "code.jquery.com", "stackpath.bootstrapcdn.com",
}

func (externalAssetDetector) Detect(tree Tree) (Finding, error) {
	assetSuffixes := []string{".html", ".css", ".js", ".jsx", ".ts", ".tsx"}
	var hits []string
	var evidence []standards.Evidence
	for _, name := range tree.Paths() {
		matched := false
		for _, suffix := range assetSuffixes {
			if strings.HasSuffix(name, suffix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		body, err := tree.File(name)
		if err != nil {
			continue
		}
		for _, host := range externalHosts {
			if !strings.Contains(body, host) {
				continue
			}
			hits = append(hits, host+" ("+name+")")
			item, evErr := StaticEvidence("source_file", name+" 가 "+host+" 를 참조합니다")
			if evErr != nil {
				return Finding{}, evErr
			}
			evidence = append(evidence, item)
		}
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		return Finding{Result: standards.ResultUnmet, DefectKind: "external_asset_reference",
			TargetScope: "web/**",
			Detail:      "런타임에 외부 자산을 불러옵니다: " + strings.Join(hits, ", "),
			Evidence:    evidence}, nil
	}
	return unknown("브라우저 요청과 서버 송신을 실제로 관찰해야 합니다"), nil
}

// REL-001: built assets inside the image, no front-end build at run time.
//
// A Dockerfile whose final stage installs Node or runs a build is building the
// front end when the container starts. On an isolated network that is a
// container that cannot start at all.
type runtimeBuildDetector struct{}

func (runtimeBuildDetector) StandardID() string { return "REL-001" }

func (runtimeBuildDetector) Detect(tree Tree) (Finding, error) {
	dockerfiles := []string{}
	for _, name := range tree.Paths() {
		if path.Base(name) == "Dockerfile" || strings.HasPrefix(path.Base(name), "Dockerfile.") {
			dockerfiles = append(dockerfiles, name)
		}
	}
	if len(dockerfiles) == 0 {
		return Finding{Result: standards.ResultUnmet, DefectKind: "missing_image_definition",
			TargetScope: ".", Detail: "이미지 정의(Dockerfile)를 찾지 못했습니다"}, nil
	}
	sort.Strings(dockerfiles)
	for _, name := range dockerfiles {
		body, err := tree.File(name)
		if err != nil {
			continue
		}
		stage := finalStage(body)
		for _, marker := range []string{"npm install", "npm ci", "yarn install", "pnpm install", "npm run build"} {
			if !strings.Contains(stage, marker) {
				continue
			}
			item, evErr := StaticEvidence("source_file", name+" 의 마지막 단계에 "+marker+" 가 있습니다")
			if evErr != nil {
				return Finding{}, evErr
			}
			return Finding{Result: standards.ResultUnmet, DefectKind: "frontend_build_at_runtime",
				TargetScope: name,
				Detail:      name + " 의 실행 단계가 프런트엔드를 빌드합니다 — 폐쇄망에서는 기동하지 못합니다",
				Evidence:    []standards.Evidence{item}}, nil
		}
	}
	item, err := StaticEvidence("source_file", strings.Join(dockerfiles, ", "))
	if err != nil {
		return Finding{}, err
	}
	finding := unknown("이미지를 실제로 로드해 기동해야 자산이 안에 들어 있는지 확인됩니다")
	finding.Evidence = []standards.Evidence{item}
	return finding, nil
}

// finalStage is the part of a Dockerfile after the last FROM, which is what
// actually runs. A build stage that installs Node is the correct way to do
// this, and flagging it would report the fix as the defect.
func finalStage(body string) string {
	lines := strings.Split(body, "\n")
	start := 0
	for i, line := range lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "FROM ") {
			start = i
		}
	}
	return strings.Join(lines[start:], "\n")
}
