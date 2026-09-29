# 공통 개발 기준

프롬프트는 검사할 수 없습니다.

프로젝트마다 복사한 긴 문단은 인용할 수는 있어도 정산되지 않습니다. 그렇게 모인 문서는 아무것도 재지 않으면서 포괄적으로 보입니다. GoalForge 의 공통 개발 기준은 데이터입니다 — 기준 하나가 **무엇으로 정산되는지**와 **그러려면 어떤 근거가 필요한지**를 함께 싣기 때문에, "이 항목은 충족되었다" 는 프로그램이 거절할 수 있는 주장이 됩니다.

---

## 기준 하나의 모양

```go
{ID: "UX-004", Revision: 1, Title: "새로고침과 직접 경로 접근 유지",
 Category: "usability", Severity: SeverityRequired,
 AppliesWhen: map[string]string{"frontend": "react"},
 Checks: []Check{
   {Type: "browser_journey", Assertion: "중첩 경로 직접 접근 시 같은 화면을 표시한다"},
   {Type: "browser_journey", Assertion: "필터를 적용한 뒤 새로고침해도 필터가 유지된다"},
 },
 EvidenceRequired: []string{"commit_sha", "route", "browser_test_result", "screenshot"},
 ChangeScopeHint:  []string{"web/src/routes/**", "internal/http/**"}}
```

| 필드 | 없으면 |
| --- | --- |
| `Checks` | 문서 속 문장입니다. 사람들에게 인용될 뿐 끝나지 않습니다 |
| `EvidenceRequired` | 의견으로 정산됩니다 |
| `AppliesWhen` | AI 가 없는 프로젝트가 스트리밍 기준을 어기고 있는 것으로 보고됩니다 |
| `ChangeScopeHint` | 생성된 과제가 저장소 전체를 범위로 들고 옵니다 |

**필수 기준은 실행해야만 얻는 근거를 요구해야 합니다.** 커밋 SHA 와 경로는 대상을 가리킬 뿐 동작을 말하지 않습니다. 읽어서 정산되는 필수 기준은 저장 버튼이 stub 에 연결된 화면을 "저장 가능" 으로 통과시킵니다.

실행 근거로 인정되는 종류: `browser_test_result`, `integration_test_result`, `security_test_result`, `build_log`, `release_asset`, `screenshot`.

권장(`recommended`) 기준은 읽어서 정산해도 됩니다. 발견 사항이지 게이트가 아니기 때문입니다.

---

## 프로젝트 프로필

프로젝트는 팩 버전을 **고정합니다**.

```go
Profile{ProjectID: "PRJ-1", PackRef: "go-react-offline-service@0.1",
        Attributes: map[string]string{"frontend": "react", "network": "offline"}}
```

`Attributes` 는 기준의 `AppliesWhen` 과 맞춰집니다. 맞지 않는 기준은 이 프로젝트의 결함이 아니므로 현황에 나오지 않습니다.

새 팩 버전은 **차이를 제안할 뿐 다시 판정하지 않습니다.**

```go
diff := standards.DiffPacks(v01, v02)
// diff.Added   — 새로 생긴 기준
// diff.Removed — 없어진 기준
// diff.Revised — 개정되어 기존 평가를 다시 돌려야 하는 기준
```

저장소는 그대로인데 어제 끝낸 일이 오늘 결함이 되면 안 됩니다.

팩 체크섬도 함께 고정됩니다. 제자리에서 수정된 팩은 버전이 그대로이므로, 체크섬이 없으면 프로필은 여전히 `0.1` 이라고 말하는데 프로젝트는 동의한 적 없는 기준으로 판정됩니다.

---

## 예외

```go
Exception{StandardID: "NET-002", Reason: "자산 반입이 아직 끝나지 않았습니다",
          Decider: "hkjang", ReviewWhen: "반입 완료 시"}
```

| 요구 | 이유 |
| --- | --- |
| 사유 | — |
| 결정자 | 아무도 소유하지 않은 예외는 누락이 결정의 옷을 입은 것입니다 |
| 재검토 날짜 또는 조건 (**필수 기준만**) | 없으면 정당화했던 사정보다 예외가 오래 살아남고 아무도 눈치채지 못합니다 |

기한이 지난 예외는 **더 이상 면제하지 않습니다.** 그렇지 않으면 6주짜리 유예가 영구가 됩니다.

팩에 없는 기준에 대한 예외는 거절됩니다. 아무것도 면제하지 못하면서 오타를 결정처럼 보이게 합니다.

`AppliesWhen` 에 맞지 않는 것과 예외로 뺀 것은 **구분해서** 보고합니다. 앞은 프로젝트에 대한 사실이고 뒤는 누군가 내린 결정입니다.

---

## 판정

다섯 가지 결과를 씁니다. 각각이 요구하는 행동이 다르기 때문입니다.

