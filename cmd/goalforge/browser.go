package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goalforge/goalforge/internal/browser"
	"github.com/goalforge/goalforge/internal/policy"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// browserServiceEnv names the environment variable holding the service
// address. It is read from the environment rather than stored per project
// because the address is a property of the network the worker is on, and two
// workers in different places legitimately reach different instances.
const browserServiceEnv = "GOALFORGE_BROWSER_URL"

func browserBaseURL(flagValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return strings.TrimSpace(flagValue)
	}
	return strings.TrimSpace(os.Getenv(browserServiceEnv))
}

// browserRun executes a script on the playwright-player service.
//
// It is shaped as a command so an ordinary verification gate can call it:
//
//	goalforge verify gate add --type route_restore --kind journey \
//	  --command-json '["goalforge","browser","run","--script","ux/route-restore"]'
//
// Nothing new has to understand browsers — the gate runs a command, the
// command exits zero or not, and the existing evidence machinery carries the
// result exactly as it carries a build's.
func browserRun(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("browser run", flag.ContinueOnError)
	service := set.String("service", "", "playwright-player 주소 (기본: "+browserServiceEnv+")")
	script := set.String("script", "", "실행할 스크립트 키")
	project := set.String("project", "chromium", "브라우저 프로젝트")
	baseURL := set.String("base-url", "", "검사 대상 주소")
	grep := set.String("grep", "", "실행할 검사 필터")
	screenshot := set.String("screenshot", "on", "스크린샷: on, off, only-on-failure")
	trace := set.String("trace", "retain-on-failure", "트레이스 보관 방식")
	timeout := set.Duration("timeout", 10*time.Minute, "한 실행의 제한 시간")
	variables := set.String("variables", "", "key=value,key=value 형식의 변수")
	if err := set.Parse(args); err != nil {
		return err
	}
	base := browserBaseURL(*service)
	if base == "" {
		return fmt.Errorf("%s 를 설정하거나 --service 로 playwright-player 주소를 주세요", browserServiceEnv)
	}
	client := browser.Client{BaseURL: base, Timeout: *timeout}
	request := browser.RunRequest{ScriptKey: *script, Project: *project, BaseURL: *baseURL, Grep: *grep,
		Screenshot: *screenshot, Trace: *trace, Variables: map[string]string{}}
	for _, pair := range splitList(*variables) {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("%q 는 key=value 형식이 아닙니다", pair)
		}
		request.Variables[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	result, err := client.Run(ctx, request)
	if err != nil {
		// Returned, not printed. The verification engine captures stdout and
		// stderr into the same buffer, so printing it here as well would put
		// the same sentence in the evidence twice.
		return err
	}
	fmt.Printf("browser: %s %s — %s\n", result.RunID, result.Status, result.Detail)
	fmt.Printf("browser: 통과 %d · 실패 %d · 건너뜀 %d · 불안정 %d\n",
		result.Expected, result.Failed, result.Skipped, result.Flaky)
	for _, shot := range result.Screenshots {
		fmt.Printf("browser: 화면 %s\n", shot)
	}
	if !result.Passed {
		// A non-zero exit is what makes this a gate rather than a report. The
		// detail is already printed; returning it again would print it twice.
		return errors.New(result.Detail)
	}
	return nil
}

// browserCheck reports whether the service is reachable.
func browserCheck(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("browser check", flag.ContinueOnError)
	service := set.String("service", "", "playwright-player 주소")
	if err := set.Parse(args); err != nil {
		return err
	}
	base := browserBaseURL(*service)
	if base == "" {
		return fmt.Errorf("%s 를 설정하거나 --service 로 playwright-player 주소를 주세요", browserServiceEnv)
	}
	health, err := browser.Client{BaseURL: base, Timeout: 15 * time.Second}.Health(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s — 스크립트 %d개 · 실행 기록 %d건\n", health.Service, health.Version, health.ScriptCount, health.RunCount)
	return nil
}

// browserGate wires a script to a criterion in one step.
//
// Doing it as two commands works and is what someone does the second time.
// The first time, the gate and the claim are one intention — "this script is
// what proves UX-004" — and splitting it is how a project ends up with a gate
// that runs and settles nothing.
func browserGate(ctx context.Context, s *store.Store, args []string) error {
	set := flag.NewFlagSet("browser gate", flag.ContinueOnError)
	checkType := set.String("type", "", "게이트 이름")
	script := set.String("script", "", "실행할 스크립트 키")
	settles := set.String("settles", "", "이 게이트가 정산하는 기준 ID 목록")
	baseURL := set.String("base-url", "", "검사 대상 주소")
	timeoutSeconds := set.Int("timeout", 900, "게이트 제한 시간(초)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*checkType) == "" || strings.TrimSpace(*script) == "" {
		return errors.New("--type 과 --script 가 필요합니다")
	}
	project, _, pack, err := standardsContext(ctx, s)
	if err != nil {
		return err
	}
	command := []string{"goalforge", "browser", "run", "--script", *script}
	if *baseURL != "" {
		command = append(command, "--base-url", *baseURL)
	}
	if err = policy.ValidateCommand(command); err != nil {
		return fmt.Errorf("verification gate command rejected: %w", err)
	}
	if err = s.UpsertGate(ctx, project.ID, store.GateConfig{Type: *checkType, Command: command,
		Timeout: time.Duration(*timeoutSeconds) * time.Second, Required: true, SuccessValue: "true",
		Kind: policy.KindJourney}); err != nil {
		return err
	}
	// A browser run yields the journey result, the route it exercised and the
	// screenshots it took. Declaring them is what lets a criterion asking for
	// a screenshot ever be settled.
	claim := store.GateClaim{CheckType: *checkType, Settles: splitList(*settles),
		Produces: []string{"route", "screenshot"}}
	if err = s.SetGateClaim(ctx, project.ID, claim, pack); err != nil {
		return err
	}
	fmt.Printf("%s 게이트가 %s 를 실행하고 %s 를 정산합니다\n", *checkType, *script, strings.Join(claim.Settles, ", "))
	return nil
}
