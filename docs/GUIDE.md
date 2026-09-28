# GoalForge 사용 가이드

기준 버전: v0.10.0

이 문서는 GoalForge 를 **처음부터 끝까지 한 번 돌려 보는 순서**로 쓰여 있습니다. 명령어의 전체 목록은 `goalforge` 를 인자 없이 실행하거나 [README](../README.md) 를 보세요.

---

## 0. GoalForge 가 하는 일과 하지 않는 일

GoalForge 는 목표를 받아 작업으로 나누고, AI 세션이 그 작업을 격리된 작업 공간에서 구현하게 하고, 검증 게이트로 판정하고, 증거를 남기고, 승인을 받아 반영합니다.

**하지 않는 일**을 먼저 아는 편이 낫습니다.

- 모델이 "완료했다"고 말하는 것을 완료로 받아들이지 않습니다. 완료는 **현재 산출물에서 필수 조건이 증거로 입증될 때**만 성립합니다.
- 승인 없이 기본 브랜치에 반영하거나 원격에 push 하지 않습니다.
- 판정 방법이 없는 요구를 완료로 처리하지 않습니다. 미확정으로 표시하고 남깁니다.

---

## 1. 설치와 첫 진단

[releases](https://github.com/hkjang/goalforge/releases) 에서 플랫폼에 맞는 압축 파일을
내려받아 풀면 실행 가능한 바이너리 하나가 나옵니다. 별도 런타임이나 Go 툴체인은
필요하지 않습니다.

```sh
tar -xzf goalforge_vX.Y.Z_linux_amd64.tar.gz
sha256sum -c SHA256SUMS --ignore-missing   # 같은 릴리즈의 체크섬으로 검증
./goalforge version
goalforge doctor
```

`version` 은 상태 데이터베이스를 열기 전에 답합니다. 내려받은 바이너리에 가장 먼저 하는
질문이 "이게 뭔데"이고, 그 답은 아직 프로젝트를 고르지 않은 디렉터리에서도 나와야 합니다.

`doctor` 는 두 가지를 따로 확인합니다.

- **환경**: git, 제공자 CLI, 어댑터가 쓰는 플래그 지원 여부, 인증
- **준비 상태**: 이 프로젝트가 **끝날 수 있는지**

플래그 확인은 CLI 자신의 도움말을 읽습니다. 이때 **서브커맨드 도움말까지** 읽습니다 —
`codex` 의 `--json`·`--output-schema` 는 `codex exec --help` 에, `opencode` 의 `--format` 은
`opencode run --help` 에만 있기 때문입니다. 그리고 도움말에서 못 찾은 플래그는 **차단하지
않고 경고로** 보고합니다. `qwen --approval-mode` 는 실제로 동작하지만 어느 도움말에도
나오지 않습니다 — 문서에 없다는 것이 CLI에 없다는 뜻은 아닙니다.

```
WARN qwen flags  도움말에서 확인하지 못한 플래그: --approval-mode (읽은 도움말: qwen --help)
```

두 번째가 조용히 틀리는 쪽입니다. 완료 조건에 같은 이름의 게이트가 없으면 증거가 영원히 쌓이지 않아 목표가 끝나지 않는데, 모든 지표는 건강해 보입니다.

```
FAIL criteria coverage  완료 조건 latency_p95 을(를) 측정하는 게이트가 없어 증거가 영원히 쌓이지 않습니다
FAIL proof kind         완료 조건이 요구하는 검증 종류와 게이트가 다릅니다: note_saves (journey 필요, 게이트는 build)
WARN proof kind         동작을 확인하는 게이트가 없어 빌드만 통과해도 완료로 판정될 수 있습니다
```

두 번째 `proof kind` 가 그중 가장 건강해 보이는 오설정입니다. 게이트도 있고 이름도 맞고 통과도 하는데, **측정한 것이 조건이 묻는 것이 아닙니다.**

---

## 2. 프로젝트 등록

```sh
goalforge project init --name myapp --provider claude --model sonnet \
  --worktrees --auto-commit
goalforge project profile team          # 예산·동시성·복구 한도를 한 번에
```

`--worktrees` 는 각 작업을 별도 worktree 에서 실행합니다. 켜 두세요. 작업이 서로의 파일을 밟지 않고, 검증이 무엇을 측정했는지가 명확해집니다.

웹에서 시작하려면 `goalforge serve` 후 `#/new` 의 설정 마법사를 쓰면 됩니다. 저장소 선택 → 환경 진단 → 목표 → 완료 조건 → 실행 정책 순서이며, 차단 문제가 있으면 다음 단계로 넘어가지 않습니다.

---

## 3. 목표를 판정 가능한 형태로

완료 조건만으로 충분한 경우:

```sh
goalforge goal set --title "결제 실패율 개선" --objective "재시도 로직을 고친다" \
  --criterion build_passed=true --criterion coverage=85
```

조건 이름에 `@종류` 를 붙이면 **어떤 종류의 검증이라야 그 조건을 충족시킬 수 있는지**를 함께 못박습니다.

```sh
goalforge goal set --title "메모 앱" --objective "사용자가 메모를 저장할 수 있다" \
  --criterion build_passed@build=true --criterion note_saves@journey=true
```

종류는 `build`, `test`, `integration`, `journey`, `security`, `performance`, `review` 입니다. 화면이 다 만들어졌고 빌드도 초록이지만 저장 버튼이 모의 구현에 연결된 경우, 빌드 통과는 **사실이면서 아무것도 증명하지 않습니다**. `@journey` 를 요구하면 그 조건은 빌드 게이트의 통과로 충족되지 않고 `WRONG_KIND` 로 보고되며, 목표는 완료되지 않습니다.

```
[x]  note_saves       기준 true       측정 true    검증 종류 불일치: journey 종류의 검증이 필요하지만 build 게이트가 측정했습니다
```

- 더 강한 증거는 약한 요구를 충족시킵니다. 여정이 돌았다면 빌드는 이미 성공했기 때문입니다.
- `security` 와 `performance` 는 다른 어떤 검증으로도 대체되지 않는 별개의 속성입니다.
- 종류를 요구하지 않는 조건은 이전과 완전히 동일하게 동작합니다. 기존 프로젝트가 갑자기 미충족이 되지 않습니다.

요구가 모호하거나 여러 개일 때는 **계약**을 씁니다.

```sh
goalforge goal contract --title "상담 시스템" \
  --users "상담원" --scenarios "문의 접수부터 종료까지" \
  --outcome "login|gate:auth_tests|verification,handling_time" \
  --measure "p95=latency_ms<=200" \
  --exclude "결제 연동"
```

각 필수 결과는 **어떻게 판정되고 누가 판정하는지**를 함께 갖습니다. 둘 중 하나라도 없으면 미확정으로 표시되고 남습니다.

```
[v]  login            gate:auth_tests / verification
[?]  handling_time    판정 방법 또는 판정 주체가 없어 미확정

미확정 1건: 판정 방법과 주체를 정하기 전까지 이 목표는 완료로 판정될 수 없습니다.
```

둘 다 성립할 수 없는 요구는 **양쪽을 남긴 채** 상충으로 보고합니다. 어느 쪽을 바꿀지는 사람이 정합니다.

계약을 바꾸면 사유와 결정자가 있는 새 버전이 생기고, 이전 버전은 자신이 요구했던 것을 그대로 유지합니다. **범위를 줄여도 과거 판정이 바뀌지 않습니다.**

---

## 4. 검증 게이트

게이트가 없으면 어떤 실행도 검증되지 않고, 목표는 완료될 수 없습니다.

```sh
goalforge verify template go-api        # 프로젝트 유형별 시작 묶음
goalforge verify gate add --type coverage \
  --command-json '["go","test","-cover","./..."]' \
  --success-value 85 --value-pattern 'coverage:\s+([0-9.]+)%' --kind test
```

**게이트에는 `--kind` 로 그 게이트가 무엇을 증명하는지 밝힙니다.** 템플릿이 넣는 게이트는 모두 `build`/`test` 입니다. 그 프로젝트의 사용자 작업이 실제로 끝나는지는 프로젝트마다 다르므로 템플릿이 대신 채울 수 없고, `doctor` 가 그 공백을 경고합니다.

```sh
goalforge verify gate add --type note_saves \
  --command-json '["./scripts/journey-save.sh"]' --kind journey
```

게이트의 종류를 바꾸면 그 게이트가 과거에 남긴 증거는 **재검증 대상이 됩니다.** 컴파일을 "여정"이라고 다시 이름 붙여 옛 빌드 결과를 여정 증거로 승격시킬 수는 없습니다.

**수치 조건에는 `--value-pattern` 이 필요합니다.** 없으면 게이트가 통과했을 때 설정값을 그대로 실측값으로 기록해, 커버리지 조건이 상수 두 개를 비교하게 됩니다. 패턴이 있으면 출력에서 실제 값을 뽑아 임계값과 비교하고, 측정값이 없으면 실패 처리합니다.

### 게이트를 격리해서 실행하기

게이트는 **세션이 방금 쓴 코드를 실행**합니다. 세션보다 더 신뢰할 근거가 없습니다.

```sh
goalforge project sandbox --mode docker --image golang:1.23 \
  --memory-mb 2048 --cpus 2
```

작업 공간만 마운트되고, 네트워크는 끊기며, 능력은 제거되고, 루트 파일시스템은 읽기 전용입니다. 기본값이 호스트인 이유는 **프로젝트의 툴체인을 실행하지 못하는 샌드박스는 없느니만 못하기** 때문입니다.

---

## 5. 작업을 넣고, 실행 전에 확인하기

```sh
goalforge work add --title "재시도 백오프 구현" --priority 90 \
  --scope "internal/payment/**" --estimated-tokens 12000 \
  --depends-on WORK-1,WORK-2
goalforge plan
```

`plan` 은 **아무것도 바꾸지 않고** 다음 실행이 무엇을 할지 보여 줍니다.

```
다음 작업: WORK-… 재시도 백오프 구현
  실행 가능한 후보 3건 가운데 우선순위가 가장 높습니다

OK    gates                  4개 중 필수 3개가 실행 후 검증합니다
OK    token budget           120k / 2000k 사용 (94% 남음)
WARN  acceptance             완료 기준이 없어 무엇이 끝인지 실행 세션이 판단하게 됩니다
OK    forecast               약 15000 토큰 — 직접 입력한 예상치
```

`BLOCK` 은 실행이 거부된다는 뜻이고, `WARN` 은 실행은 되지만 결과에 문제가 있다는 뜻입니다 — 대개 목표가 완료로 판정될 수 없다는 뜻입니다. 둘은 다릅니다.

---

## 6. 실행

```sh
goalforge continue                 # 작업 1건을 실행하고 검증
goalforge worker                   # 자율 실행 (예약된 작업을 처리)
goalforge run --until-quota        # 한도까지 연속 실행
```

실행 중 무슨 일이 일어나는지:

1. 격리된 worktree 를 준비하고, **검증 게이트 원본을 확보**합니다
2. 실행 프롬프트에 **컨텍스트 패키지**를 싣습니다 — 확정된 설계 결정, 변경 제약, 이 작업의 이전 실패, 검증 방법
3. 세션이 구현합니다
4. **게이트를 원본으로 되돌리고** 검증합니다. 세션이 자신을 판정할 게이트를 다시 썼다면 그것은 반영되지 않습니다
5. 통과하면 작업 브랜치에 커밋합니다. 기본 브랜치는 건드리지 않습니다

### 실패했을 때

실패는 분류됩니다.

| 분류 | 자동 재시도 |
|---|---|
| 테스트 실패, 빌드 실패, 기준 미달 | 예 (횟수·비용 한도 안에서) |
| 환경 문제, 의존성, 인증 | **아니오** — 모델 재실행은 예산만 씁니다 |
| 제한 시간 초과, 게이트 설정 오류 | 아니오 — 사람의 판단 필요 |

```sh
goalforge status                   # 무엇이 막혔는지
goalforge reproduce --run RUN-1 --out ./repro   # 같은 조건으로 재현
```

---

## 7. 상태 읽기

```sh
goalforge status
```

```
Progress: 66.7% (2/3 가중치, 폐기 1건 기준선 제외)
Criteria:
  [v]  build_passed   기준 true  측정 true   근거 RUN-… 09-27 14:02
  [~]  coverage       기준 85    측정 88     재검증 필요: verified code changed
Needs you:
  - 승인 대기: MERGE_BRANCH — … (goalforge approval approve APR-…)
  - 통합 검증 필요: 병합 후 … (goalforge verify integration)
```

- `[v]` 충족 · `[~]` 재검증 필요 · `[!]` 기준 미달 · `[ ]` 증거 없음
- **작업 진행률과 필수 조건 충족은 다른 지표입니다.** 100% 진행이어도 조건이 미충족이면 완료가 아닙니다.
- 증거는 **측정한 트리와 게이트**를 기록하므로, 코드나 게이트가 바뀌면 자동으로 재검증 대상이 됩니다

---

## 8. 검토와 반영

```sh
goalforge serve                    # 대시보드에서 변경 검토
goalforge approval request --action merge-branch --work-item WORK-1 --reason "..."
goalforge approval list
goalforge approval approve APR-1
goalforge merge --work-item WORK-1
goalforge verify integration       # 병합 결과를 다시 검증
```

**승인은 검토한 그 변경에만 적용됩니다.** 작업·커밋 SHA·적용 대상에 고정되므로, 검토 후 커밋이 바뀌면 재검토를 요구합니다.

병합은 기본 브랜치를 **미검증 상태로** 만듭니다. 각 작업은 자신의 worktree 에서만 검증됐고, 합친 결과는 아직 아무도 검증하지 않았기 때문입니다. `verify integration` 이 그것을 채웁니다.

통합 검증이 실패하면 **통합 수정 작업이 만들어집니다.** 무엇이 어느 커밋에서 실패했는지 담긴 채 바로 집을 수 있는 상태로 들어갑니다 — 오류 메시지로 끝나면 아무도 배정되지 않고 브랜치는 출시 불가인 채 남습니다. 같은 미해결 통합에서 다시 실패하면 그 작업을 갱신하고, 해결된 뒤의 실패는 새 작업을 만듭니다.

반려할 때는 사유를 남기세요. 개별 반려가 아니라 **반복되는 이유**가 개선할 지점을 알려 줍니다.

```sh
goalforge approval reject APR-1 --category insufficient_evidence --note "커버리지 근거 없음"
```

---

## 9. 사람이 직접 고칠 때

```sh
goalforge takeover --work-item WORK-1 --reason "설계를 직접 바꿔야 함"
# ... 직접 수정 ...
goalforge takeover return --work-item WORK-1 --summary "인증 흐름 재작성"
```

인계는 일시정지가 아닙니다. 실행 중이면 거부하고, 작업 공간 소유권을 넘기고, 자동화가 그 작업을 집지 않게 합니다. 반환할 때 **게이트를 다시 실행**합니다 — 손으로 고친 것도 검증을 면제받지 않습니다.

한 건을 인계받아도 **자동화는 다른 작업을 계속합니다.** 동시 구현 한도는 에이전트가 쥔 작업만 셉니다.

---

## 10. 증거와 인수인계

```sh
goalforge evidence export --out ./evidence
goalforge pr --work-item WORK-1          # 목표·조건·검증이 연결된 PR 본문
goalforge report --since 24h             # 무엇이 끝났고 무엇이 막혔는지
```

증거 묶음은 목표 버전과 변경 사유, 작업별 커밋과 게이트 측정값, 설계 결정, 승인 이력, **반려된 승인과 완화된 기준**까지 한 문서로 묶습니다. 외부 리소스를 전혀 불러오지 않는 자체 완결형 HTML 이라 메일로 보내거나 몇 년 뒤에 열어도 동작합니다.

좋은 소식만 담은 묶음은 실제로 일어난 프로젝트와 다른 프로젝트를 서술합니다. 그래서 불편한 기록도 남깁니다.

---

## 11. 설계 결정 남기기

```sh
goalforge decision add --title "세션 저장소" \
  --decision "세션은 SQLite 에 보관한다" \
  --alternatives "메모리: 재시작에 살아남지 못함" \
  --consequences "동시 접근은 WAL 로 처리"
```

기록된 결정은 이후 모든 실행 프롬프트에 실립니다. 새 세션이 같은 결론을 다시 도출하거나, 이미 합의된 구조를 임의로 뒤집는 일을 줄입니다. 결정은 삭제되지 않고 **다른 결정으로 대체**됩니다 — 무엇이 기각됐는지가 그것이 다시 제안되는 것을 막습니다.

---

## 12. 구성 변경을 측정하기

프롬프트나 모델이나 정책을 바꿨을 때 **그 변경의 효과**를 알려면 고정된 과제가 필요합니다.

```sh
goalforge eval add --name "nil-deref" --kind bug_fix --objective "빈 입력에서 패닉이 나지 않는다"
goalforge eval spec --case EVAL-1 --fixture ./fixtures/bug1 --ref <commit> \
  --criterion build_passed=true --work "패닉 수정" \
  --gate-type build_passed --gate-command-json '["go","test","./..."]' \
  --token-budget 100000 --timeout-seconds 900
goalforge eval run --case EVAL-1 --label "sonnet-baseline" --repeat 3
goalforge eval compare --case EVAL-1
```

반복마다 **새로 클론한 작업 공간과 다른 시행을 본 적 없는 상태 DB** 에서 실행됩니다. 기준 상태에서 시작하지 않은 작업 공간은 측정 불가로 기록합니다 — 점수를 매기면 다른 과제를 측정하는 것이고, 버리면 유리한 실행만 남습니다.

보고는 **재실행 시행과 붙인 실행을 절대 합산하지 않고**, 반복 안정성을 단일 성공률과 나란히 보여 주며, 성공당 비용에 실패한 시도를 포함합니다.

---

## 13. 권한 경계

세션은 저장소를 바꿀 수 있지만 **무엇이 수용 가능한지를 결정하는 것**은 바꿀 수 없습니다.

제공자 프로세스와 검증 게이트는 역할 표시를 달고 실행되며, 그 안에서 시작된 것은 전부 상속합니다. 승인, 목표 변경, 게이트 변경, 예산·정책 변경, merge, publish, 평가 기준, 인계는 CLI·MCP 어디서도 거부됩니다.

환경도 좁혀집니다. `PATH`, `HOME`, 제공자 자격증명 계열만 넘어가고 관련 없는 배포 토큰은 넘어가지 않습니다. 도구가 특정 변수를 꼭 필요로 하면:

```sh
export GOALFORGE_PASS_ENV=DEPLOY_TOKEN,REGISTRY_URL
```

---

## 14. 장애가 났을 때

```sh
goalforge effects --reconcile      # 바깥에 무엇을 했는지 정산
goalforge status
```

- **후속 작업 유실 없음**: 계속하겠다는 의도가 결과와 같은 트랜잭션에 기록되고, 워커가 시작할 때 작업으로 바뀝니다
- **중복 외부 변경 없음**: push·merge 는 원장을 거치고, 결과가 불명확하면 재시도 전에 원격에 실제 상태를 묻습니다. 확인할 수 없으면 **아무것도 재시도하지 않습니다**
- **오래된 워커 차단**: 임대가 만료되거나 취소되면 세대가 올라가고, 이전 세대의 확정은 거부됩니다

### 백업과 복원

```sh
goalforge backup --out ./state.backup
goalforge restore --from ./state.backup --to ./restored/state.db
```

백업은 파일을 그대로 복사하지 않고 SQLite 자체 메커니즘을 씁니다. 워커가 쓰는 중에 뜬 복사본은 **절반만 적용된 트랜잭션의 복사본**이기 때문입니다.

복원은 세 가지를 합니다. 살아 있는 상태를 덮어쓰지 않고, 복원된 기록이 백업 당시와 같은지 대조하며, GoalForge 가 바깥에 시작해 둔 것을 정산합니다. **미해결 push 를 둔 채 재개하는 것이 복원이 중복을 만드는 방식입니다.** 결과를 확인할 수 없는 효과가 하나라도 있으면 "safe to resume" 을 말하지 않고 멈춥니다.

---

## 자주 묻는 것

**목표가 100% 인데 완료가 아니라고 나옵니다.**
작업 가중치와 필수 조건은 다른 지표입니다. `status` 의 `Criteria` 를 보세요. `[~]` 는 코드나 게이트가 바뀌어 재검증이 필요하다는 뜻입니다.

**게이트가 계속 실패하는데 자동 수정이 안 됩니다.**
실패가 환경·의존성·인증으로 분류됐을 가능성이 큽니다. 모델을 다시 돌려도 해결되지 않는 종류라 재시도하지 않습니다. `goalforge status` 의 `Needs you` 를 보세요.

**승인했는데 merge 가 거부됩니다.**
승인 이후 그 작업이 다시 실행되어 새 커밋이 생겼을 수 있습니다. 승인은 검토한 커밋에 고정되므로 새 변경은 다시 검토해야 합니다.

**샌드박스에서 게이트가 실패합니다.**
이미지에 프로젝트의 툴체인이 있는지, 네트워크가 필요한 작업이라면 `--network` 를 켰는지 확인하세요. 의심되면 `--mode none` 으로 되돌려 비교하면 됩니다.
