package policy

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// ErrSandboxUnsupportedEngine means the container engine cannot enforce what
// this sandbox promises.
//
// The docker sandbox exists to make three specific statements true: the
// container root is read-only, capabilities are dropped, and no process can
// gain new privileges. A Windows-container daemon supports none of them and
// rejects --read-only outright. Running without those flags would still be
// called "the sandbox" while confining nothing, and a guarantee that quietly
// degrades is worse than one that is absent — the operator stops watching.
var ErrSandboxUnsupportedEngine = errors.New("이 컨테이너 엔진은 샌드박스가 보장하는 격리를 적용할 수 없습니다")

// EngineOS reports the operating system the container daemon runs containers
// for, which is not always the host's: Docker Desktop can be in Linux or
// Windows container mode on the same machine.
func EngineOS(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Os}}").Output()
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(string(output))), nil
}

// CheckEngine reports whether the configured sandbox can actually be enforced
// here. It is called before the first gate runs rather than left to surface as
// a gate failure, because "docker rejected an option" reads like the project
// is broken when it means the sandbox is not available.
func (p SandboxPolicy) CheckEngine(ctx context.Context) error {
	if p.Mode != SandboxDocker {
		return nil
	}
	engineOS, err := EngineOS(ctx)
	if err != nil {
		// Whether docker is installed at all is a separate finding that
		// diagnostics already reports; not being able to ask is not the same
		// as being told no.
		return nil
	}
	if engineOS != "" && engineOS != "linux" {
		return errors.Join(ErrSandboxUnsupportedEngine,
			errors.New("컨테이너 엔진이 "+engineOS+" 컨테이너 모드입니다. "+
				"읽기 전용 루트·능력 제거·권한 상승 차단이 적용되지 않으므로 거부합니다. "+
				"Linux 컨테이너로 전환하거나 project sandbox --mode none 으로 명시적으로 끄세요"))
	}
	return nil
}
