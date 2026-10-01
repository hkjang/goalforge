package observer

import (
	"errors"
	"strings"
	"testing"

	"github.com/goalforge/goalforge/internal/standards"
)

func inForce(ids ...string) []standards.Standard {
	pack := standards.GoReactOfflineService()
	var result []standards.Standard
	for _, id := range ids {
		standard, ok := pack.Standard(id)
		if !ok {
			panic("unknown criterion " + id)
		}
		result = append(result, standard)
	}
	return result
}

// The guard that everything else rests on. A detector labelling a file read as
// a browser_test_result would pass every other check, because the judge looks
// at the kind of evidence and not at who produced it.
func TestAFileReadCannotProduceATestResult(t *testing.T) {
	for _, kind := range []string{"browser_test_result", "integration_test_result", "security_test_result",
		"build_log", "release_asset", "screenshot"} {
		if _, err := StaticEvidence(kind, "웹 화면을 보니 될 것 같습니다"); !errors.Is(err, ErrExecutedEvidence) {
			t.Fatalf("%s must be refused to a static read: %v", kind, err)
		}
	}
	item, err := StaticEvidence("source_file", "web/src/App.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if !item.Observed {
		t.Fatal("something actually read from the tree is observed")
	}
	if _, err = StaticEvidence("source_file", "  "); err == nil {
		t.Fatal("evidence with no content is not evidence")
	}
}

// A static detector returning MET would settle a criterion by reading. Catching
// it here rather than relying on the judge means the bug surfaces at the
// detector that has it.
func TestAStaticDetectorMayNotClaimSomethingIsMet(t *testing.T) {
	_, err := Observe(MemoryTree{"go.mod": "module x"}, inForce("CORE-001"),
		[]Detector{fixedDetector{id: "CORE-001", result: standards.ResultMet}})
	if err == nil || !strings.Contains(err.Error(), "CORE-001") {
		t.Fatalf("a static MET must be refused and named: %v", err)
	}
	if _, err = Observe(MemoryTree{}, inForce("CORE-001"),
		[]Detector{fixedDetector{id: "CORE-001", result: standards.ResultNotApplicable}}); err == nil {
		t.Fatal("a detector cannot excuse a criterion either")
	}
}

type fixedDetector struct{ id, result string }

func (d fixedDetector) StandardID() string { return d.id }
func (d fixedDetector) Detect(Tree) (Finding, error) {
	return Finding{Result: d.result, DefectKind: "x", TargetScope: "."}, nil
}

// "We looked and found nothing wrong" and "nobody looked" are different, and a
// report that omits the second reads as coverage of a catalogue nobody walked.
func TestCriteriaNoDetectorCoversAreReportedAsUnchecked(t *testing.T) {
	observation, err := Observe(MemoryTree{"go.mod": "module x"},
		inForce("CORE-001", "UX-004", "QA-001"), Default())
	if err != nil {
		t.Fatal(err)
	}
	unchecked := map[string]bool{}
	for _, id := range observation.Unchecked {
		unchecked[id] = true
	}
	if !unchecked["UX-004"] || !unchecked["QA-001"] {
		t.Fatalf("criteria with no detector must be listed: %v", observation.Unchecked)
	}
	if unchecked["CORE-001"] {
		t.Fatal("CORE-001 has a detector and was checked")
	}
}

// A repository with a Go module and no React front end is missing half the
// stack, and that is visible in the tree.
func TestTheStackDetectorProvesWhatIsAbsent(t *testing.T) {
	finding, err := goAndReactDetector{}.Detect(MemoryTree{"go.mod": "module example.com/x\n"})
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet || !strings.Contains(finding.Detail, "React") {
		t.Fatalf("finding=%+v", finding)
	}
	// With both present it stops short of claiming the criterion is met: the
	// other half is about names matching across screens and documents, which a
	// file listing cannot see.
	both := MemoryTree{"go.mod": "module example.com/x\n",
		"web/package.json": `{"dependencies":{"react":"18.2.0"}}`}
	finding, err = goAndReactDetector{}.Detect(both)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnknown {
		t.Fatalf("a file listing cannot settle this upward: %+v", finding)
	}
	if !strings.Contains(finding.Detail, "확인해야") {
		t.Fatalf("the unknown must say what would settle it: %q", finding.Detail)
	}
}