| 결과 | 뜻 | 다음 행동 |
| --- | --- | --- |
| `MET` | 필요한 종류의 근거가 모두 관측되었다 | 없음 |
| `UNMET` | 충족되지 않았다 | 작업 |
| `PARTIAL` | 일부만 | 더 작은 작업 |
| `UNKNOWN` | 판단할 근거가 없다 | 조사 |
| `NOT_APPLICABLE` | 적용하지 않기로 결정했다 | 없음 (예외 기록 필요) |

**판정은 위로 올라가는 실패만 막습니다.**

```
근거가 아예 없음                → UNKNOWN  "browser_test_result 근거가 없습니다"
근거 항목은 있는데 내용이 빔    → UNKNOWN  표시만 된 체크박스는 근거가 아닙니다
근거가 실행이 아니라 추정       → UNKNOWN  "browser_test_result 근거가 추정입니다"
모든 필수 종류가 관측됨          → MET
```

`UNMET` 은 불필요할 수도 있는 작업을 만들고 `MET` 은 아무도 확인하지 않은 것을 내보냅니다. 비싼 쪽은 후자라 그 방향만 막습니다.

`NOT_APPLICABLE` 은 **기록된 예외가 있을 때만** 가능합니다. 관측기가 스스로 제외할 수 있으면, 프로젝트를 재는 것이 무엇으로 재일지를 정하게 됩니다.

측정하지 않은 기준은 빠뜨리지 않고 `UNKNOWN` 으로 보고합니다. 측정한 것만 보여 주는 목록은 아무도 다 걷지 않은 카탈로그를 완전한 것으로 읽히게 합니다.

기준 개정판이 올라간 뒤의 옛 평가는 `stale` 로 표시합니다. 그 평가는 잰 것에 대해서는 여전히 유효하고, 지금 묻는 질문에 답하지 않을 뿐입니다.

---

## 중복 접수 방지

같은 결함은 두 번 접수되지 않습니다. 키는 **제목이 아닙니다.**

```
dedup_key = sha256(프로젝트 + 기준 ID + 결함 종류 + 대상 범위)
```

새 아이디어를 요구받은 생성기는 같은 공백에 대해 매 사이클 새 문장을 써냅니다. 그걸 다 받는 보드는 진짜 백로그를 바꿔 쓴 말들 아래 묻습니다. 제목 유사도는 이 키가 보지 못하는 경우를 위한 보조 수단이지 주 수단이 아닙니다.

같은 기준이라도 결함 종류나 대상 범위가 다르면 다른 발견입니다.

---

## 실린 팩

`go-react-offline-service@0.1` — Go 서버 + React 프런트엔드, 폐쇄망 운영을 전제한 41개 기준.

| 분류 | 기준 |
| --- | --- |
| `architecture` | CORE-001, API-001 |
| `configuration` | CFG-001 ~ CFG-004 |
| `network` | NET-001, NET-002 |
| `auth` | AUTH-001 ~ AUTH-005 |
| `usability` | UX-001 ~ UX-007 |
| `ai` | AI-001 ~ AI-003 |
| `security` | KEY-001 ~ KEY-003, CFG-004, INT-002 |
| `integration` | INT-001 ~ INT-003 |
| `workflow` | WF-001, WF-002 |
| `quality` | QA-001, QA-002 |
| `release` | REL-001 ~ REL-004 |
| `documentation` | DOC-001 ~ DOC-004 |

조건부로만 적용되는 기준은 `AppliesWhen` 을 답니다.

| 속성 | 값 | 적용되는 기준 |
| --- | --- | --- |
| `frontend` | `react` | UX-004 |
| `network` | `offline` | NET-001, NET-002 |
| `ai` | `true` | AI-001 ~ AI-003 |
| `social_login` | `true` | AUTH-002, AUTH-003, AUTH-005 |
| `api_keys` | `true` | KEY-001, KEY-002 |
| `connectors` | `true` | INT-001 ~ INT-003 |
| `approval_workflow` | `true` | WF-001, WF-002 |

카탈로그는 자기가 강제하는 규칙을 통과하는지 시험받습니다. 누가 제출했으면 거절당했을 카탈로그는 아무도 검사하지 않은 카탈로그입니다.

---

---

## 저장소를 읽어 평가하기

```sh
goalforge standards profile --attributes "frontend=react,network=offline"
goalforge standards assess              # 읽기만 합니다
goalforge standards assess --supply     # 미충족 기준을 작업으로 공급합니다
goalforge standards status
goalforge standards except --standard NET-002 --reason "..." --decider hkjang --review-when "..."
```

관측은 **커밋에 고정해서** 합니다. 작업 트리 읽기는 재현되지 않고 아무도 커밋하지 않은 파일까지 포함합니다 — 한 기계에만 존재하는 변경으로 평가가 통과할 수 있습니다.

