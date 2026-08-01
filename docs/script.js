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
      nav_arch: "아키텍처",
      nav_providers: "지원 프로바이더",
      nav_faq: "자주 묻는 질문",
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

      footer_desc: "GoalForge는 개발자 중심의 통제력과 AI의 생산성을 결합한 목표 중심 오케스트레이터입니다.",
      footer_links_title: "바로가기",
      footer_legal_title: "프로젝트 정보",
      email_contact_text: "메일 문의:"
    },
    en: {
      site_title: "GoalForge | Goal-First AI Development Orchestrator",
      meta_desc: "GoalForge is a goal-first development orchestrator controlling AI sessions (Codex, Claude Code, Qwen, OpenCode). Execute autonomous AI coding with deterministic verification gates and git worktree isolation.",
      nav_features: "Features",
      nav_arch: "Architecture",
      nav_providers: "Providers",
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