// A fifth environment variable is a fifth thing an operator has to know about,
// and it is right there in the source.
func TestTheEnvironmentDetectorNamesTheExtraVariables(t *testing.T) {
	tree := MemoryTree{
		"internal/config/config.go": `package config
func Load() {
	dsn := os.Getenv("POSTGRES_DSN")
	redis := os.Getenv("REDIS_URL")
	smtp, _ := os.LookupEnv("SMTP_HOST")
	_ = os.Getenv("PATH")
}`,
		"internal/config/config_test.go": `package config
func TestX() { os.Getenv("TEST_ONLY_KNOB") }`,
	}
	finding, err := runtimeEnvDetector{}.Detect(tree)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet {
		t.Fatalf("finding=%+v", finding)
	}
	for _, want := range []string{"REDIS_URL", "SMTP_HOST"} {
		if !strings.Contains(finding.Detail, want) {
			t.Fatalf("%s must be named: %q", want, finding.Detail)
		}
	}
	// The four in the contract are not extra, PATH is not application
	// configuration, and a test file is not the runtime.
	for _, absent := range []string{"POSTGRES_DSN", "PATH", "TEST_ONLY_KNOB"} {
		if strings.Contains(finding.Detail, absent) {
			t.Fatalf("%s must not be reported: %q", absent, finding.Detail)
		}
	}
	// A program that reads nothing extra still has not been shown to start on
	// the four alone.
	clean, err := runtimeEnvDetector{}.Detect(MemoryTree{"main.go": `package main
func main() { _ = os.Getenv("POSTGRES_DSN") }`})
	if err != nil {
		t.Fatal(err)
	}
	if clean.Result != standards.ResultUnknown {
		t.Fatalf("starting is settled by starting: %+v", clean)
	}
}

// A CDN URL is a runtime dependency on the internet, and on an isolated
// network it is a blank page.
func TestTheAssetDetectorFindsExternalReferences(t *testing.T) {
	tree := MemoryTree{"web/index.html": `<link href="https://fonts.googleapis.com/css2?family=Inter">`}
	finding, err := externalAssetDetector{}.Detect(tree)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet || !strings.Contains(finding.Detail, "fonts.googleapis.com") {
		t.Fatalf("finding=%+v", finding)
	}
	if finding.DefectKind != "external_asset_reference" {
		t.Fatalf("the defect kind goes into the dedup key: %q", finding.DefectKind)
	}
	// A Go file mentioning a CDN in a comment is not a front-end asset request.
	clean, err := externalAssetDetector{}.Detect(MemoryTree{
		"internal/doc.go": "// see https://cdn.jsdelivr.net for the thing we do not use"})
	if err != nil {
		t.Fatal(err)
	}
	if clean.Result != standards.ResultUnknown {
		t.Fatalf("a comment in a Go file is not an asset reference: %+v", clean)
	}
}

// Installing Node in a build stage is the correct way to do this. Flagging it
// would report the fix as the defect.
func TestTheImageDetectorLooksAtTheStageThatRuns(t *testing.T) {
	correct := MemoryTree{"Dockerfile": `FROM node:20 AS web
RUN npm ci && npm run build
FROM golang:1.24 AS server
RUN go build -o /goalforge ./cmd/goalforge
FROM gcr.io/distroless/base
COPY --from=web /app/dist /web
COPY --from=server /goalforge /goalforge
ENTRYPOINT ["/goalforge"]`}
	finding, err := runtimeBuildDetector{}.Detect(correct)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnknown {
		t.Fatalf("a build stage that installs Node is the right shape: %+v", finding)
	}
	broken := MemoryTree{"Dockerfile": `FROM node:20
COPY . .
CMD npm install && npm run build && ./server`}
	finding, err = runtimeBuildDetector{}.Detect(broken)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet || finding.DefectKind != "frontend_build_at_runtime" {
		t.Fatalf("finding=%+v", finding)
	}
	missing, err := runtimeBuildDetector{}.Detect(MemoryTree{"go.mod": "module x"})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Result != standards.ResultUnmet || missing.DefectKind != "missing_image_definition" {
		t.Fatalf("finding=%+v", missing)
	}
}

// Every finding must carry a defect kind and a target, because those two are
// the dedup key. A finding without them files a new card every cycle.
func TestEveryFindingCarriesADedupableDefect(t *testing.T) {
	trees := []Tree{
		MemoryTree{"go.mod": "module x"},
		MemoryTree{"main.go": `package main
func main(){ os.Getenv("REDIS_URL") }`},
		MemoryTree{"web/app.js": `fetch("https://unpkg.com/x")`},
		MemoryTree{"Dockerfile": "FROM node:20\nCMD npm install && node ."},
	}
	for _, tree := range trees {
		observation, err := Observe(tree, inForce("CORE-001", "CFG-001", "NET-002", "REL-001"), Default())
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range observation.Findings {
			if finding.Result != standards.ResultUnmet {
				continue
			}
			if finding.DefectKind == "" || finding.TargetScope == "" {
				t.Fatalf("%s: an UNMET finding needs a defect kind and a target: %+v", finding.StandardID, finding)
			}
		}
	}
}

