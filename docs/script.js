/**
 * GoalForge Landing Page Interactive Script
 * Supports KO / EN i18n switching, Terminal Tab simulation, FAQ accordion, Copy-to-clipboard, etc.
 */

document.addEventListener('DOMContentLoaded', () => {
  // Translations Dictionary
  const translations = {
    ko: {
      site_title: "GoalForge | 개발자 주도 AI 개발 오케스트레이터",
      meta_desc: "GoalForge는 Codex, Claude Code, Qwen, OpenCode 등 다양한 AI 모델을 통제하는 목표 중심 개발 오케스트레이터입니다. 결정론적 검증 게이트와 워크트리 격리 환경에서 안전한 자동화를 실현하세요.",
      nav_features: "주요 기능",
      nav_comp: "패러다임 비교",
      nav_deepdive: "심층 메커니즘",
      nav_arch: "아키텍처",
      nav_providers: "지원 프로바이더",
      nav_mcp: "MCP & CLI",
      nav_cli: "CLI 가이드",
      nav_roadmap: "로드맵",
      nav_faq: "FAQ",
      badge_hero: "✨ Go 기반 결정론적 AI 개발 오케스트레이터",
      hero_title_1: "AI는 실행 도구일 뿐,",
      hero_title_2: "프로젝트 주도권은 개발자가 쥔다",
      hero_subtitle: "Codex, Claude Code, Qwen, OpenCode를 개발자의 승인과 검증 게이트 아래 통합 통제. 멈추지 않는 목표 달성을 위한 최적의 오케스트레이션 엔진.",
      btn_start: "지금 시작하기",
      btn_github: "GitHub 저장소",
      copy_hint: "복사하려면 클릭하세요",
      copied: "클립보드에 복사되었습니다!",
      
      stat_providers: "4종 AI CLI 통합 지원",
      stat_gates: "100% 결정론적 검증",
      stat_isolation: "0건의 무단 메인 병합",
      stat_mcp: "15개 빌트인 MCP 툴",

      sec_features_tag: "CORE ADVANTAGES",
      sec_features_title: "GoalForge를 선택해야 하는 이유",
      sec_features_desc: "환각과 통제 불능 코딩 에이전트의 시대는 끝났습니다. GoalForge가 개발자 중심 시스템 통제를 재정의합니다.",

      feat_1_title: "목표 중심 오케스트레이션",
      feat_1_desc: "AI 세션이 아닌 Go 프로세스가 버전 관리되는 목표, 마일스톤, 작업 순서 및 검증 증거의 단일 진실 출처(Single Source of Truth)를 소유합니다.",

      feat_2_title: "이종 AI 모델 통합 통제",
      feat_2_desc: "Codex, Claude Code, Qwen Code, OpenCode를 동일한 CLI 규격과 권한 매핑 정책 아래 유연하게 교체하고 실행할 수 있습니다.",

      feat_3_title: "결정론적 자동 검증 게이트",
      feat_3_desc: "`go test`, `go build` 및 사용자 정의 검증을 통과해야만 병합 및 발행 승인 요청을 생성합니다. 환각된 코드의 자동 릴리즈를 차단합니다.",

      feat_4_title: "Git Worktree 안전격리",
      feat_4_desc: "AI 실행은 기본 브랜치가 아닌 격리된 Git 워크트리에서 일어납니다. 실패 시 메인 브랜치 오염 없이 즉시 롤백 및 작업 재시도가 가능합니다.",

      feat_5_title: "빌트인 MCP 서버 지원",
      feat_5_desc: "stdio 및 Streamable HTTP(POST /mcp)를 통해 Claude Code 및 AI 에디터와 대화형으로 연동되는 15가지 관리 및 제어 도구를 제공합니다.",

      feat_6_title: "예산 및 API 쿼터 자동 관리",
      feat_6_desc: "프로젝트 토큰/비용 예산과 프로바이더 쿼터를 독립 평가합니다. Quota 초과 시 자동 대기(WAITING_QUOTA) 및 복구 체크포인트를 기록합니다.",

      sec_comp_tag: "PARADIGM SHIFT",
      sec_comp_title: "기존 AI 코딩 도구 vs GoalForge 오케스트레이터",
      sec_comp_desc: "자유도라는 이름 아래 발생하던 리스크를 완벽하게 통제된 엔지니어링 프로세스로 전환하세요.",

      comp_raw_badge: "TRADITIONAL AI AGENTS",
      comp_raw_title: "기존 프롬프트 기반 코딩 에이전트",
      comp_raw_1: "메인 브랜치 직연결 수정으로 코드베이스 오염 리스크",
      comp_raw_2: "검증 없는 환각 커밋 및 반복 실패 시 무한 루프 비용 발생",
      comp_raw_3: "API Quota 초과 시 무한 폴링으로 계정 잠금 또는 오류 종료",
      comp_raw_4: ".env, 개인키 등 보안 파일 무단 변경을 사전 차단할 메커니즘 부재",

      comp_gf_badge: "GOALFORGE ORCHESTRATOR",
      comp_gf_title: "GoalForge 통제형 오케스트레이터",
      comp_gf_1: "격리된 Git 워크트리에서 일어나는 단일 임대 실행으로 메인 보호",
      comp_gf_2: "go test, 빌드 게이트 100% 성공 시에만 승인 및 커밋 트레일러 기록",
      comp_gf_3: "Quota 초과 시 무한 폴링 대신 체크포인트 보관 후 WAITING_QUOTA 자동 대기",
      comp_gf_4: "SHA-256 핑거프린트 검증으로 비승인 보안 파일 변경 시 자동 롤백",

      sec_mcp_tag: "MODEL CONTEXT PROTOCOL",
      sec_mcp_title: "15가지 빌트인 MCP 서버 제어 도구",
      sec_mcp_desc: "Claude Code, Cursor, AI 에디터와 대화형으로 연동되어 오케스트레이터를 실시간 통제합니다.",

      mcp_tool_1: "프로젝트 상태, 가중 마일스톤 완료율 및 검증 게이트 통과율 실시간 리포팅",
      mcp_tool_2: "우선순위(0~100), 가치 점수 및 작업 상태(APPROVED/BLOCKED) 대화형 관리",
      mcp_tool_3: "CLI 테스트 명령어를 결정론적 검증 게이트로 대화 중 즉시 등록",
      mcp_tool_4: "Protected-files, merge-branch, publish-branch에 대한 개발자 승인 처리",
      mcp_tool_5: "프로바이더별 토큰 소비량, 비용 ledger 및 Quota 리셋 시각 정밀 조회",
      mcp_tool_6: "자율 백그라운드 워커 작업 인큐ing 및 과거 실행 세션 타임라인 재생",

      sec_deepdive_tag: "DEEP DIVE & SECURITY",
      sec_deepdive_title: "핵심 보안 및 제어 메커니즘",
      sec_deepdive_desc: "단순한 에이전트 호출을 넘어 코드베이스 오염 차단과 결정론적 안정을 보장하는 4대 차별화 기술",

      deep_1_title: "보호 파일 핑거프린팅 & 단일 임대",
      deep_1_desc: "`.env`, SSH 키, 보안 세팅 파일의 SHA-256 해시 기준선을 사전 기록합니다. AI의 무단 변경/삭제 시 변경사항을 즉시 롤백하고 실행을 차단합니다.",

      deep_2_title: "무한 루프 차단 & 중복 필터링",
      deep_2_desc: "`same_work`, `same_change`, `no_change`, `same_error` 신호를 실시간 추적합니다. 동일 오류 반복 시 자동으로 세션을 교체하거나 리뷰용으로 멈춥니다.",

      deep_3_title: "Quota 회복 및 체크포인트 복구",
      deep_3_desc: "API Rate Limit 초과 시 무한 폴링 대신 `continuity/<project>.md` 체크포인트를 남기고 `WAITING_QUOTA` 대기 후 리셋 시각에 자동 복구됩니다.",

      deep_4_title: "AES-GCM 암호화 감사 트레일",
      deep_4_desc: "`GOALFORGE_AUDIT_KEY` 설정 시 모든 AI 전송 프롬프트 원본을 AES 암호화하여 저장하며, Slack/웹훅 알림 시 민감 데이터를 자동 Redact 처리합니다.",

      sec_arch_tag: "SYSTEM ARCHITECTURE",
      sec_arch_title: "통제된 실행 파이프라인",
      sec_arch_desc: "개발자의 승인과 검증 정책을 통과하는 완벽한 4단계 라이프사이클",

      arch_step_1_title: "1. 목표 및 작업 등록",
      arch_step_1_sub: "개발자가 Goal, Gate, Work Scope 지정",

      arch_step_2_title: "2. 격리된 워크트리 세션",
      arch_step_2_sub: "Codex / Claude / Qwen / OpenCode 실행",

      arch_step_3_title: "3. 검증 게이트 자동 수행",
      arch_step_3_sub: "테스트, 빌드, 보안 검사 검증 완료",

      arch_step_4_title: "4. 승인 요청 & 최종 병합",
      arch_step_4_sub: "Goal-ID 트레일러 기록 후 메인 병합",

      sec_prov_tag: "COMPATIBILITY",
      sec_prov_title: "지원 AI 프로바이더 매핑",
      sec_prov_desc: "각 AI 에이전트 CLI의 최적 바이너리 트랜스포트와 권한 매핑을 지원합니다.",

      th_provider: "프로바이더",
      th_binary: "CLI 바이너리",
      th_transport: "트랜스포트",
      th_readonly: "읽기 전용 매핑",
      th_writable: "쓰기 권한 매핑",
      th_resume: "세션 복구",

      sec_cli_tag: "CLI COMMANDS & WORKFLOW",
      sec_cli_title: "실전 개발자 명령어 가이드",
      sec_cli_desc: "GoalForge CLI를 통한 초기화부터 검증 게이트 등록, 자율 실행 및 안전 병합까지",

      cli_1_title: "1. 진단 및 프로젝트 초기화",
      cli_2_title: "2. 목표 및 검증 게이트 설정",
      cli_3_title: "3. 자율 작업 Discovery & 실행",
      cli_4_title: "4. 승인, 병합 & 안전 롤백",

      sec_roadmap_tag: "DEVELOPMENT ROADMAP",
      sec_roadmap_title: "GoalForge 개발 로드맵 (단기 및 중기 계획)",
      sec_roadmap_desc: "개발자 주도 오케스트레이션 혁신을 향한 단계별 마일스톤과 차세대 확장 로드맵",

      roadmap_short_tag: "단기 계획 (Short-Term Plan) — 2026 Q3~Q4",
      roadmap_short_title: "분산 스토리지 완성 & 관측 가능성 강화",
      roadmap_short_desc: "멀티 워커 스케일아웃 지원과 대규모 프로덕션 환경에서의 실시간 시각화 기반 마련",

      m_short_1_t: "PostgreSQL 분산 스토리지 전면 이중화",
      m_short_1_d: "SQLite 기반 단일 스토리지에서 PostgreSQL SKIP LOCKED 기반 고성능 분산 작업 큐 및 마이그레이션 도구 완결.",
      m_short_2_t: "웹 UI 대시보드 & 실시간 SSE 모니터링",
      m_short_2_d: "활성 세션 추적, 에이전트 실행 플레임그래프(Flamegraph), 토큰 비용 분석 및 Prometheus /metrics 실시간 대시보드 구축.",
      m_short_3_t: "Gemini CLI / DeepSeek / Ollama 어댑터 추가",
      m_short_3_d: "Google Gemini CLI, DeepSeek Coder, 로컬 Ollama/vLLM 지원으로 프로바이더 선택의 폭 대폭 확장.",
      m_short_4_t: "GitHub Actions & CI/CD 네이티브 플러그인",
      m_short_4_d: "CI/CD 파이프라인 상에서 GoalForge의 결정론적 검증 게이트를 원클릭으로 실행하는 공식 GitHub Action 릴리즈.",

      roadmap_mid_tag: "중기 계획 (Mid-Term Plan) — 2027 H1~H2",
      roadmap_mid_title: "멀티 에이전트 자율 협업 & 엔터프라이즈 플랫폼",
      roadmap_mid_desc: "특화된 AI 에이전트 간의 자율 스웜(Swarm) 협업과 엔터프라이즈 레벨 보안/SaaS 관리 체계 완성",

      m_mid_1_t: "멀티 에이전트 자율 스웜 (Multi-Agent Swarm)",
      m_mid_1_d: "구현 에이전트, QA 테스트 에이전트, 보안 검사 에이전트가 격리된 워크트리에서 합의 정책 아래 병렬 협업하는 엔진 구현.",
      m_mid_2_t: "자가 치유 검증 게이트 (Self-Healing Gate)",
      m_mid_2_d: "검증 게이트(go test, 빌드) 실패 시 실패 지점을 정밀 진단하여 1차 자가 코드 수정을 수행하는 자동 복구 정책 탑재.",
      m_mid_3_t: "엔터프라이즈 보안 & 컴플라이언스 팩",
      m_mid_3_d: "RBAC 권한 제어, SAML/SSO 연동, 폐쇄망(Air-gapped) LLM 매핑 및 SOC2/ISO 감사 내보내기 스펙 확충.",
      m_mid_4_t: "GoalForge Cloud SaaS 관리형 플랫폼",
      m_mid_4_d: "기업/팀 단위 토큰 예산 풀링, 중앙ized 승인 관리 콘솔 및 관리형 인프라 제공 서비스 런칭.",

      sec_faq_tag: "FAQ & AEO",
      sec_faq_title: "자주 묻는 질문",
      sec_faq_desc: "GoalForge의 핵심 개념 및 동작 방식에 대한 완벽한 가이드",

      faq_1_q: "Q1. GoalForge란 무엇인가요?",
      faq_1_a: "GoalForge는 AI 코딩 에이전트(Codex, Claude Code, Qwen Code, OpenCode)를 통합 관리하는 목표 중심(Goal-First) 개발 오케스트레이터입니다. AI 세션에 프로젝트 상태 관리를 의존하는 대신, Go 기반 고성능 프로세스가 프로젝트 목표, 마일스톤, 검증 게이트, 토큰 예산을 결정론적으로 관리합니다.",

      faq_2_q: "Q2. 기존 AI 코딩 에이전트(Claude Code, Cursor 등)와 무엇이 다른가요?",
      faq_2_a: "기존 에이전트는 프롬프트 기반으로 무제한 자유도를 가집니다. 이로 인해 메인 브랜치 훼손, 검증되지 않은 코드 커밋, 환각 현상 등의 리스크가 있습니다. GoalForge는 AI를 단순 '실행 도구'로 격리하고, Go 프로세스가 검증 게이트(`go test`, 빌드 등)를 통과한 결과만 승인 절차를 거쳐 반영하도록 통제합니다.",

      faq_3_q: "Q3. 검증 게이트(Verification Gate)는 어떻게 동작하나요?",
      faq_3_a: "GoalForge CLI로 `goalforge verify gate add --type build_passed --command-json '[\"go\",\"test\",\"./...\"]'`과 같이 등록합니다. AI 에이전트가 작업을 수행한 후, 해당 검증 게이트가 100% 성공을 반환해야만 작업이 완료(DONE)로 승인되며 Git 커밋 트레일러가 작성됩니다.",

      faq_4_q: "Q4. GoalForge MCP 서버 기능은 어떻게 활용하나요?",
      faq_4_a: "GoalForge는 `goalforge mcp` 명령어로 stdio 또는 Streamable HTTP(POST /mcp) 서버를 시작할 수 있습니다. 15가지 MCP 도구를 통해 Claude Code, Cursor 등 외부 MCP 클라이언트에서 프로젝트 상태 조회, 목표 및 작업 수정, 쿼터 리포트 확인, 승인 요청 등을 대화형으로 처리할 수 있습니다.",

      faq_5_q: "Q5. API 쿼터나 비용 한도 초과 시 어떻게 처리되나요?",
      faq_5_a: "GoalForge는 프롬프트 호출 전 프로젝트 비용 ledger와 프로바이더 Quota 리셋 시각을 사전 평가합니다. Quota 부족 시 무한 폴링 대신 Git/세션 체크포인트를 저장하고 `WAITING_QUOTA` 상태로 대기하며, Quota 리셋 후 `worker`가 안전하게 작업을 복구합니다.",

      faq_6_q: "Q6. GoalForge의 단기 및 중기 로드맵 계획은 무엇인가요?",
      faq_6_a: "GoalForge는 단기적으로(2026 Q3~Q4) PostgreSQL 분산 스토리지 완결, 웹 UI 모니터링 대시보드, Gemini/DeepSeek/Ollama 어댑터 추가 및 GitHub Actions 플러그인을 제공하며, 중기적으로(2027 H1~H2) 멀티 에이전트 자율 스웜 오케스트레이션, 자가 치유 검증 게이트, 엔터프라이즈 보안 팩 및 Managed Cloud SaaS 서비스를 런칭할 계획입니다.",

      faq_7_q: "Q7. 소스코드 및 보안 민감 파일(보호 파일)은 어떻게 안전하게 통제되나요?",
      faq_7_a: "GoalForge는 execution 시작 전 `.env`, SSH 키, 개인키 등 프로젝트 내 모든 보호 파일의 SHA-256 해시 핑거프린트를 생성합니다. AI 실행 중 비승인된 변경/삭제가 발생하면 해당 run을 즉시 취소하고 원래 상태로 롤백하며, `GOALFORGE_AUDIT_KEY`를 통해 원본 프롬프트 또한 AES-GCM으로 암호화 보관합니다.",

      footer_desc: "GoalForge는 개발자 중심의 통제력과 AI의 생산성을 결합한 목표 중심 오케스트레이터입니다.",
      footer_links_title: "바로가기",
      footer_legal_title: "프로젝트 정보",
      email_contact_text: "메일 문의:"
    },
    en: {
      site_title: "GoalForge | Goal-First AI Development Orchestrator",
      meta_desc: "GoalForge is a goal-first development orchestrator controlling AI sessions (Codex, Claude Code, Qwen, OpenCode). Execute autonomous AI coding with deterministic verification gates and git worktree isolation.",
      nav_features: "Features",
      nav_comp: "Paradigm Shift",
      nav_deepdive: "Deep Dive",
      nav_arch: "Architecture",
      nav_providers: "Providers",
      nav_mcp: "MCP & CLI",
      nav_cli: "CLI Guide",
      nav_roadmap: "Roadmap",
      nav_faq: "FAQ",
      badge_hero: "✨ Go-based Deterministic AI Development Orchestrator",
      hero_title_1: "AI is an Execution Tool.",
      hero_title_2: "You Control the Engine.",
      hero_subtitle: "Unified orchestration of Codex, Claude Code, Qwen, and OpenCode under strict developer approval and deterministic verification gates.",
      btn_start: "Get Started",
      btn_github: "GitHub Repository",
      copy_hint: "Click to copy",
      copied: "Copied to clipboard!",

      stat_providers: "4 AI CLI Providers",
      stat_gates: "100% Deterministic Gates",
      stat_isolation: "0 Unapproved Main Merges",
      stat_mcp: "15 Built-in MCP Tools",

      sec_features_tag: "CORE ADVANTAGES",
      sec_features_title: "Why Choose GoalForge?",
      sec_features_desc: "The era of unconstrained hallucinating coding agents is over. GoalForge redefines developer-first system control.",

      feat_1_title: "Goal-First Orchestration",
      feat_1_desc: "The Go process owns project state, versioned goals, work ordering, and verification evidence as the Single Source of Truth—AI sessions are execution tools.",

      feat_2_title: "Multi-Provider AI Control",
      feat_2_desc: "Swap and execute Codex, Claude Code, Qwen Code, and OpenCode under identical CLI contracts and permission mapping policies.",

      feat_3_title: "Deterministic Verification Gates",
      feat_3_desc: "Automated `go test`, `go build`, and custom verification gates must pass 100% before merge and publish approvals are issued.",

      feat_4_title: "Git Worktree Isolation",
      feat_4_desc: "AI execution happens in isolated Git worktrees, never on the default main branch. Failed runs rollback instantly without contaminating main.",

      feat_5_title: "Built-in MCP Server",
      feat_5_desc: "15 management tools accessible via stdio or Streamable HTTP (POST /mcp) for conversational integration with Claude Code and AI editors.",

      feat_6_title: "Quota & Budget Management",
      feat_6_desc: "Independent evaluation of project budgets and provider quotas. Automatically saves recovery checkpoints and enters WAITING_QUOTA upon rate limits.",

      sec_comp_tag: "PARADIGM SHIFT",
      sec_comp_title: "Traditional AI Coding Agents vs GoalForge Orchestrator",
      sec_comp_desc: "Transform unconstrained risk into a fully deterministic engineering process governed by developer policy.",

      comp_raw_badge: "TRADITIONAL AI AGENTS",
      comp_raw_title: "Raw Prompt-Driven Coding Agents",
      comp_raw_1: "Direct main-branch edits risk unverified codebase corruption",
      comp_raw_2: "Unverified hallucinated commits & endless retry loops inflate costs",
      comp_raw_3: "API Rate limits trigger polling lockouts or unhandled crashes",
      comp_raw_4: "No built-in baseline check to stop unauthorized .env or key edits",

      comp_gf_badge: "GOALFORGE ORCHESTRATOR",
      comp_gf_title: "GoalForge Controlled Orchestrator",
      comp_gf_1: "Single-writer leases in isolated Git worktrees protect default branch",
      comp_gf_2: "Go test & build gates must pass 100% before merge approvals",
      comp_gf_3: "Saves checkpoints and enters WAITING_QUOTA on rate limits",
      comp_gf_4: "SHA-256 fingerprinting rolls back unapproved protected file edits",

      sec_mcp_tag: "MODEL CONTEXT PROTOCOL",
      sec_mcp_title: "15 Built-in MCP Management Tools",
      sec_mcp_desc: "Interact conversationally with Claude Code, Cursor, and AI editors via stdio or Streamable HTTP.",

      mcp_tool_1: "Real-time project status, weighted milestone progress & gate pass rates",
      mcp_tool_2: "Interactive backlog priority (0-100), value scoring & state transitions",
      mcp_tool_3: "Register test commands as deterministic verification gates on the fly",
      mcp_tool_4: "Issue approvals for protected-files, merge-branch, and publish-branch",
      mcp_tool_5: "Inspect token usage ledgers, cost tracking & provider quota reset times",
      mcp_tool_6: "Enqueue autonomous background tasks & replay historical run timelines",

      sec_deepdive_tag: "DEEP DIVE & SECURITY",
      sec_deepdive_title: "Core Security & Control Mechanisms",
      sec_deepdive_desc: "Four key technologies ensuring code safety and deterministic stability beyond raw agent calls",

      deep_1_title: "Protected File Fingerprinting & Lease",
      deep_1_desc: "Baseline SHA-256 hashes of `.env` and SSH keys are verified before runs. Unapproved changes trigger instant rollback.",

      deep_2_title: "Loop Protection & Deduplication",
      deep_2_desc: "Monitors loop signals like `same_error` and `no_change`, rotating sessions or pausing for developer review.",

      deep_3_title: "Quota Resilience & Checkpoints",
      deep_3_desc: "Saves `continuity/<project>.md` checkpoints on rate limit, entering `WAITING_QUOTA` until reset.",

      deep_4_title: "AES-GCM Encrypted Audit Trail",
      deep_4_desc: "Encrypted prompt log storage with AES-GCM and automatic secret redaction on webhook alerts.",

      sec_arch_tag: "SYSTEM ARCHITECTURE",
      sec_arch_title: "Controlled Execution Pipeline",
      sec_arch_desc: "A rigorous 4-step lifecycle governed by developer policies and verification gates",

      arch_step_1_title: "1. Goal & Work Definition",
      arch_step_1_sub: "Developer sets Goal, Gates, and Work Scope",

      arch_step_2_title: "2. Isolated Worktree Run",
      arch_step_2_sub: "Codex / Claude / Qwen / OpenCode execution",

      arch_step_3_title: "3. Gate Verification",
      arch_step_3_sub: "Automated test, build, and policy checks",

      arch_step_4_title: "4. Approval & Merge",
      arch_step_4_sub: "Git trailers created & merged to main branch",

      sec_prov_tag: "COMPATIBILITY",
      sec_prov_title: "Supported Provider Mappings",
      sec_prov_desc: "Native integration with optimal transport protocols and permission mappings for each AI CLI.",

      th_provider: "Provider",
      th_binary: "CLI Binary",
      th_transport: "Transport",
      th_readonly: "Read-only Mapping",
      th_writable: "Writable Mapping",
      th_resume: "Session Resume",

      sec_cli_tag: "CLI COMMANDS & WORKFLOW",
      sec_cli_title: "Developer CLI Command Reference",
      sec_cli_desc: "Complete 4-step workflow from setup and gate registration to autonomous execution and merging",

      cli_1_title: "1. Diagnostics & Init",
      cli_2_title: "2. Goal & Gate Setup",
      cli_3_title: "3. Discovery & Autonomous Run",
      cli_4_title: "4. Approval, Merge & Rollback",

      sec_roadmap_tag: "DEVELOPMENT ROADMAP",
      sec_roadmap_title: "GoalForge Development Roadmap (Short & Mid-Term Plans)",
      sec_roadmap_desc: "Step-by-step milestones and next-generation architecture expansion plans",

      roadmap_short_tag: "Short-Term Plan — 2026 Q3~Q4",
      roadmap_short_title: "Distributed Storage & Observability",
      roadmap_short_desc: "Enabling multi-worker scale-out and real-time observability for large-scale production",

      m_short_1_t: "Full PostgreSQL Distributed Storage",
      m_short_1_d: "Transitioning from SQLite to PostgreSQL SKIP LOCKED distributed queues and lease recovery.",
      m_short_2_t: "Web UI Dashboard & Real-time SSE",
      m_short_2_d: "Live session tracking, execution flamegraphs, token cost breakdown, and Prometheus metrics UI.",
      m_short_3_t: "Gemini CLI, DeepSeek & Ollama Adapters",
      m_short_3_d: "Native CLI integration for Gemini, DeepSeek, and local LLMs (Ollama/vLLM).",
      m_short_4_t: "GitHub Actions & CI/CD Native Plugin",
      m_short_4_d: "Official GitHub Action to trigger GoalForge verification gates natively in CI/CD.",

      roadmap_mid_tag: "Mid-Term Plan — 2027 H1~H2",
      roadmap_mid_title: "Multi-Agent Swarm & Enterprise SaaS",
      roadmap_mid_desc: "Autonomous multi-agent collaboration with enterprise security and cloud SaaS management",

      m_mid_1_t: "Multi-Agent Swarm Orchestration",
      m_mid_1_d: "Specialized agents (Dev, QA, Security) collaborating concurrently in isolated worktrees.",
      m_mid_2_t: "Self-Healing Verification & Auto-Repair",
      m_mid_2_d: "Automated root-cause analysis and auto-repair attempts upon verification gate failures.",
      m_mid_3_t: "Enterprise Security & Compliance Suite",
      m_mid_3_d: "RBAC, SAML/SSO, air-gapped LLM proxy support, and SOC2/ISO audit log exports.",
      m_mid_4_t: "GoalForge Cloud SaaS Managed Platform",
      m_mid_4_d: "Managed SaaS solution for team token pooling, centralized approval dashboard, and enterprise controls.",

      sec_faq_tag: "FAQ & AEO",
      sec_faq_title: "Frequently Asked Questions",
      sec_faq_desc: "Comprehensive answers to key questions about GoalForge's architecture and capabilities",

      faq_1_q: "Q1. What is GoalForge?",
      faq_1_a: "GoalForge is a goal-first development orchestrator designed to manage AI coding CLI tools (Codex, Claude Code, Qwen Code, OpenCode). Rather than letting AI sessions maintain state, a Go process deterministically owns project goals, work items, verification gates, and token budgets.",

      faq_2_q: "Q2. How is GoalForge different from raw AI coding agents?",
      faq_2_a: "Standard AI agents operate with unconstrained freedom, which risks main branch corruption, unverified code commits, and hallucinations. GoalForge treats AI as an isolated execution tool: code changes are verified by automated gates (`go test`, build checks) and require explicit developer approval before merging.",

      faq_3_q: "Q3. How do Verification Gates work?",
      faq_3_a: "Gates are registered via CLI, e.g., `goalforge verify gate add --type build_passed --command-json '[\"go\",\"test\",\"./...\"]'`. When an AI finishes a work item, GoalForge executes the gate. Only if it returns 100% success is the work marked APPROVED/DONE and signed with Git trailers.",

      faq_4_q: "Q4. How do I use the GoalForge MCP Server?",
      faq_4_a: "Run `goalforge mcp` (stdio) or `goalforge mcp --addr 127.0.0.1:8799` (HTTP). 15 MCP tools allow AI clients like Claude Code or editors to query project status, edit backlogs, request approvals, and inspect quota usage conversationally.",

      faq_5_q: "Q5. How does GoalForge handle API Quota limits or budget caps?",
      faq_5_a: "Before invoking AI models, GoalForge checks budget ledgers and provider quota reset timestamps. If quota is exhausted, GoalForge saves a recovery checkpoint, transitions to `WAITING_QUOTA`, and resumes automatically when quota resets.",

      faq_6_q: "Q6. What are GoalForge's short-term and mid-term roadmap plans?",
      faq_6_a: "GoalForge's short-term plan (2026 Q3-Q4) focuses on PostgreSQL distributed storage completion, Web SSE dashboard, Gemini/DeepSeek/Ollama adapters, and GitHub Actions plugins. The mid-term plan (2027 H1-H2) introduces Multi-Agent Swarm orchestration, self-healing verification gates, Enterprise Compliance Suite, and GoalForge Cloud SaaS.",

      faq_7_q: "Q7. How are sensitive files and source code protected?",
      faq_7_a: "Before writable AI runs, GoalForge records SHA-256 baseline hashes of protected files (.env, keys). Any unapproved mutation immediately halts the run and restores the baseline. Additionally, prompt logs can be encrypted with AES-GCM using GOALFORGE_AUDIT_KEY.",

      footer_desc: "GoalForge combines developer-centric system control with high-output AI productivity.",
      footer_links_title: "Quick Links",
      footer_legal_title: "Project Info",
      email_contact_text: "Email Inquiry:"
    }
  };

  let currentLang = 'ko';

  // Language Switcher Handler
  const langBtns = document.querySelectorAll('.lang-btn');
  langBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      const targetLang = btn.dataset.lang;
      if (targetLang && targetLang !== currentLang) {
        currentLang = targetLang;
        updateLanguage(currentLang);
      }
    });
  });

  function updateLanguage(lang) {
    // Update active button state
    langBtns.forEach(b => {
      b.classList.toggle('active', b.dataset.lang === lang);
    });

    // Update document lang attribute
    document.documentElement.lang = lang;

    // Update all data-i18n elements
    const i18nElements = document.querySelectorAll('[data-i18n]');
    i18nElements.forEach(el => {
      const key = el.dataset.i18n;
      if (translations[lang] && translations[lang][key]) {
        el.textContent = translations[lang][key];
      }
    });

    // Update page title & meta description
    if (translations[lang].site_title) {
      document.title = translations[lang].site_title;
    }
    const metaDesc = document.querySelector('meta[name="description"]');
    if (metaDesc && translations[lang].meta_desc) {
      metaDesc.setAttribute('content', translations[lang].meta_desc);
    }

    // Refresh active terminal tab content for selected language
    updateTerminalContent(activeTab);
  }

  // Terminal Simulator Tabs Data
  const terminalTabsData = {
    init: {
      ko: `<span class="term-comment"># 1. 환경 진단 및 프로젝트 초기화</span>
<span class="term-cmd">$ goalforge doctor</span>
<span class="term-success">[OK] Git 2.42.0 detected</span>
<span class="term-success">[OK] Claude CLI (claude-code 1.2.0) verified</span>
<span class="term-success">[OK] Codex CLI (codex v0.8) verified</span>
<span class="term-info">[INF] All authentication and permissions passed</span>

<span class="term-cmd">$ goalforge project init --name demo --provider claude --model haiku \\
    --worktrees --auto-commit --fallback-model sonnet</span>
<span class="term-success">[OK] Initialized GoalForge project in .goalforge/goalforge.db</span>
<span class="term-info">[INF] Worktree isolation enabled, auto-commit trailer active</span>`,
      en: `<span class="term-comment"># 1. Environment diagnostics & project init</span>
<span class="term-cmd">$ goalforge doctor</span>
<span class="term-success">[OK] Git 2.42.0 detected</span>
<span class="term-success">[OK] Claude CLI (claude-code 1.2.0) verified</span>
<span class="term-success">[OK] Codex CLI (codex v0.8) verified</span>
<span class="term-info">[INF] All authentication and permissions passed</span>

<span class="term-cmd">$ goalforge project init --name demo --provider claude --model haiku \\
    --worktrees --auto-commit --fallback-model sonnet</span>
<span class="term-success">[OK] Initialized GoalForge project in .goalforge/goalforge.db</span>
<span class="term-info">[INF] Worktree isolation enabled, auto-commit trailer active</span>`
    },
    goal: {
      ko: `<span class="term-comment"># 2. 버전 관리되는 목표 및 검증 게이트 등록</span>
<span class="term-cmd">$ goalforge goal set --title "Ship Session Store" \\
    --objective "Implement Redis-backed session manager with unit tests" \\
    --criterion build_passed=true</span>
<span class="term-success">[OK] Created Goal V1 (ID: GOAL-01)</span>

<span class="term-cmd">$ goalforge verify gate add --type build_passed \\
    --command-json '["go","test","./internal/session/..."]'</span>
<span class="term-success">[OK] Verification gate registered: ["go", "test", "./internal/session/..."]</span>

<span class="term-cmd">$ goalforge work add --title "Add Redis Client" --priority 90 --scope "internal/session/**"</span>
<span class="term-success">[OK] Work item WORK-101 created (Priority: 90)</span>`,
      en: `<span class="term-comment"># 2. Set versioned goals & register verification gates</span>
<span class="term-cmd">$ goalforge goal set --title "Ship Session Store" \\
    --objective "Implement Redis-backed session manager with unit tests" \\
    --criterion build_passed=true</span>
<span class="term-success">[OK] Created Goal V1 (ID: GOAL-01)</span>

<span class="term-cmd">$ goalforge verify gate add --type build_passed \\
    --command-json '["go","test","./internal/session/..."]'</span>
<span class="term-success">[OK] Verification gate registered: ["go", "test", "./internal/session/..."]</span>

<span class="term-cmd">$ goalforge work add --title "Add Redis Client" --priority 90 --scope "internal/session/**"</span>
<span class="term-success">[OK] Work item WORK-101 created (Priority: 90)</span>`
    },
    run: {
      ko: `<span class="term-comment"># 3. 자율 실행 및 게이트 자동 검증</span>
<span class="term-cmd">$ goalforge continue</span>
<span class="term-info">[INF] Selected work item: WORK-101 (Add Redis Client)</span>
<span class="term-info">[INF] Spawning isolated worktree: .goalforge/worktrees/WORK-101</span>
<span class="term-info">[INF] Provider claude (haiku) executing scope internal/session/**</span>
<span class="term-warning">[RUN] Running verification gate: go test ./internal/session/...</span>
<span class="term-success">[PASS] Gate build_passed (100% success rate)</span>
<span class="term-success">[OK] Committed in worktree: 4f8a91b (Goal-ID: GOAL-01, Work-Item-ID: WORK-101)</span>
<span class="term-info">[INF] Status: WAITING_MERGE_APPROVAL</span>`,
      en: `<span class="term-comment"># 3. Autonomous execution & gate verification</span>
<span class="term-cmd">$ goalforge continue</span>
<span class="term-info">[INF] Selected work item: WORK-101 (Add Redis Client)</span>
<span class="term-info">[INF] Spawning isolated worktree: .goalforge/worktrees/WORK-101</span>
<span class="term-info">[INF] Provider claude (haiku) executing scope internal/session/**</span>
<span class="term-warning">[RUN] Running verification gate: go test ./internal/session/...</span>
<span class="term-success">[PASS] Gate build_passed (100% success rate)</span>
<span class="term-success">[OK] Committed in worktree: 4f8a91b (Goal-ID: GOAL-01, Work-Item-ID: WORK-101)</span>
<span class="term-info">[INF] Status: WAITING_MERGE_APPROVAL</span>`
    },
    mcp: {
      ko: `<span class="term-comment"># 4. MCP 서버 실행 (Streamable HTTP / stdio)</span>
<span class="term-cmd">$ goalforge mcp --addr 127.0.0.1:8799 --token secret-token-123</span>
<span class="term-success">[OK] GoalForge MCP Server running at http://127.0.0.1:8799/mcp</span>
<span class="term-info">[INF] Registered 15 MCP management tools:</span>
<span class="term-info">  • status, goal_show, backlog_list, verify_gate_add, approval_request</span>
<span class="term-info">  • usage_report, checkpoint_create, continue_enqueue, replay_run</span>
<span class="term-success">[READY] Claude Code / AI Editor connected via Model Context Protocol</span>`,
      en: `<span class="term-comment"># 4. Run MCP Server (Streamable HTTP / stdio)</span>
<span class="term-cmd">$ goalforge mcp --addr 127.0.0.1:8799 --token secret-token-123</span>
<span class="term-success">[OK] GoalForge MCP Server running at http://127.0.0.1:8799/mcp</span>
<span class="term-info">[INF] Registered 15 MCP management tools:</span>
<span class="term-info">  • status, goal_show, backlog_list, verify_gate_add, approval_request</span>
<span class="term-info">  • usage_report, checkpoint_create, continue_enqueue, replay_run</span>
<span class="term-success">[READY] Claude Code / AI Editor connected via Model Context Protocol</span>`
    }
  };

  let activeTab = 'init';
  const termBody = document.getElementById('terminal-body');
  const tabBtns = document.querySelectorAll('.tab-btn');

  tabBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      const tab = btn.dataset.tab;
      if (tab && terminalTabsData[tab]) {
        activeTab = tab;
        tabBtns.forEach(b => b.classList.toggle('active', b === btn));
        updateTerminalContent(activeTab);
      }
    });
  });

  function updateTerminalContent(tab) {
    if (termBody && terminalTabsData[tab]) {
      termBody.innerHTML = terminalTabsData[tab][currentLang] || terminalTabsData[tab]['en'];
    }
  }

  // FAQ Accordion Handler
  const faqItems = document.querySelectorAll('.faq-item');
  faqItems.forEach(item => {
    const questionBtn = item.querySelector('.faq-question');
    if (questionBtn) {
      questionBtn.addEventListener('click', () => {
        const isActive = item.classList.contains('active');
        // Close all items
        faqItems.forEach(i => i.classList.remove('active'));
        // If wasn't active, open clicked one
        if (!isActive) {
          item.classList.add('active');
        }
      });
    }
  });

  // Copy to Clipboard & Toast Handler
  const copyBtns = document.querySelectorAll('.js-copy-code');
  const toast = document.getElementById('toast');

  copyBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      const textToCopy = btn.dataset.copyText || 'go install github.com/hkjang/goalforge/cmd/goalforge@latest';
      navigator.clipboard.writeText(textToCopy).then(() => {
        showToast(translations[currentLang].copied || 'Copied to clipboard!');
      }).catch(err => {
        console.error('Copy failed', err);
      });
    });
  });

  function showToast(msg) {
    if (!toast) return;
    toast.textContent = `✓ ${msg}`;
    toast.classList.add('show');
    setTimeout(() => {
      toast.classList.remove('show');
    }, 2500);
  }
});


  // Mobile Menu Toggle
  const mobileBtn = document.getElementById('mobile-menu-btn');
  const navLinks = document.querySelector('.nav-links') || document.querySelector('.nav-menu') || document.querySelector('.nav-list');
  if (mobileBtn && navLinks) {
    mobileBtn.addEventListener('click', () => {
      navLinks.classList.toggle('active');
    });
    navLinks.querySelectorAll('a').forEach(link => {
      link.addEventListener('click', () => navLinks.classList.remove('active'));
    });
  }
