package api

const dashboardHTML = `<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>GoalForge</title><style>
:root{color-scheme:dark;--bg:#0b1020;--panel:#151d33;--card:#0d1427;--line:#2b385b;--text:#e7ecf7;--muted:#9daaca;--ok:#4ade80;--warn:#fbbf24;--bad:#fb7185;--accent:#38bdf8}
*{box-sizing:border-box}body{margin:0;background:radial-gradient(circle at top,#17213d,var(--bg) 48%);color:var(--text);font:15px/1.5 system-ui,sans-serif}
main{max-width:1120px;margin:auto;padding:32px 22px}header{display:flex;justify-content:space-between;align-items:end;margin-bottom:22px;gap:12px}h1{font-size:26px;margin:0}h1 a{color:inherit;text-decoration:none}.sub{color:var(--muted)}
#projects{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:16px}
.card{background:color-mix(in srgb,var(--panel) 92%,transparent);border:1px solid var(--line);border-radius:16px;padding:18px;box-shadow:0 18px 50px #0004}
a.card{display:block;color:inherit;text-decoration:none;cursor:pointer}a.card:hover{border-color:var(--accent)}
.row{display:flex;justify-content:space-between;gap:12px;align-items:center}
.state{font-size:12px;padding:4px 10px;border:1px solid var(--line);border-radius:999px;white-space:nowrap}
.st-ok{color:var(--ok);border-color:#215a3d}.st-warn{color:var(--warn);border-color:#6b5416}.st-bad{color:var(--bad);border-color:#6b2438}
.bar{height:8px;background:#26314e;border-radius:10px;overflow:hidden;margin:10px 0}.bar span{display:block;height:100%;background:var(--accent)}
.bar .g-ok{background:linear-gradient(90deg,#38bdf8,#4ade80)}.bar .g-warn{background:var(--warn)}.bar .g-bad{background:var(--bad)}
.metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;margin:14px 0}
.metric{background:var(--card);border-radius:10px;padding:10px 12px}.metric b{display:block;font-size:19px}.metric small{color:var(--muted)}
section.panel{background:color-mix(in srgb,var(--panel) 92%,transparent);border:1px solid var(--line);border-radius:16px;padding:16px 18px;margin-bottom:16px}
section.panel h2{font-size:15px;margin:0 0 10px;color:var(--muted);font-weight:500}
.crit{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:6px;font-size:13px}
.kanban{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:10px}
.col{background:var(--card);border-radius:10px;padding:10px}.col h3{font-size:12px;color:var(--muted);margin:0 0 8px;font-weight:500}
.item{background:#182242;border:1px solid var(--line);border-radius:8px;padding:8px;font-size:12px;margin-bottom:6px}.item small{color:var(--muted)}
table{width:100%;font-size:12.5px;border-collapse:collapse}th{color:var(--muted);font-weight:400;text-align:left;padding:4px 8px 4px 0}td{padding:6px 8px 6px 0;border-top:1px solid #1e2946;vertical-align:top}
.mono{font-family:ui-monospace,monospace}
button{background:#263556;color:var(--text);border:1px solid #40517a;border-radius:9px;padding:7px 11px;cursor:pointer}
.error{color:var(--bad)}.pill{font-size:11px;padding:2px 8px;border-radius:999px;border:1px solid var(--line);color:var(--muted)}
.approve{background:#3a2c14;border:1px solid #6b5416;border-radius:10px;padding:10px 12px;font-size:13px;margin-bottom:8px}
code{background:#0d1427;border-radius:6px;padding:2px 6px;font-size:12px}
</style></head><body><main>
<header><div><h1><a href="#">GoalForge</a></h1><div class="sub" id="crumb">목표 중심 AI 개발 오케스트레이터</div></div><button onclick="route()">새로고침</button></header>
<div id="status" class="sub">불러오는 중…</div><div id="view"></div></main>
<script>
var esc=function(s){return String(s==null?'':s).replace(/[&<>"']/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})};
var timer=null;
function stateClass(s){if(s==='COMPLETED'||s==='READY'||s==='RUNNING')return'st-ok';if(s==='BLOCKED'||s==='FAILED'||s==='CANCELLED'||s==='REPAIR_REQUIRED')return'st-bad';return'st-warn'}
function gaugeClass(p){return p>=97?'g-bad':p>=80?'g-warn':'g-ok'}
function pct(v){return Math.max(0,Math.min(100,v))}
function fmtTokens(v){return v>=1000000?(v/1000000).toFixed(1)+'M':v>=1000?(v/1000).toFixed(1)+'k':String(v)}
async function api(path){var token=sessionStorage.getItem('goalforgeToken')||'';var r=await fetch(path,{headers:token?{Authorization:'Bearer '+token}:{}});if(r.status===401){token=prompt('GoalForge API token')||'';if(token)sessionStorage.setItem('goalforgeToken',token);r=await fetch(path,{headers:{Authorization:'Bearer '+token}})}if(!r.ok)throw new Error((await r.json()).error||r.status);return r.json()}
function gauge(label,used,limit,percent,detail){var p=pct(percent);return'<div class="metric"><small>'+esc(label)+'</small><b>'+esc(used)+(limit?' <small>/ '+esc(limit)+'</small>':'')+'</b><div class="bar"><span class="'+gaugeClass(p)+'" style="width:'+p+'%"></span></div><small>'+esc(detail||p.toFixed(1)+'%')+'</small></div>'}
function countdown(){var el=document.querySelector('[data-deadline]');if(!el)return;var at=new Date(el.getAttribute('data-deadline')).getTime();var left=at-Date.now();if(left<=0){el.textContent='재개 시각 도달 — 재확인 대기';return}var h=Math.floor(left/3600000),m=Math.floor(left%3600000/60000),s=Math.floor(left%60000/1000);el.textContent='재개까지 '+(h>0?h+'시간 ':'')+m+'분 '+s+'초'}
async function renderList(){document.querySelector('#crumb').textContent='목표 중심 AI 개발 오케스트레이터';var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';var data=await api('/api/v1/projects');status.className='sub';status.textContent=data.projects.length+'개 프로젝트';var html='<div id="projects">';for(var i=0;i<data.projects.length;i++){var x=data.projects[i],p=x.project,m=x.metrics,g=x.goal||{};html+='<a class="card" href="#/project/'+encodeURIComponent(p.ID)+'"><div class="row"><div><strong>'+esc(p.Name)+'</strong><div class="sub">'+esc(p.Provider)+' · '+esc(p.Model||'default')+'</div></div><span class="state '+stateClass(p.State)+'">'+esc(p.State)+'</span></div><h3>'+esc(g.Title||'목표 미등록')+'</h3><div class="bar"><span class="g-ok" style="width:'+pct(x.progress_percent)+'%"></span></div><div class="row sub"><span>진행률</span><span>'+x.progress_percent.toFixed(1)+'%</span></div><div class="metrics"><div class="metric"><b>'+m.RunsTotal+'</b><small>실행</small></div><div class="metric"><b>'+m.WorkDone+'</b><small>완료 작업</small></div><div class="metric"><b>$'+m.CostUSD.toFixed(2)+'</b><small>비용</small></div></div></a>'}view.innerHTML=html+'</div>'}
async function renderDetail(id){var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';var d=await api('/api/v1/projects/'+encodeURIComponent(id));var p=d.project,m=d.metrics,g=d.goal||{};status.className='sub';status.textContent='';document.querySelector('#crumb').innerHTML='<a href="#" style="color:var(--muted)">프로젝트</a> / '+esc(p.Name);
var deadline='';var candidates=[];(d.quota_windows||[]).forEach(function(q){if(q.ResumeAt)candidates.push(q.ResumeAt)});(d.scheduler_jobs||[]).forEach(function(j){if(j.Status==='PENDING'&&(j.Type==='RESUME'||j.Type==='CONTINUE'))candidates.push(j.RunAt)});candidates=candidates.filter(function(t){return new Date(t).getTime()>Date.now()}).sort();if(candidates.length)deadline=candidates[0];
var html='<section class="panel"><div class="row"><div><strong style="font-size:19px">'+esc(g.Title||'목표 미등록')+'</strong>'+(g.Version?' <span class="pill">v'+g.Version+(g.Status&&g.Status!=='ACTIVE'?' · '+esc(g.Status):'')+'</span>':'')+'<div class="sub">'+esc(p.Provider)+' · '+esc(p.Model||'default')+(p.AutoCommitEnabled?' · auto-commit':'')+(p.WorktreeEnabled?' · worktrees':'')+'</div></div><div style="text-align:right"><span class="state '+stateClass(p.State)+'">'+esc(p.State)+'</span>'+(deadline?'<div class="sub" style="margin-top:6px" data-deadline="'+esc(deadline)+'"></div>':'')+'</div></div></section>';
html+='<div class="metrics">'+gauge('목표 진행률',d.progress_percent.toFixed(1)+'%','',d.progress_percent,(d.complete?'완료 조건 충족':'진행 중'));
var tokensUsed=m.InputTokens+m.OutputTokens+m.CachedInputTokens+m.ReasoningTokens;
if(d.budget&&d.budget.TokenLimit>0){html+=gauge('토큰 예산',fmtTokens(d.budget.TokensUsed),fmtTokens(d.budget.TokenLimit),d.budget.TokensUsed/d.budget.TokenLimit*100)}else{html+='<div class="metric"><small>사용 토큰</small><b>'+fmtTokens(tokensUsed)+'</b><small>예산 미설정</small></div>'}
if(d.budget&&d.budget.CostLimitUSD>0){html+=gauge('비용 예산','$'+d.budget.CostUsedUSD.toFixed(2),'$'+d.budget.CostLimitUSD.toFixed(0),d.budget.CostUsedUSD/d.budget.CostLimitUSD*100)}else{html+='<div class="metric"><small>누적 비용</small><b>$'+m.CostUSD.toFixed(4)+'</b><small>한도 미설정</small></div>'}
(d.quota_windows||[]).forEach(function(q){html+=gauge('계정 한도 ('+esc(q.LimitType)+')',q.UsedPercent.toFixed(0)+'%','',q.UsedPercent,esc(q.Status)+' · '+esc(q.Confidence))});
html+='</div>';
if(d.pending_approvals&&d.pending_approvals.length){html+='<section class="panel"><h2>승인 대기 '+d.pending_approvals.length+'건</h2>';d.pending_approvals.forEach(function(a){html+='<div class="approve"><strong>'+esc(a.ActionType)+'</strong> — '+esc(a.Reason)+'<div class="sub" style="margin-top:4px">CLI: <code>goalforge approval approve '+esc(a.ID)+'</code></div></div>'});html+='</section>'}
if(d.criteria&&d.criteria.length){html+='<section class="panel"><h2>완료 조건</h2><div class="crit">';d.criteria.forEach(function(c){html+='<span style="color:'+(c.Satisfied?'var(--ok)':'var(--muted)')+'">'+(c.Satisfied?'✓':'○')+' '+esc(c.Type)+' = '+esc(c.ExpectedValue)+(c.ActualValue&&!c.Satisfied?' <small>(현재 '+esc(c.ActualValue)+')</small>':'')+'</span>'});html+='</div></section>'}
var cols=[['대기',['BACKLOG','APPROVED']],['진행 중',['IN_PROGRESS']],['검증 중',['VERIFYING']],['완료',['DONE']],['차단/보류',['BLOCKED','DISCARDED']]];
html+='<section class="panel"><h2>백로그</h2><div class="kanban">';cols.forEach(function(col){var items=(d.work_items||[]).filter(function(w){return col[1].indexOf(w.Status)>=0});html+='<div class="col"><h3>'+col[0]+' · '+items.length+'</h3>';items.slice(0,4).forEach(function(w){html+='<div class="item">'+esc(w.Title)+'<br><small>'+esc(w.ID)+(w.Priority?' · P'+w.Priority:'')+'</small></div>'});if(items.length>4)html+='<div class="sub" style="font-size:11px">+'+(items.length-4)+'건 더</div>';html+='</div>'});html+='</div></section>';
if(d.runs&&d.runs.length){html+='<section class="panel"><h2>최근 실행</h2><table><tr><th>실행</th><th>유형</th><th>작업</th><th>토큰</th><th>상태</th></tr>';d.runs.forEach(function(r){html+='<tr><td class="mono">'+esc(r.ID)+'</td><td>'+esc(r.TaskType||'-')+'</td><td class="mono">'+esc(r.WorkItemID||'-')+'</td><td>'+fmtTokens(r.Tokens)+'</td><td><span class="state '+stateClass(r.State)+'">'+esc(r.State)+'</span></td></tr>'});html+='</table></section>'}
if(d.sessions&&d.sessions.length){html+='<section class="panel"><h2>세션</h2><table><tr><th>세션</th><th>상태</th><th>컨텍스트 토큰</th><th>사유</th></tr>';d.sessions.forEach(function(s){html+='<tr><td class="mono">'+esc(s.SessionID)+'</td><td>'+esc(s.Status)+'</td><td>'+fmtTokens(s.ContextTokensUsed)+'</td><td class="sub">'+esc(s.ReplacementReason||'-')+'</td></tr>'});html+='</table></section>'}
view.innerHTML=html;countdown()}
async function route(){var status=document.querySelector('#status');if(timer){clearInterval(timer);timer=null}try{var h=location.hash;var match=h.match(/^#\/project\/(.+)$/);if(match){await renderDetail(decodeURIComponent(match[1]));timer=setInterval(countdown,1000)}else{await renderList()}}catch(e){status.className='error';status.textContent=e.message}}
window.addEventListener('hashchange',route);route();
</script></body></html>`