// A detector that only looks where it expects to find things reports an
// absence it did not establish. The platform matrix of this very repository
// lives in a script the workflow calls, and a detector reading only the
// workflow reported a project building six platforms as building none.
func TestTheReleaseDetectorLooksWhereTheMatrixActuallyLives(t *testing.T) {
	// Workflow calls a script; the script holds the matrix.
	split := MemoryTree{
		".github/workflows/release.yml": "on:\n  push:\n    tags: ['v*']\njobs:\n  publish:\n    steps:\n      - run: ./scripts/build-release.sh\n",
		"scripts/build-release.sh":      "for GOOS in linux darwin windows; do GOARCH=amd64 go build; done\nsha256sum * > SHA256SUMS\n",
	}
	finding, err := releaseMatrixDetector{}.Detect(split)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result == standards.ResultUnmet {
		t.Fatalf("the matrix is in the script the workflow calls: %+v", finding)
	}
	// A project that genuinely builds one platform is still found.
	single := MemoryTree{
		".github/workflows/release.yml": "jobs:\n  publish:\n    steps:\n      - run: go build -o out\n",
	}
	finding, err = releaseMatrixDetector{}.Detect(single)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet {
		t.Fatalf("one platform and no checksums is a real gap: %+v", finding)
	}
	if !strings.Contains(finding.Detail, "GOOS") || !strings.Contains(finding.Detail, "체크섬") {
		t.Fatalf("the finding must name both: %q", finding.Detail)
	}
	// No workflow at all is a different finding from an incomplete one.
	none, err := releaseMatrixDetector{}.Detect(MemoryTree{"go.mod": "module x"})
	if err != nil {
		t.Fatal(err)
	}
	if none.DefectKind != "no_release_workflow" {
		t.Fatalf("finding=%+v", none)
	}
}

// A workflow that runs on a tag checks the release, which is after the
// decision. Only one that runs on a pull request gates a change.
func TestOnlyAWorkflowOnPullRequestsGatesAChange(t *testing.T) {
	tagOnly := MemoryTree{".github/workflows/release.yml": "on:\n  push:\n    tags: ['v*']\njobs:\n  t:\n    steps:\n      - run: go test ./... && go vet ./... && gofmt -l .\n"}
	finding, err := ciGateDetector{}.Detect(tagOnly)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet {
		t.Fatalf("a tag workflow does not gate a change: %+v", finding)
	}
	onPR := MemoryTree{".github/workflows/ci.yml": "on:\n  pull_request:\njobs:\n  t:\n    steps:\n      - run: go test ./...\n      - run: go vet ./...\n      - run: gofmt -l .\n"}
	finding, err = ciGateDetector{}.Detect(onPR)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result == standards.ResultUnmet {
		t.Fatalf("finding=%+v", finding)
	}
	// A pull-request workflow missing one of the three names which.
	partial := MemoryTree{".github/workflows/ci.yml": "on:\n  pull_request:\njobs:\n  t:\n    steps:\n      - run: go test ./...\n"}
	finding, err = ciGateDetector{}.Detect(partial)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet || !strings.Contains(finding.Detail, "vet") {
		t.Fatalf("finding=%+v", finding)
	}
}

// A replace pointing outside the module makes the build depend on a path that
// is not in the repository, so the same commit does not build the same way
// somewhere else.
func TestAReplaceOutsideTheModuleIsFound(t *testing.T) {
	outside := MemoryTree{"go.mod": "module example.com/x\n\ngo 1.24\n\nreplace example.com/dep => ../dep\n"}
	finding, err := modulePinDetector{}.Detect(outside)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result != standards.ResultUnmet || finding.DefectKind != "replace_outside_module" {
		t.Fatalf("finding=%+v", finding)
	}
	// A replace within the module is how a multi-module repository works.
	inside := MemoryTree{"go.mod": "module example.com/x\n\ngo 1.24\n\nreplace example.com/dep => ./dep\n"}
	finding, err = modulePinDetector{}.Detect(inside)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Result == standards.ResultUnmet {
		t.Fatalf("a replace inside the repository is fine: %+v", finding)
	}
	// No go directive leaves the toolchain unpinned.
	loose := MemoryTree{"go.mod": "module example.com/x\n"}
	finding, err = modulePinDetector{}.Detect(loose)
	if err != nil {
		t.Fatal(err)
	}
	if finding.DefectKind != "no_go_directive" {
		t.Fatalf("finding=%+v", finding)
	}
}