```
$ goalforge standards assess --supply
커밋 6ff00562669d 기준으로 평가했습니다
공급: WORK-…767397819, WORK-…770377201, WORK-…773334294
자동 검사 없음 24건: CFG-002, CFG-003, CFG-004, NET-001, …

$ goalforge standards status
go-react-offline-service@0.1 — 적용 28건
충족 0 · 미충족 3 · 일부 0 · 미확인 25 · 적용 제외 0

[!]  CFG-001    required 런타임 환경변수 네 종류 제한
               런타임 환경변수 REDIS_URL, SMTP_HOST 를 추가로 읽습니다 — 계약은 네 종류입니다
[!]  NET-002    required 프런트엔드 자산 로컬 제공
               런타임에 외부 자산을 불러옵니다: fonts.googleapis.com (web/index.html)
[?]  CORE-001   required 서버 Go · 웹 React 및 이름 일관성
               정적 읽기로는 판정할 수 없습니다 — 서비스명·로고·제품명이 화면과
               문서에서 일치하는지는 실행해서 확인해야 합니다
```

### 관측기가 할 수 있는 일과 할 수 없는 일

**부재는 증명할 수 있습니다.** 번들 자산 안의 CDN 주소, 계약이 허용한 네 개를 넘는 환경변수, 실행 단계에서 프런트엔드를 빌드하는 Dockerfile — 소스에 보입니다.

**동작은 증명할 수 없습니다.** 라우터에 경로가 있다는 것은 그 화면이 필터를 복원하는지에 대해 아무 말도 하지 않고, 테스트 파일이 있다는 것은 그 테스트가 통과하는지에 대해 아무 말도 하지 않습니다.

그래서 관측기는 부재가 보이면 `UNMET`, 그 밖에는 **무엇이 정산할지를 적은 `UNKNOWN`** 을 냅니다. 실행이 필요한 것에 `MET` 을 내지 않습니다 — 정적 탐지기가 `MET` 이나 `NOT_APPLICABLE` 을 반환하면 그 탐지기에서 실패합니다.

정적 읽기는 **실행 근거를 만들 수 없습니다.** 파일 읽기를 `browser_test_result` 라고 이름 붙이는 탐지기는 다른 모든 검사를 지나갑니다 — 판정기는 근거의 *종류*만 보지 누가 만들었는지 보지 않기 때문입니다.

### 현재 자동 검사가 있는 기준

| 기준 | 증명하는 부재 |
| --- | --- |
| CORE-001 | go.mod 또는 react 의존성 없음 |
| CFG-001 | 계약의 네 변수를 넘는 런타임 환경변수 (테스트 파일 제외) |
| NET-002 | 프런트엔드 자산의 CDN·외부 폰트 참조 |
| REL-001 | Dockerfile 없음, 또는 **마지막 FROM 이후** 단계에서 프런트엔드 빌드 |

빌드 단계에서 Node 를 설치하는 것은 올바른 방식입니다. 그것을 결함으로 보고하면 해결책을 결함이라고 말하는 셈이라, 마지막 `FROM` 이후만 봅니다.

나머지는 `자동 검사 없음` 으로 **빠뜨리지 않고 말합니다.** "봤는데 문제 없음" 과 "아무도 안 봤음" 은 다르고, 뒤를 생략한 보고서는 아무도 다 걷지 않은 카탈로그를 완전한 것으로 읽히게 합니다.

### 공급

`--supply` 를 붙였을 때만 작업이 만들어집니다. 저장소를 읽는 것이 누군가의 보드에 일을 올려서는 안 됩니다.

- **`UNKNOWN` 은 접수하지 않습니다.** 조사는 결함이 아니고, "가서 봐라" 를 수정처럼 보드에 올리면 아무도 잘못됐다고 확인하지 않은 것들이 쌓입니다.
- **순위를 매긴 뒤에 상한을 적용합니다.** 먼저 도는 탐지기가 무엇을 찾았든 한 회차 예산을 다 쓰는 일이 없습니다. 넘친 것은 버리지 않고 보류합니다.
- **미완료 공급 작업이 10건 쌓이면 공급을 멈춥니다.** 아무도 처리하지 않는 보드에 필요한 것은 더 많은 발견이 아니라 있는 것을 닫는 일입니다.
- **같은 결함은 다시 접수되지 않습니다.** 두 번째 평가는 `이미 접수됨: NET-002 → WORK-…` 이라고 말합니다.

카드는 관측한 것과 기준의 의도를 이고 갑니다. 인수 조건은 **기준 자신의 검사**라서, 일을 접수하는 쪽과 판정하는 쪽이 같은 문장을 읽습니다.

```
NET-002: 프런트엔드 자산 로컬 제공          범위 web/**   우선순위 90

런타임에 외부 자산을 불러옵니다: fonts.googleapis.com (web/index.html)

관측: web/index.html 가 fonts.googleapis.com 를 참조합니다

기준: JS·CSS·폰트·아이콘·도움말을 로컬에서 제공하고 런타임 CDN·외부 텔레메트리를 없앤다

--- 인수 ---
- [browser_journey] 브라우저 요청에 미허용 외부 도메인이 없다
- [static] 런타임 CDN·외부 텔레메트리 참조가 없다
근거: commit_sha, browser_test_result
```

---

## 아직 없는 것

1·2단계까지 구현되어 있습니다. 다음은 정기 평가 스케줄과 백로그 보충, 발견 예산, 그리고 승인된 저위험 범위의 자동 구현입니다. 지금은 `standards assess` 를 직접 돌립니다.
