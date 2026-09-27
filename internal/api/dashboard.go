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
.bar .g-run{background:linear-gradient(90deg,#38bdf8,#818cf8)}.bar .g-done{background:var(--ok)}
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
.tabs{display:flex;gap:6px;flex-wrap:wrap;margin:0 0 14px}
.tabs a{font-size:13px;padding:6px 12px;border:1px solid var(--line);border-radius:999px;color:var(--muted);text-decoration:none}
.tabs a.on{color:var(--text);border-color:var(--accent);background:#17243f}
.actions{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}
.actions button[disabled]{opacity:.55;cursor:not-allowed}
.why{font-size:12px;color:var(--muted);margin-top:6px}
.filters{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-bottom:10px}
input,select,textarea{background:var(--card);color:var(--text);border:1px solid var(--line);border-radius:8px;padding:7px 9px;font:inherit;font-size:13px}
textarea{width:100%;min-height:70px;resize:vertical}
input[type=search]{min-width:220px}
label{display:block;font-size:12px;color:var(--muted);margin:10px 0 4px}
.grid2{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:0 16px}
pre.diff{white-space:pre;overflow:auto;max-height:460px;font-size:12px;background:var(--card);border-radius:8px;padding:10px;margin:0;font-family:ui-monospace,monospace}
pre.diff i{font-style:normal;display:block}
pre.diff i.add{color:#86efac}pre.diff i.del{color:#fda4af}pre.diff i.hunk{color:var(--accent)}pre.diff i.meta{color:var(--muted)}
.attn{background:#2a1c2e;border:1px solid #6b2438;border-radius:10px;padding:10px 12px;margin-bottom:8px;font-size:13px}
.attn.warn{background:#3a2c14;border-color:#6b5416}
.ev{display:grid;grid-template-columns:auto 1fr auto;gap:8px 12px;font-size:13px;align-items:baseline}
.badge{font-size:11px;padding:2px 7px;border-radius:6px;border:1px solid var(--line);color:var(--muted);white-space:nowrap}
.badge.met{color:var(--ok);border-color:#215a3d}.badge.unmet{color:var(--bad);border-color:#6b2438}.badge.none{color:var(--muted)}
a.plain{color:var(--accent);text-decoration:none}
.kbd{font-size:11px;border:1px solid var(--line);border-radius:5px;padding:1px 5px;color:var(--muted)}
</style></head><body><main>
<header><div><h1><a href="#">GoalForge</a></h1><div class="sub" id="crumb">목표 중심 AI 개발 오케스트레이터</div></div><button onclick="route()">새로고침</button></header>
<div id="status" class="sub">불러오는 중…</div><div id="view"></div></main>
<script>
var esc=function(s){return String(s==null?'':s).replace(/[&<>"']/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})};
var timer=null;
function stateClass(s){if(s==='COMPLETED'||s==='READY'||s==='RUNNING')return'st-ok';if(s==='BLOCKED'||s==='FAILED'||s==='CANCELLED'||s==='REPAIR_REQUIRED')return'st-bad';return'st-warn'}
// gaugeClass colours resource burn: nearing a limit is a warning.
function gaugeClass(p){return p>=97?'g-bad':p>=80?'g-warn':'g-ok'}
// progressClass colours goal progress, where high is good. Sharing
// gaugeClass painted a goal at 98% complete red, as if it were failing.
function progressClass(complete){return complete?'g-done':'g-run'}
function progressGauge(label,percent,complete,detail){var p=pct(percent);return'<div class="metric"><small>'+esc(label)+'</small><b>'+p.toFixed(1)+'%'+(complete?' <small style="color:var(--ok)">완료</small>':'')+'</b><div class="bar"><span class="'+progressClass(complete)+'" style="width:'+p+'%"></span></div><small>'+esc(detail||'')+'</small></div>'}
function scopeNote(d){var sc=d.progress_scope||{};var parts=[];if(sc.total_items)parts.push('작업 '+sc.done_items+'/'+sc.total_items+'건');if(sc.discarded_items)parts.push('폐기 '+sc.discarded_items+'건 기준선 제외');parts.push(d.complete?'완료 조건 충족':(sc.criteria_met?'완료 조건 충족 · 작업 잔여':'완료 조건 미충족'));return parts.join(' · ')}
function pct(v){return Math.max(0,Math.min(100,v))}
function fmtTokens(v){return v>=1000000?(v/1000000).toFixed(1)+'M':v>=1000?(v/1000).toFixed(1)+'k':String(v)}
async function api(path,options){options=options||{};var token=sessionStorage.getItem('goalforgeToken')||'';var headers={'X-Requested-With':'GoalForge'};if(options.body)headers['Content-Type']='application/json';if(token)headers.Authorization='Bearer '+token;var init={method:options.method||'GET',headers:headers,body:options.body};var r=await fetch(path,init);if(r.status===401){token=prompt('GoalForge API token')||'';if(token)sessionStorage.setItem('goalforgeToken',token);headers.Authorization='Bearer '+token;r=await fetch(path,init)}if(!r.ok)throw new Error((await r.json()).error||r.status);return r.json()}
async function decide(projectID,approvalID,action,label){if(!confirm(label+' — 계속할까요?'))return;try{await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/approvals/'+encodeURIComponent(approvalID)+'/'+action,{method:'POST'});route()}catch(e){alert(e.message)}}
async function decideWork(projectID,workID,status,label){if(!confirm(label+' — 계속할까요?'))return;try{await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/work/'+encodeURIComponent(workID)+'/status/'+status,{method:'POST'});route()}catch(e){alert(e.message)}}
function fmtTime(t){var d=new Date(t);if(isNaN(d))return'';return d.toLocaleString('ko-KR',{month:'numeric',day:'numeric',hour:'2-digit',minute:'2-digit'})}
function usageChart(series){if(!series||!series.length)return'';var w=1040,h=150,pad=26,bw=Math.min(64,Math.floor((w-pad)/series.length)-10);var max=1;series.forEach(function(pt){if(pt.Tokens>max)max=pt.Tokens});var svg='<svg viewBox="0 0 '+w+' '+(h+38)+'" style="width:100%;height:auto" role="img" aria-label="일별 토큰 사용량">';series.forEach(function(pt,i){var x=pad+i*((w-pad)/series.length),bh=Math.max(2,Math.round(pt.Tokens/max*h));svg+='<rect x="'+x+'" y="'+(h-bh)+'" width="'+bw+'" height="'+bh+'" rx="4" fill="#38bdf8" opacity="0.85"></rect>';svg+='<text x="'+(x+bw/2)+'" y="'+(h-bh-6)+'" text-anchor="middle" font-size="11" fill="#9daaca">'+fmtTokens(pt.Tokens)+'</text>';svg+='<text x="'+(x+bw/2)+'" y="'+(h+14)+'" text-anchor="middle" font-size="10" fill="#9daaca">'+esc(pt.Date.slice(5))+'</text>';svg+='<text x="'+(x+bw/2)+'" y="'+(h+30)+'" text-anchor="middle" font-size="10" fill="#6d7ba0">$'+pt.CostUSD.toFixed(3)+' · '+pt.Runs+'회</text>'});return svg+'</svg>'}
var liveStream=null;
// The live feed streams events over SSE instead of re-fetching the whole run
// every three seconds, and it shows its own connection state: a feed that has
// gone quiet because the connection dropped must not look like a run that has
// gone quiet because nothing is happening.
function liveStatus(state,detail){var box=document.querySelector('#livestate');if(!box)return;
var cls=state==='연결됨'?'st-ok':(state==='재연결 중'?'st-warn':'st-bad');
box.innerHTML='<span class="state '+cls+'">'+esc(state)+'</span> <span class="sub">'+esc(detail||'')+'</span>'}
function liveAppend(events){var body=document.querySelector('#livebody');if(!body)return;
events.forEach(function(e){var row=document.createElement('tr');
row.innerHTML='<td style="width:110px"><span class="pill">'+esc(e.Type)+'</span></td><td class="sub mono" style="font-size:11px;word-break:break-all">'+esc(e.Raw.length>300?e.Raw.slice(0,300)+'…':e.Raw)+'</td>';
body.appendChild(row)});
while(body.children.length>200)body.removeChild(body.firstChild);
var wrap=document.querySelector('#livescroll');if(wrap)wrap.scrollTop=wrap.scrollHeight}
function startLive(projectID,runID){stopLive();var stream={closed:false,after:0,attempt:0};liveStream=stream;
function fail(message){if(stream.closed)return;stream.attempt++;
var delay=Math.min(30000,1000*Math.pow(2,stream.attempt-1));
liveStatus('재연결 중',message+' · '+Math.round(delay/1000)+'초 후 재시도 ('+stream.attempt+'회)');
stream.timer=setTimeout(function(){connect()},delay)}
async function connect(){if(stream.closed)return;liveStatus('연결 중','실행 '+runID);
try{var token=sessionStorage.getItem('goalforgeToken')||'';var headers={'X-Requested-With':'GoalForge','Accept':'text/event-stream'};if(token)headers.Authorization='Bearer '+token;
var response=await fetch('/api/v1/projects/'+encodeURIComponent(projectID)+'/runs/'+encodeURIComponent(runID)+'/stream?after='+stream.after,{headers:headers});
if(!response.ok||!response.body){fail('연결 실패 (HTTP '+response.status+')');return}
stream.attempt=0;liveStatus('연결됨','실행 '+runID);
var reader=response.body.getReader(),decoder=new TextDecoder(),buffer='';
while(true){var chunk=await reader.read();if(chunk.done)break;
buffer+=decoder.decode(chunk.value,{stream:true});
var frames=buffer.split('\n\n');buffer=frames.pop();
frames.forEach(function(frame){handleFrame(frame)})}
if(!stream.closed&&!stream.done)fail('연결이 끊어졌습니다')}
catch(e){fail(esc(e.message||'네트워크 오류'))}}
function handleFrame(frame){var name='message',data='';
frame.split('\n').forEach(function(line){if(line.indexOf('event: ')===0)name=line.slice(7);else if(line.indexOf('data: ')===0)data+=line.slice(6)});
if(!data)return;var payload;try{payload=JSON.parse(data)}catch(e){return}
if(name==='events'){stream.after=payload.last_id||stream.after;liveAppend(payload.events||[]);
liveStatus('연결됨','마지막 수신 '+new Date().toLocaleTimeString('ko-KR')+' · 상태 '+stateLabel(payload.state)+' · '+fmtTokens(payload.tokens||0)+' 토큰')}
else if(name==='state'){liveStatus('연결됨','상태 '+stateLabel(payload.state)+' · '+new Date().toLocaleTimeString('ko-KR'))}
else if(name==='done'){stream.done=true;liveStatus('종료됨','실행이 '+stateLabel(payload.state)+' 상태로 끝났습니다');setTimeout(route,600)}
else if(name==='error'){fail(payload.error||'스트림 오류')}}
connect()}
function stopLive(){if(!liveStream)return;liveStream.closed=true;if(liveStream.timer)clearTimeout(liveStream.timer);liveStream=null}
function approvalCard(projectID,a,projectName){return'<div class="approve"><div class="row"><div><strong>'+esc(a.ActionType)+'</strong>'+(projectName?' <span class="pill">'+esc(projectName)+'</span>':'')+' — '+esc(a.Reason)+approvalScope(a)+'<div class="sub" style="margin-top:4px"><a class="plain" href="#/project/'+encodeURIComponent(projectID)+'/approval/'+encodeURIComponent(a.ID)+'">근거 보고 결정 →</a> · CLI: <code>goalforge approval approve '+esc(a.ID)+'</code></div></div><div style="display:flex;gap:6px;flex-shrink:0"><button onclick="decide(\''+esc(projectID)+'\',\''+esc(a.ID)+'\',\'approve\',\''+esc(a.ActionType)+' 승인\')">승인</button><button onclick="decide(\''+esc(projectID)+'\',\''+esc(a.ID)+'\',\'reject\',\''+esc(a.ActionType)+' 거절\')">거절</button></div></div></div>'}
function gauge(label,used,limit,percent,detail){var p=pct(percent);return'<div class="metric"><small>'+esc(label)+'</small><b>'+esc(used)+(limit?' <small>/ '+esc(limit)+'</small>':'')+'</b><div class="bar"><span class="'+gaugeClass(p)+'" style="width:'+p+'%"></span></div><small>'+esc(detail||p.toFixed(1)+'%')+'</small></div>'}
// stateLabel explains a machine state in the user's language; the code stays
// visible so it remains greppable in logs and the CLI.
function stateLabel(s){var m={CREATED:'생성됨',READY:'실행 준비됨',RUNNING:'실행 중',VERIFYING:'검증 중',CHECKPOINTING:'체크포인트 기록 중',REPAIR_REQUIRED:'검증 실패 — 수정 필요',BLOCKED:'차단됨 — 사람 판단 필요',PAUSED:'일시정지',CANCELLED:'취소됨',FAILED:'실패',COMPLETED:'완료',RESUMING:'재개 중',DRAINING:'정리 중',QUOTA_BLOCKED:'계정 한도 대기'};return m[s]||s}
function workLabel(s){var m={BACKLOG:'대기',APPROVED:'승인됨',IN_PROGRESS:'진행 중',VERIFYING:'검증 중',DONE:'완료',BLOCKED:'보류',DISCARDED:'폐기'};return m[s]||s}
function stateChip(s){return'<span class="state '+stateClass(s)+'">'+esc(stateLabel(s))+' <small class="mono">'+esc(s)+'</small></span>'}
// Criterion status is shown with a text marker as well as colour so it is
// readable without relying on colour alone.
function critBadge(c){var st=c.Status||(c.Satisfied?'MET':'NO_EVIDENCE');if(st==='MET')return'<span class="badge met">✓ 충족</span>';if(st==='UNMET')return'<span class="badge unmet">△ 기준 미달</span>';return'<span class="badge none">○ 증거 없음</span>'}
function shortSHA(v){return esc(String(v||'').slice(0,12))}
function fmtUSD(v){return '$'+(v||0).toFixed(v&&v<1?4:2)}
function tabsHTML(projectID,active){var tabs=[['overview','개요'],['plan','계획'],['runs','실행'],['verify','검증'],['cost','비용']];var html='<div class="tabs">';tabs.forEach(function(t){var href='#/project/'+encodeURIComponent(projectID)+(t[0]==='overview'?'':'/tab/'+t[0]);html+='<a class="'+(active===t[0]?'on':'')+'" href="'+href+'">'+t[1]+'</a>'});return html+'</div>'}
// diffHTML colours a patch by line kind. The patch is escaped first: it is
// untrusted repository content being rendered into the page.
function diffHTML(text){var out='';String(text).split('\n').forEach(function(line){var cls='';if(line.indexOf('+++')===0||line.indexOf('---')===0||line.indexOf('diff --git')===0||line.indexOf('index ')===0)cls='meta';else if(line.indexOf('@@')===0)cls='hunk';else if(line.charAt(0)==='+')cls='add';else if(line.charAt(0)==='-')cls='del';out+='<i class="'+cls+'">'+esc(line||' ')+'</i>'});return'<pre class="diff">'+out+'</pre>'}
async function act(projectID,action,label,detail){if(!confirm(label+'\n\n'+detail+'\n\n계속할까요?'))return;try{var r=await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/actions/'+action,{method:'POST'});alert(r.detail||'완료');route()}catch(e){alert(e.message)}}
// actionBar offers what the current state actually allows, and says why a
// button is disabled instead of leaving the state code to be interpreted.
function actionBar(d){var p=d.project,id=p.ID;var running=(d.runs||[]).some(function(r){return r.State==='RUNNING'});var pending=(d.pending_approvals||[]).length;var failed=null;(d.runs||[]).forEach(function(r){if(!failed&&(r.State==='REPAIR_REQUIRED'||r.State==='FAILED'))failed=r.ID});var html='<div class="actions">';
var startWhy='';if(running)startWhy='실행 중인 세션이 있습니다';else if(p.State==='COMPLETED')startWhy='목표가 완료되었습니다';else if(p.State==='BLOCKED')startWhy='차단 원인을 먼저 해결해야 합니다';else if(!d.goal)startWhy='목표가 없습니다';
html+='<button '+(startWhy?'disabled':'onclick="act(\''+esc(id)+'\',\'continue\',\'다음 작업 실행\',\'워커가 다음 작업 1건을 실행하고 검증합니다.\')"')+'>다음 작업 실행</button>';
html+='<button '+(running?'onclick="act(\''+esc(id)+'\',\'pause\',\'일시정지\',\'현재 턴이 끝나면 멈춥니다. 작업 공간과 세션은 보존됩니다.\')"':'disabled')+'>일시정지</button>';
html+='<button onclick="act(\''+esc(id)+'\',\'cancel\',\'중지\',\'실행을 중단합니다. 검증을 통과하지 못한 변경은 작업 공간에 남고 작업은 백로그로 돌아갑니다.\')">중지</button>';
if(failed)html+='<a class="plain" style="align-self:center" href="#/project/'+encodeURIComponent(id)+'/run/'+encodeURIComponent(failed)+'">실패 분석 →</a>';
if(pending)html+='<a class="plain" style="align-self:center" href="#/project/'+encodeURIComponent(id)+'/approval/'+encodeURIComponent(d.pending_approvals[0].ID)+'">변경 검토 '+pending+'건 →</a>';
html+='</div>';
if(startWhy)html+='<div class="why">다음 작업 실행 불가: '+esc(startWhy)+'</div>';
return html}
function countdown(){var el=document.querySelector('[data-deadline]');if(!el)return;var at=new Date(el.getAttribute('data-deadline')).getTime();var left=at-Date.now();if(left<=0){el.textContent='재개 시각 도달 — 재확인 대기';return}var h=Math.floor(left/3600000),m=Math.floor(left%3600000/60000),s=Math.floor(left%60000/1000);el.textContent='재개까지 '+(h>0?h+'시간 ':'')+m+'분 '+s+'초'}
// attentionCards puts what needs a decision first: the cause, what it affects,
// and the recommended action, so the home view is not a list to walk through.
function attentionCards(projects,inbox){var items=[];
(inbox||[]).forEach(function(a){items.push({level:'warn',title:esc(a.ProjectName||a.ProjectID)+' · 승인 대기',cause:esc(a.ActionType)+' — '+esc(a.Reason),effect:'승인 전까지 변경이 반영되지 않습니다',action:'<a class="plain" href="#/project/'+encodeURIComponent(a.ProjectID)+'/approval/'+encodeURIComponent(a.ID)+'">근거 확인하고 결정 →</a>'})});
(projects||[]).forEach(function(x){var p=x.project,link='<a class="plain" href="#/project/'+encodeURIComponent(p.ID)+'">프로젝트 열기 →</a>';
if(p.State==='REPAIR_REQUIRED')items.push({level:'bad',title:esc(p.Name)+' · 복구 필요',cause:'마지막 실행이 검증을 통과하지 못했습니다',effect:'작업이 백로그로 돌아가 목표가 진행되지 않습니다',action:link});
else if(p.State==='BLOCKED')items.push({level:'bad',title:esc(p.Name)+' · 차단됨',cause:'정책 위반 또는 승인 필요로 실행이 멈췄습니다',effect:'사람이 판단하기 전까지 자동 실행이 재개되지 않습니다',action:link});
else if(p.State==='QUOTA_BLOCKED')items.push({level:'warn',title:esc(p.Name)+' · 계정 한도 대기',cause:'제공자 한도에 도달했습니다',effect:'한도 리셋까지 실행이 지연됩니다',action:link});
var integration=x.integration||{};
if(integration.Pending)items.push({level:'warn',title:esc(p.Name)+' · 통합 검증 필요',cause:esc(integration.Reason||'병합 이후 기본 브랜치가 검증되지 않았습니다'),effect:'각 작업은 격리된 worktree 에서만 검증되어 병합 결과는 아직 확인되지 않았습니다',action:'<code>goalforge verify integration</code>'});
var b=x.budget;if(b){var ratios=[];if(b.TokenLimit>0)ratios.push(['토큰',b.TokensUsed/b.TokenLimit*100]);if(b.CostLimitUSD>0)ratios.push(['비용',b.CostUsedUSD/b.CostLimitUSD*100]);ratios.forEach(function(r){if(r[1]>=80)items.push({level:r[1]>=97?'bad':'warn',title:esc(p.Name)+' · '+r[0]+' 예산 '+r[1].toFixed(0)+'%',cause:r[0]+' 한도의 '+r[1].toFixed(0)+'% 를 사용했습니다',effect:r[1]>=97?'한도 초과로 다음 실행이 거부될 수 있습니다':'남은 예산으로 큰 작업을 시작하기 어렵습니다',action:link})})}});
if(!items.length)return'';var html='<section class="panel"><h2>조치가 필요한 항목 · '+items.length+'건</h2>';
items.forEach(function(i){html+='<div class="attn'+(i.level==='warn'?' warn':'')+'"><strong>'+i.title+'</strong><div class="sub" style="margin-top:4px">원인: '+i.cause+'</div><div class="sub">영향: '+i.effect+'</div><div style="margin-top:6px">'+i.action+'</div></div>'});
return html+'</section>'}
// activityPanel summarizes what ran while nobody was watching, so returning
// after a night of automated work does not mean reading run logs.
function activityPanel(r){if(!r)return'';
var active=(r.Projects||[]).filter(function(p){return p.Runs>0||p.WorkCompleted>0});
if(!active.length&&!(r.Unresolved||[]).length)return'';
var html='<section class="panel"><h2>최근 24시간</h2><div class="sub" style="margin-bottom:8px">실행 '+r.Runs+'회 · 검증 완료 작업 '+r.WorkCompleted+'건 · '+fmtTokens(r.Tokens)+' 토큰 · '+fmtUSD(r.CostUSD)+'</div>';
if(active.length){html+='<table><tr><th>프로젝트</th><th>실행</th><th>검증 완료</th><th>진행률</th><th>비용</th></tr>';
active.forEach(function(p){html+='<tr><td><a class="plain" href="#/project/'+encodeURIComponent(p.ProjectID)+'">'+esc(p.Name)+'</a></td><td>'+p.Runs+'</td><td>'+p.WorkCompleted+'</td><td>'+p.ProgressPercent.toFixed(1)+'%</td><td>'+fmtUSD(p.CostUSD)+'</td></tr>'});
html+='</table>'}
if((r.Unresolved||[]).length){html+='<h2 style="margin-top:14px">미해결 '+r.Unresolved.length+'건</h2><table>';
r.Unresolved.forEach(function(u){html+='<tr><td>'+stateChip(u.State)+'</td><td><a class="plain mono" href="#/project/'+encodeURIComponent(u.ProjectID)+'/run/'+encodeURIComponent(u.RunID)+'">'+esc(u.RunID)+'</a></td><td>'+(u.FailureKind?'<span class="badge unmet">'+esc(failureLabel(u.FailureKind))+'</span>':'')+'</td><td class="sub">'+esc(u.Reason||'')+'</td></tr>'});
html+='</table>'}
return html+'</section>'}
async function renderList(){document.querySelector('#crumb').textContent='목표 중심 AI 개발 오케스트레이터';var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';var data=await api('/api/v1/projects');var inbox=await api('/api/v1/approvals');status.className='sub';status.textContent=data.projects.length+'개 프로젝트';
var html=attentionCards(data.projects,inbox.approvals);
try{html+=activityPanel(await api('/api/v1/report?since=24h'))}catch(e){}
if(!data.projects.length)html+='<section class="panel"><h2>아직 프로젝트가 없습니다</h2><div class="sub">저장소에서 <code>goalforge project init --name 이름 --provider claude</code> 로 시작하고, <code>goalforge doctor</code> 로 환경을 먼저 진단하세요.</div></section>';
html+='<div class="actions" style="margin-bottom:14px"><a class="plain" href="#/new">+ 새 프로젝트 설정</a> <span class="sub"><span class="kbd">/</span> 백로그 검색 · <span class="kbd">Esc</span> 뒤로 · <span class="kbd">r</span> 새로고침</span></div>';html+='<div id="projects">';for(var i=0;i<data.projects.length;i++){var x=data.projects[i],p=x.project,m=x.metrics,g=x.goal||{};html+='<a class="card" href="#/project/'+encodeURIComponent(p.ID)+'"><div class="row"><div><strong>'+esc(p.Name)+'</strong><div class="sub">'+esc(p.Provider)+' · '+esc(p.Model||'default')+'</div></div>'+stateChip(p.State)+'</div><h3>'+esc(g.Title||'목표 미등록')+'</h3><div class="bar"><span class="'+progressClass(x.complete)+'" style="width:'+pct(x.progress_percent)+'%"></span></div><div class="row sub"><span>진행률'+(x.pending_approvals_count?' · 승인 대기 '+x.pending_approvals_count+'건':'')+'</span><span>'+x.progress_percent.toFixed(1)+'%</span></div><div class="metrics"><div class="metric"><b>'+m.RunsTotal+'</b><small>실행</small></div><div class="metric"><b>'+m.WorkDone+'</b><small>완료 작업</small></div><div class="metric"><b>'+fmtUSD(m.CostUSD)+'</b><small>비용</small></div></div></a>'}view.innerHTML=html+'</div>'}
var detailCache=null;
function goalHead(d,tab){var p=d.project,g=d.goal||{};
var deadline='';var candidates=[];(d.quota_windows||[]).forEach(function(q){if(q.ResumeAt)candidates.push(q.ResumeAt)});(d.scheduler_jobs||[]).forEach(function(j){if(j.Status==='PENDING'&&(j.Type==='RESUME'||j.Type==='CONTINUE'))candidates.push(j.RunAt)});candidates=candidates.filter(function(t){return new Date(t).getTime()>Date.now()}).sort();if(candidates.length)deadline=candidates[0];
var html='<section class="panel"><div class="row"><div><strong style="font-size:19px">'+esc(g.Title||'목표 미등록')+'</strong>'+(g.Version?' <span class="pill">v'+g.Version+(g.Status&&g.Status!=='ACTIVE'?' · '+esc(g.Status):'')+'</span>':'')+'<div class="sub">'+esc(p.Provider)+' · '+esc(p.Model||'default')+(p.AutoCommitEnabled?' · auto-commit':'')+(p.WorktreeEnabled?' · worktrees':'')+'</div></div><div style="text-align:right">'+stateChip(p.State)+(deadline?'<div class="sub" style="margin-top:6px" data-deadline="'+esc(deadline)+'"></div>':'')+'</div></div>';
if(g.Objective)html+='<div class="sub" style="margin-top:10px">'+esc(g.Objective)+'</div>';
html+=actionBar(d)+'</section>';
return html+tabsHTML(p.ID,tab)}
// overviewTab answers "what is happening and what do I do next" before any
// detail: the goal, the item in flight, the last verification, and the cost.
function overviewTab(d){var p=d.project,m=d.metrics;
var current=(d.work_items||[]).filter(function(w){return w.Status==='IN_PROGRESS'||w.Status==='VERIFYING'})[0];
var next=(d.work_items||[]).filter(function(w){return w.Status==='APPROVED'||w.Status==='BACKLOG'})[0];
var lastRun=(d.runs||[])[0];
var html='<div class="metrics">'+progressGauge('목표 진행률',d.progress_percent,d.complete,scopeNote(d));
if(d.budget&&d.budget.CostLimitUSD>0)html+=gauge('비용 예산',fmtUSD(d.budget.CostUsedUSD),'$'+d.budget.CostLimitUSD.toFixed(0),d.budget.CostUsedUSD/d.budget.CostLimitUSD*100);else html+='<div class="metric"><small>누적 비용</small><b>'+fmtUSD(m.CostUSD)+'</b><small>한도 미설정</small></div>';
html+='<div class="metric"><small>완료 작업</small><b>'+m.WorkDone+'</b><small>실행 '+m.RunsTotal+'회</small></div></div>';
html+='<section class="panel"><h2>지금 상태</h2><table>';
html+='<tr><th style="width:110px">현재 작업</th><td>'+(current?workLink(p.ID,current)+' <span class="badge">'+esc(workLabel(current.Status))+'</span>':'<span class="sub">진행 중인 작업이 없습니다</span>')+'</td></tr>';
html+='<tr><th>다음 작업</th><td>'+(next?workLink(p.ID,next)+' <span class="badge">'+esc(workLabel(next.Status))+'</span>':'<span class="sub">대기 중인 작업이 없습니다 — <code>goalforge ideas</code> 또는 <code>goalforge replan</code></span>')+'</td></tr>';
html+='<tr><th>마지막 실행</th><td>'+(lastRun?'<a class="plain mono" href="#/project/'+encodeURIComponent(p.ID)+'/run/'+encodeURIComponent(lastRun.ID)+'">'+esc(lastRun.ID)+'</a> '+stateChip(lastRun.State)+' <span class="sub">'+fmtTokens(lastRun.Tokens)+' 토큰 · '+fmtUSD(lastRun.CostUSD)+'</span>':'<span class="sub">기록된 실행이 없습니다</span>')+'</td></tr>';
html+='<tr><th>예상 비용</th><td>'+estimateNote(d)+'</td></tr></table></section>';
html+=criteriaPanel(d);
var liveRun=null;(d.runs||[]).forEach(function(r){if(!liveRun&&r.State==='RUNNING')liveRun=r.ID});
if(liveRun)html+='<section class="panel" id="livefeed"><div class="row"><h2 style="margin:0">라이브 실행</h2><div id="livestate"></div></div><div id="livescroll" style="max-height:300px;overflow:auto;margin-top:10px"><table><tbody id="livebody"></tbody></table></div><div class="why"><a class="plain" href="#/project/'+encodeURIComponent(p.ID)+'/run/'+encodeURIComponent(liveRun)+'">실행 상세 열기 →</a></div></section>';
if(d.pending_approvals&&d.pending_approvals.length){html+='<section class="panel"><h2>승인 대기 '+d.pending_approvals.length+'건</h2>';d.pending_approvals.forEach(function(a){html+=approvalCard(p.ID,a,'')});html+='</section>'}
return {html:html,live:liveRun}}
function workLink(projectID,w){return'<a class="plain" href="#/project/'+encodeURIComponent(projectID)+'/work/'+encodeURIComponent(w.ID)+'">'+esc(w.Title)+'</a> <small class="mono sub">'+esc(w.ID)+'</small>'}
function estimateNote(d){var pending=(d.work_items||[]).filter(function(w){return w.Status==='APPROVED'||w.Status==='BACKLOG'});var known=pending.filter(function(w){return w.EstimatedTokens>0});var total=0;known.forEach(function(w){total+=w.EstimatedTokens});
if(!pending.length)return'<span class="sub">남은 작업이 없습니다</span>';
if(!known.length)return'<span class="sub">남은 '+pending.length+'건에 토큰 예상치가 없습니다 — 실행 이력 평균으로 추정됩니다</span>';
return fmtTokens(total)+' 토큰 <span class="sub">(예상치가 있는 '+known.length+'/'+pending.length+'건 합계)</span>'}
// criteriaPanel separates "the work is done" from "the goal is met": each
// criterion is shown with the evidence that decided it.
function criteriaPanel(d){if(!d.criteria||!d.criteria.length)return'<section class="panel"><h2>완료 조건</h2><div class="sub">완료 조건이 없어 목표는 완료로 판정되지 않습니다. <code>goalforge goal set --criterion build_passed=true</code></div></section>';
var html='<section class="panel"><h2>완료 조건과 근거</h2><table><tr><th>조건</th><th>기준</th><th>측정값</th><th>상태</th><th>근거</th></tr>';
d.criteria.forEach(function(c){var evidence='<span class="sub">없음</span>';if(c.RunID)evidence='<a class="plain mono" href="#/project/'+encodeURIComponent(d.project.ID)+'/run/'+encodeURIComponent(c.RunID)+'">'+esc(c.RunID)+'</a>'+(c.MeasuredAt?' <span class="sub">'+fmtTime(c.MeasuredAt)+'</span>':'');
html+='<tr><td>'+esc(c.Type)+'</td><td class="mono">'+esc(c.ExpectedValue)+'</td><td class="mono">'+esc(c.ActualValue||'-')+'</td><td>'+critBadge(c)+'</td><td>'+evidence+'</td></tr>'});
return html+'</table></section>'}
// planTab is the whole backlog with search and status filters; the kanban only
// ever showed four items per column and offered no way to the rest.
// decisionsPanel keeps settled architecture visible where planning happens,
// so a decision is inherited rather than re-derived by the next session.
function decisionsPanel(){return'<section class="panel"><div class="row"><h2 style="margin:0">설계 결정</h2><button onclick="toggleDecisionForm()">결정 기록</button></div>'+
'<div id="decision-form" style="display:none;margin-top:10px">'+
'<label for="dec-title">제목</label><input id="dec-title" style="width:100%">'+
'<label for="dec-decision">결정한 내용</label><textarea id="dec-decision"></textarea>'+
'<label for="dec-context">왜 결정이 필요했는가</label><textarea id="dec-context"></textarea>'+
'<label for="dec-alternatives">검토했지만 제외한 대안과 이유</label><textarea id="dec-alternatives"></textarea>'+
'<label for="dec-consequences">이 결정이 감수하는 것</label><textarea id="dec-consequences"></textarea>'+
'<div class="actions"><button onclick="saveDecision()">저장</button><button onclick="toggleDecisionForm()">취소</button></div></div>'+
'<div id="decision-list" class="sub" style="margin-top:8px">불러오는 중…</div></section>'}
function toggleDecisionForm(){var box=document.querySelector('#decision-form');if(box)box.style.display=box.style.display==='none'?'block':'none'}
async function loadDecisions(){var box=document.querySelector('#decision-list');if(!box)return;
try{var data=await api('/api/v1/projects/'+encodeURIComponent(detailCache.project.ID)+'/decisions');var list=data.decisions||[];
if(!list.length){box.innerHTML='기록된 설계 결정이 없습니다. 구조를 정할 때 남겨 두면 이후 실행이 같은 결론을 다시 도출하지 않습니다.';return}
var html='';list.forEach(function(dec){html+='<div class="attn warn" style="background:var(--card);border-color:var(--line)"><strong>'+esc(dec.Title)+'</strong> <span class="badge">'+esc(dec.Status)+'</span>'+
'<div style="margin-top:4px">'+esc(dec.Decision)+'</div>'+
(dec.Alternatives?'<div class="sub" style="margin-top:4px">제외한 대안: '+esc(dec.Alternatives)+'</div>':'')+
(dec.Consequences?'<div class="sub">영향: '+esc(dec.Consequences)+'</div>':'')+
'<div class="sub mono" style="margin-top:4px;font-size:11px">'+esc(dec.ID)+(dec.BaseCommit?' @ '+shortSHA(dec.BaseCommit):'')+' · '+fmtTime(dec.CreatedAt)+'</div></div>'});
box.innerHTML=html}catch(e){box.innerHTML='<span class="error">'+esc(e.message)+'</span>'}}
async function saveDecision(){var body={title:document.querySelector('#dec-title').value,decision:document.querySelector('#dec-decision').value,context:document.querySelector('#dec-context').value,alternatives:document.querySelector('#dec-alternatives').value,consequences:document.querySelector('#dec-consequences').value};
if(!body.title||!body.decision){alert('제목과 결정 내용이 필요합니다');return}
try{await api('/api/v1/projects/'+encodeURIComponent(detailCache.project.ID)+'/decisions',{method:'POST',body:JSON.stringify(body)});route()}catch(e){alert(e.message)}}
function planTab(d){var p=d.project;
var statuses=['BACKLOG','APPROVED','IN_PROGRESS','VERIFYING','DONE','BLOCKED','DISCARDED'];
var html='<section class="panel"><h2>백로그</h2><div class="filters"><input type="search" id="wq" placeholder="제목·ID·범위 검색 ( / )" value="'+esc(planQuery)+'" onkeyup="if(event.key===\'Enter\')loadPlan()"><select id="wstatus" onchange="loadPlan()"><option value="">전체 상태</option>';
statuses.forEach(function(st){html+='<option value="'+st+'"'+(planStatus===st?' selected':'')+'>'+workLabel(st)+'</option>'});
html+='</select><button onclick="loadPlan()">검색</button><span class="sub" id="wcount"></span></div><div id="worklist" class="sub">불러오는 중…</div></section>';
var cols=[['대기',['BACKLOG','APPROVED']],['진행 중',['IN_PROGRESS']],['검증 중',['VERIFYING']],['완료',['DONE']],['차단/보류',['BLOCKED','DISCARDED']]];
html+='<section class="panel"><h2>상태별 요약</h2><div class="kanban">';cols.forEach(function(col){var items=(d.work_items||[]).filter(function(w){return col[1].indexOf(w.Status)>=0});html+='<div class="col"><h3>'+col[0]+' · '+items.length+'</h3>';items.slice(0,4).forEach(function(w){html+='<div class="item">'+workLink(p.ID,w)+'<br><small>'+(w.Priority?'P'+w.Priority:'')+(w.EstimatedTokens?' · '+fmtTokens(w.EstimatedTokens)+' 토큰':'')+'</small></div>'});if(items.length>4)html+='<div class="sub" style="font-size:11px"><a class="plain" href="#" onclick="filterStatus(\''+col[1][0]+'\');return false">+'+(items.length-4)+'건 더 보기</a></div>';html+='</div>'});html+='</div></section>';
var triage=(d.work_items||[]).filter(function(w){return d.idea_scores&&d.idea_scores[w.ID]&&(w.Status==='BACKLOG'||w.Status==='BLOCKED')});triage.sort(function(a,b){return d.idea_scores[b.ID].PriorityScore-d.idea_scores[a.ID].PriorityScore});
if(triage.length){html+='<section class="panel"><h2>아이디어 triage · '+triage.length+'건</h2>';triage.forEach(function(w){var sc=d.idea_scores[w.ID];html+='<div class="approve" style="background:var(--card);border-color:var(--line)"><div class="row"><div><strong>'+workLink(p.ID,w)+'</strong> <span class="pill">점수 '+sc.PriorityScore.toFixed(1)+'</span>'+(sc.ScopeExpansion?' <span class="pill" style="color:var(--warn);border-color:#6b5416">범위 확장</span>':'')+(w.Status==='BLOCKED'?' <span class="pill" style="color:var(--bad)">보류됨</span>':'')+'<div class="sub" style="margin-top:4px">기여 '+sc.GoalContribution.toFixed(0)+' · 가치 '+sc.UserValue.toFixed(0)+' · 운영 '+sc.OperationalNeed.toFixed(0)+' · 가능성 '+sc.Feasibility.toFixed(0)+' · 난이도 '+sc.Difficulty.toFixed(0)+(sc.ExpectedChangeScope?' · 범위 '+esc(sc.ExpectedChangeScope):'')+'</div></div><div style="display:flex;gap:6px;flex-shrink:0"><button onclick="decideWork(\''+esc(p.ID)+'\',\''+esc(w.ID)+'\',\'APPROVED\',\'아이디어 승인\')">승인</button>'+(w.Status==='BACKLOG'?'<button onclick="decideWork(\''+esc(p.ID)+'\',\''+esc(w.ID)+'\',\'BLOCKED\',\'아이디어 보류\')">보류</button>':'')+'<button onclick="decideWork(\''+esc(p.ID)+'\',\''+esc(w.ID)+'\',\'DISCARDED\',\'아이디어 폐기\')">폐기</button></div></div></div>'});html+='</section>'}
return html+decisionsPanel()}
var planQuery='',planStatus='';
function filterStatus(st){planStatus=st;location.hash='#/project/'+encodeURIComponent(detailCache.project.ID)+'/tab/plan';if(document.querySelector('#worklist'))loadPlan()}
async function loadPlan(){var box=document.querySelector('#worklist');if(!box)return;var qEl=document.querySelector('#wq'),sEl=document.querySelector('#wstatus');planQuery=qEl?qEl.value:planQuery;planStatus=sEl?sEl.value:planStatus;
var id=detailCache.project.ID;var url='/api/v1/projects/'+encodeURIComponent(id)+'/work?q='+encodeURIComponent(planQuery)+'&status='+encodeURIComponent(planStatus);
try{var data=await api(url);var items=data.work_items||[];document.querySelector('#wcount').textContent=items.length+'건';
if(!items.length){box.innerHTML='조건에 맞는 작업이 없습니다.';return}
var html='<table><tr><th>작업</th><th>상태</th><th>우선순위</th><th>가중치</th><th>예상 토큰</th><th>변경 범위</th></tr>';
items.forEach(function(w){html+='<tr><td>'+workLink(id,w)+'</td><td><span class="badge">'+esc(workLabel(w.Status))+'</span></td><td>'+(w.Priority||0)+'</td><td>'+(w.Weight||1)+'</td><td>'+(w.EstimatedTokens?fmtTokens(w.EstimatedTokens):'<span class="sub">추정</span>')+'</td><td class="sub mono">'+esc(w.ChangeScope||'-')+'</td></tr>'});
box.innerHTML=html+'</table>'}catch(e){box.innerHTML='<span class="error">'+esc(e.message)+'</span>'}}
function runsTab(d){var p=d.project;var html='';
if(d.runs&&d.runs.length){html+='<section class="panel"><h2>최근 실행</h2><table><tr><th>실행</th><th>유형</th><th>작업</th><th>토큰</th><th>비용</th><th>상태</th></tr>';d.runs.forEach(function(r){html+='<tr><td class="mono"><a class="plain" href="#/project/'+encodeURIComponent(p.ID)+'/run/'+encodeURIComponent(r.ID)+'">'+esc(r.ID)+'</a></td><td>'+esc(r.TaskType||'-')+'</td><td class="mono">'+esc(r.WorkItemID||'-')+'</td><td>'+fmtTokens(r.Tokens)+'</td><td>'+fmtUSD(r.CostUSD)+'</td><td>'+stateChip(r.State)+'</td></tr>'});html+='</table></section>'}
else html+='<section class="panel"><h2>최근 실행</h2><div class="sub">기록된 실행이 없습니다.</div></section>';
var timeline=[];(d.quota_windows||[]).forEach(function(q){if(q.QuotaResetAt)timeline.push({t:q.QuotaResetAt,label:'한도 리셋 ('+q.LimitType+')',detail:esc(q.Source)+' · 신뢰도 '+esc(q.Confidence)});if(q.ResumeAt)timeline.push({t:q.ResumeAt,label:'재개 예정 ('+q.LimitType+')',detail:'안전 지연 포함'})});(d.scheduler_jobs||[]).forEach(function(j){timeline.push({t:j.RunAt,label:j.Type+' 잡 · '+j.Status,detail:(j.Attempts?'시도 '+j.Attempts+'회':'')+(j.LastError?' · '+esc(j.LastError):'')})});timeline.sort(function(a,b){return new Date(a.t)-new Date(b.t)});
if(timeline.length){html+='<section class="panel"><h2>한도·스케줄 타임라인</h2><table>';timeline.forEach(function(e){var future=new Date(e.t).getTime()>Date.now();html+='<tr><td class="mono" style="width:130px;color:'+(future?'var(--warn)':'var(--muted)')+'">'+(future?'▸ ':'')+fmtTime(e.t)+'</td><td>'+e.label+'</td><td class="sub">'+e.detail+'</td></tr>'});html+='</table></section>'}
if(d.sessions&&d.sessions.length){html+='<section class="panel"><h2>세션</h2><table><tr><th>세션</th><th>상태</th><th>컨텍스트 토큰</th><th>사유</th></tr>';d.sessions.forEach(function(s){html+='<tr><td class="mono">'+esc(s.SessionID)+'</td><td>'+esc(s.Status)+'</td><td>'+fmtTokens(s.ContextTokensUsed)+'</td><td class="sub">'+esc(s.ReplacementReason||'-')+'</td></tr>'});html+='</table></section>'}
return html}
function verifyTab(d){var html=criteriaPanel(d);
var gated=(d.runs||[]).filter(function(r){return r.State==='REPAIR_REQUIRED'||r.State==='FAILED'});
if(gated.length){html+='<section class="panel"><h2>검증 실패 실행 · '+gated.length+'건</h2><table>';gated.forEach(function(r){html+='<tr><td class="mono"><a class="plain" href="#/project/'+encodeURIComponent(d.project.ID)+'/run/'+encodeURIComponent(r.ID)+'">'+esc(r.ID)+'</a></td><td>'+esc(r.WorkItemID||'-')+'</td><td>'+stateChip(r.State)+'</td><td class="sub">'+fmtTime(r.StartedAt)+'</td></tr>'});html+='</table></section>'}
html+='<section class="panel"><h2>게이트 설정</h2><div class="sub">게이트는 CLI 로 관리합니다: <code>goalforge verify gate add --type coverage --command-json \'["go","test","-cover","./..."]\' --success-value 85 --value-pattern \'([0-9.]+)%\'</code><br>수치 조건은 <code>--value-pattern</code> 이 있어야 실측값으로 판정됩니다.</div></section>';
return html}
function costTab(d){var m=d.metrics;var html='<div class="metrics">';
var tokensUsed=m.InputTokens+m.OutputTokens+m.CachedInputTokens+m.ReasoningTokens;
if(d.budget&&d.budget.TokenLimit>0)html+=gauge('토큰 예산',fmtTokens(d.budget.TokensUsed),fmtTokens(d.budget.TokenLimit),d.budget.TokensUsed/d.budget.TokenLimit*100);else html+='<div class="metric"><small>사용 토큰</small><b>'+fmtTokens(tokensUsed)+'</b><small>예산 미설정</small></div>';
if(d.budget&&d.budget.CostLimitUSD>0)html+=gauge('비용 예산',fmtUSD(d.budget.CostUsedUSD),'$'+d.budget.CostLimitUSD.toFixed(0),d.budget.CostUsedUSD/d.budget.CostLimitUSD*100);else html+='<div class="metric"><small>누적 비용</small><b>'+fmtUSD(m.CostUSD)+'</b><small>한도 미설정</small></div>';
(d.quota_windows||[]).forEach(function(q){html+=gauge('계정 한도 ('+esc(q.LimitType)+')',q.UsedPercent.toFixed(0)+'%','',q.UsedPercent,esc(q.Status)+' · '+esc(q.Confidence))});
html+='</div>';
if(d.daily_usage)html+='<section class="panel"><h2>오늘 사용량</h2><div class="sub">실행 '+d.daily_usage.Runs+'회 · '+fmtTokens(d.daily_usage.Tokens)+' 토큰 · '+fmtUSD(d.daily_usage.CostUSD)+'</div></section>';
if(d.usage_series&&d.usage_series.length)html+='<section class="panel"><h2>일별 사용량 (최근 14일)</h2>'+usageChart(d.usage_series)+'</section>';
html+='<section class="panel"><h2>예상 비용</h2><div class="sub">'+estimateNote(d)+'</div></section>';
return html}
async function renderDetail(id,tab){var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';
var d=await api('/api/v1/projects/'+encodeURIComponent(id));detailCache=d;var p=d.project;
status.className='sub';status.textContent='';
document.querySelector('#crumb').innerHTML='<a class="sub" href="#">프로젝트</a> / '+esc(p.Name);
tab=tab||'overview';var live=null;var body='';
if(tab==='plan')body=planTab(d);
else if(tab==='runs')body=runsTab(d);
else if(tab==='verify')body=verifyTab(d);
else if(tab==='cost')body=costTab(d);
else{var o=overviewTab(d);body=o.html;live=o.live}
view.innerHTML=goalHead(d,tab)+body;countdown();
if(tab==='plan'){loadPlan();loadDecisions()}
if(live)startLive(p.ID,live)}
// renderWork is the work item as an executable specification rather than a
// title: why it exists, what "done" means, what blocks it, and every attempt.
// The setup flow follows the order the decisions actually happen in:
// repository → environment diagnosis → goal → completion criteria → execution
// policy. Each step is only reachable once the previous one holds, so a
// project cannot be created against a directory that is not a repository.
var setup={step:1,project:null,criteria:[{type:'build_passed',value:'true'}]};
function setupCriteriaHTML(){var html='';setup.criteria.forEach(function(c,i){html+='<div style="display:flex;gap:6px;margin-bottom:6px"><input value="'+esc(c.type)+'" placeholder="조건 이름 (예: build_passed)" oninput="setup.criteria['+i+'].type=this.value" style="flex:2"><input value="'+esc(c.value)+'" placeholder="기준값" oninput="setup.criteria['+i+'].value=this.value" style="flex:1"><button onclick="setup.criteria.splice('+i+',1);renderSetup()">삭제</button></div>'});
return html+'<button onclick="setup.criteria.push({type:\'\',value:\'\'});renderSetup()">조건 추가</button>'}
async function renderSetup(){var status=document.querySelector('#status'),view=document.querySelector('#view');status.className='sub';status.textContent='';
document.querySelector('#crumb').innerHTML='<a class="sub" href="#">프로젝트</a> / 새 프로젝트';
var steps=['저장소','환경 진단','목표','완료 조건','실행 정책'];
var html='<div class="tabs">';steps.forEach(function(name,i){html+='<a class="'+(setup.step===i+1?'on':'')+'" href="#/new">'+(i+1)+'. '+name+'</a>'});html+='</div>';
if(setup.step===1){html+='<section class="panel"><h2>1. 저장소 선택</h2>'+
'<label for="s-repo">저장소 경로</label><input id="s-repo" style="width:100%" placeholder="/path/to/repo" value="'+esc(setup.repo||'')+'">'+
'<label for="s-name">프로젝트 이름</label><input id="s-name" style="width:100%" value="'+esc(setup.name||'')+'">'+
'<div class="grid2"><div><label for="s-provider">제공자</label><select id="s-provider" style="width:100%">'+['codex','claude','qwen','opencode'].map(function(pv){return'<option value="'+pv+'"'+(setup.provider===pv?' selected':'')+'>'+pv+'</option>'}).join('')+'</select></div>'+
'<div><label for="s-model">모델 (선택)</label><input id="s-model" style="width:100%" value="'+esc(setup.model||'')+'"></div></div>'+
'<div class="actions"><button onclick="setupDiagnose()">환경 진단하기</button></div>'+
'<div class="why">GoalForge 는 저장소를 직접 수정하지 않습니다. 작업은 격리된 worktree 에서 실행되고, 기본 브랜치 반영은 승인이 필요합니다.</div></section>'}
if(setup.step===2){html+='<section class="panel"><h2>2. 환경 진단</h2><div id="doctor">진단 중…</div>'+
'<div class="actions"><button onclick="setup.step=1;renderSetup()">이전</button><button id="s-next2" onclick="setup.step=3;renderSetup()" disabled>목표 작성으로</button></div></section>'}
if(setup.step===3){html+='<section class="panel"><h2>3. 목표</h2>'+
'<label for="s-title">목표 제목</label><input id="s-title" style="width:100%" value="'+esc(setup.title||'')+'">'+
'<label for="s-objective">무엇을 달성하는가</label><textarea id="s-objective">'+esc(setup.objective||'')+'</textarea>'+
'<div class="actions"><button onclick="setup.step=2;renderSetup()">이전</button><button onclick="setupCaptureGoal()">완료 조건으로</button></div></section>'}
if(setup.step===4){html+='<section class="panel"><h2>4. 완료 조건</h2><div class="why">완료 조건이 없으면 목표는 완료로 판정되지 않습니다. 각 조건은 같은 이름의 검증 게이트가 만든 증거로 판정됩니다.</div>'+setupCriteriaHTML()+
'<div class="actions"><button onclick="setup.step=3;renderSetup()">이전</button><button onclick="setupCreate()">프로젝트 만들기</button></div></section>'}
if(setup.step===5){var p=setup.project;html+='<section class="panel"><h2>5. 실행 정책</h2><div class="sub">'+esc(p.Name)+' 이(가) 생성되었습니다. 예산은 모든 실행의 상한이며 비워 두면 제한 없이 실행됩니다.</div>'+
'<div class="grid2"><div><label for="s-tokens">토큰 예산</label><input id="s-tokens" type="number" min="0" value="2000000" style="width:100%">'+
'<label for="s-cost">비용 예산 (USD)</label><input id="s-cost" type="number" min="0" step="1" value="50" style="width:100%"></div>'+
'<div><label for="s-dailyruns">하루 최대 실행</label><input id="s-dailyruns" type="number" min="0" value="20" style="width:100%">'+
'<label for="s-turn">턴 / 실행 제한 시간</label><div style="display:flex;gap:6px"><input id="s-turn" value="30m" style="width:50%"><input id="s-run" value="2h" style="width:50%"></div></div></div>'+
'<div class="actions"><button onclick="setupPolicy()">저장하고 프로젝트 열기</button><button onclick="location.hash=\'#/project/\'+encodeURIComponent(setup.project.ID)">건너뛰기</button></div>'+
'<div class="why">다음 단계: 검증 게이트를 등록해야 실행이 검증될 수 있습니다 — <code>goalforge verify gate add --type build_passed --command-json \'["go","build","./..."]\'</code></div></section>'}
view.innerHTML=html;
if(setup.step===2)runDoctor()}
function setupField(id){var el=document.querySelector(id);return el?el.value.trim():''}
async function setupDiagnose(){setup.repo=setupField('#s-repo');setup.name=setupField('#s-name');setup.provider=setupField('#s-provider');setup.model=setupField('#s-model');
if(!setup.repo||!setup.name){alert('저장소 경로와 프로젝트 이름이 필요합니다');return}
setup.step=2;renderSetup()}
async function runDoctor(){var box=document.querySelector('#doctor');if(!box)return;
try{var r=await api('/api/v1/doctor?repo='+encodeURIComponent(setup.repo)+'&provider='+encodeURIComponent(setup.provider));
var html='<table>';(r.Checks||[]).forEach(function(c){var cls=c.Level==='OK'?'st-ok':(c.Level==='FAIL'?'st-bad':'st-warn');html+='<tr><td style="width:80px"><span class="state '+cls+'">'+esc(c.Level)+'</span></td><td>'+esc(c.Name)+'</td><td class="sub">'+esc(c.Detail)+'</td></tr>'});
box.innerHTML=html+'</table>'+(r.Failed?'<div class="why">차단 문제 '+r.Failed+'건을 먼저 해결해야 합니다.</div>':'<div class="why">차단 문제가 없습니다.</div>');
var next=document.querySelector('#s-next2');if(next)next.disabled=r.Failed>0}catch(e){box.innerHTML='<span class="error">'+esc(e.message)+'</span>'}}
function setupCaptureGoal(){setup.title=setupField('#s-title');setup.objective=setupField('#s-objective');
if(!setup.title||!setup.objective){alert('목표 제목과 내용이 필요합니다');return}
setup.step=4;renderSetup()}
async function setupCreate(){var criteria=setup.criteria.filter(function(c){return c.type&&c.value});
if(!criteria.length){alert('완료 조건이 최소 1개 필요합니다');return}
try{var created=await api('/api/v1/projects',{method:'POST',body:JSON.stringify({name:setup.name,repository_path:setup.repo,provider:setup.provider,model:setup.model,worktrees:true,auto_commit:true})});
setup.project=created.project;
await api('/api/v1/projects/'+encodeURIComponent(setup.project.ID)+'/goal',{method:'POST',body:JSON.stringify({title:setup.title,objective:setup.objective,criteria:criteria.map(function(c){return{type:c.type,expected_value:c.value}})})});
setup.step=5;renderSetup()}catch(e){alert(e.message)}}
async function setupPolicy(){try{await api('/api/v1/projects/'+encodeURIComponent(setup.project.ID)+'/policy',{method:'POST',body:JSON.stringify({token_limit:parseInt(setupField('#s-tokens'),10)||0,cost_limit_usd:parseFloat(setupField('#s-cost'))||0,daily_run_limit:parseInt(setupField('#s-dailyruns'),10)||0,turn_timeout:setupField('#s-turn'),run_timeout:setupField('#s-run')})});
location.hash='#/project/'+encodeURIComponent(setup.project.ID)}catch(e){alert(e.message)}}
async function renderWork(projectID,workID){var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';
var d=await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/work/'+encodeURIComponent(workID));var w=d.item;
status.className='sub';status.textContent='';
document.querySelector('#crumb').innerHTML='<a class="sub" href="#">프로젝트</a> / <a class="sub" href="#/project/'+encodeURIComponent(projectID)+'/tab/plan">백로그</a> / '+esc(w.ID);
var html='<section class="panel"><div class="row"><div><strong style="font-size:18px">'+esc(w.Title)+'</strong><div class="sub mono">'+esc(w.ID)+' · '+esc(w.Type)+'</div></div><div style="text-align:right"><span class="badge">'+esc(workLabel(w.Status))+' <span class="mono">'+esc(w.Status)+'</span></span></div></div>';
if(d.blockers&&d.blockers.length){html+='<div style="margin-top:12px">';d.blockers.forEach(function(b){var terminal=b.Kind==='STATUS';html+='<div class="attn'+(terminal?' warn':'')+'"><strong>'+esc(blockerLabel(b.Kind))+'</strong><div class="sub" style="margin-top:2px">'+esc(b.Detail)+'</div></div>'});html+='</div>'}
else html+='<div class="why">지금 실행할 수 있는 상태입니다.</div>';
html+='</section>';
html+='<section class="panel"><h2>작업 명세</h2><div class="grid2">';
html+='<div><label for="w-objective">목적 — 왜 이 작업이 필요한가</label><textarea id="w-objective">'+esc(w.Objective||'')+'</textarea>';
html+='<label for="w-acceptance">완료 기준 — 무엇이 참이어야 끝인가</label><textarea id="w-acceptance">'+esc(w.Acceptance||'')+'</textarea></div>';
html+='<div><label for="w-scope">변경 범위 (glob)</label><input id="w-scope" value="'+esc(w.ChangeScope||'')+'" style="width:100%">';
html+='<label for="w-dep">선행 작업 ID (쉼표로 구분)</label><input id="w-dep" value="'+esc((w.Dependencies||[]).join(', '))+'" style="width:100%">';
html+='<label for="w-risk">위험도</label><select id="w-risk"><option value="low"'+(w.Risk==='low'?' selected':'')+'>low</option><option value="medium"'+(w.Risk==='medium'?' selected':'')+'>medium</option><option value="high"'+(w.Risk==='high'?' selected':'')+'>high</option></select>';
html+='<label for="w-priority">우선순위 / 가중치 / 예상 토큰</label><div style="display:flex;gap:6px"><input id="w-priority" type="number" step="1" value="'+(w.Priority||0)+'" style="width:33%"><input id="w-weight" type="number" step="0.5" min="0.5" value="'+(w.Weight||1)+'" style="width:33%"><input id="w-tokens" type="number" step="1000" min="0" value="'+(w.EstimatedTokens||0)+'" style="width:33%"></div>';
html+='<div class="why">'+estimateSourceNote(d)+'</div></div></div>';
html+='<div class="actions"><button onclick="saveWorkPlan(\''+esc(projectID)+'\',\''+esc(workID)+'\')">명세 저장</button>';
if(w.Status==='BACKLOG')html+='<button onclick="decideWork(\''+esc(projectID)+'\',\''+esc(workID)+'\',\'APPROVED\',\'작업 승인\')">승인</button>';
if(w.Status==='BACKLOG'||w.Status==='APPROVED')html+='<button onclick="decideWork(\''+esc(projectID)+'\',\''+esc(workID)+'\',\'BLOCKED\',\'작업 보류\')">보류</button>';
if(w.Status!=='DONE'&&w.Status!=='DISCARDED')html+='<button onclick="decideWork(\''+esc(projectID)+'\',\''+esc(workID)+'\',\'DISCARDED\',\'작업 폐기 — 목표 기준선에서 제외됩니다\')">폐기</button>';
html+='</div><div class="why">명세 저장은 상태를 바꾸지 않습니다. 상태 변경은 위의 승인·보류·폐기 버튼으로만 이루어집니다.</div></section>';
if((d.dependencies||[]).length){html+='<section class="panel"><h2>선행 작업 '+d.dependencies.length+'건</h2><table>';d.dependencies.forEach(function(dep){html+='<tr><td>'+workLink(projectID,dep)+'</td><td><span class="badge'+(dep.Status==='DONE'?' met':' unmet')+'">'+esc(workLabel(dep.Status))+'</span></td></tr>'});html+='</table></section>'}
html+=modelNote(d);
if(d.score){var sc=d.score;html+='<section class="panel"><h2>선정 점수</h2><div class="sub">우선순위 점수 '+sc.PriorityScore.toFixed(1)+' · 목표 기여 '+sc.GoalContribution.toFixed(0)+' · 사용자 가치 '+sc.UserValue.toFixed(0)+' · 운영 필요 '+sc.OperationalNeed.toFixed(0)+' · 실현 가능성 '+sc.Feasibility.toFixed(0)+' · 위험 감소 '+sc.RiskReduction.toFixed(0)+' · 난이도 '+sc.Difficulty.toFixed(0)+(sc.ScopeExpansion?' · 범위 확장 제안':'')+'</div></section>'}
if(d.commit)html+='<section class="panel"><h2>검증된 커밋</h2><div class="sub mono">'+shortSHA(d.commit.CommitSHA)+' · '+esc(d.commit.Branch)+' · '+d.commit.FilesCommitted+'개 파일</div><div class="actions"><a class="plain" href="#/project/'+encodeURIComponent(projectID)+'/run/'+encodeURIComponent(d.commit.RunID)+'">변경 검토 →</a></div></section>';
if(d.runs&&d.runs.length){html+='<section class="panel"><h2>실행 이력 · '+d.runs.length+'회</h2><table><tr><th>실행</th><th>유형</th><th>토큰</th><th>비용</th><th>상태</th><th>시각</th></tr>';d.runs.forEach(function(r){html+='<tr><td class="mono"><a class="plain" href="#/project/'+encodeURIComponent(projectID)+'/run/'+encodeURIComponent(r.ID)+'">'+esc(r.ID)+'</a></td><td>'+esc(r.TaskType||'-')+'</td><td>'+fmtTokens(r.Tokens)+'</td><td>'+fmtUSD(r.CostUSD)+'</td><td>'+stateChip(r.State)+'</td><td class="sub">'+fmtTime(r.StartedAt)+'</td></tr>'});html+='</table></section>'}
else html+='<section class="panel"><h2>실행 이력</h2><div class="sub">아직 실행된 적이 없습니다.</div></section>';
view.innerHTML=html}
function blockerLabel(kind){var m={DEPENDENCY:'선행 작업 미완료',WIP_LIMIT:'동시 구현 제한',APPROVAL:'승인 필요',STATUS:'현재 상태'};return m[kind]||kind}
function confidenceLabel(c){var m={high:'높음',medium:'보통',low:'낮음',none:'없음'};return m[c]||c}
function estimateSourceNote(d){var f=d.forecast||{};
var forecast=f.Samples?'실행 기록 예측 '+fmtTokens(f.Expected)+' (범위 '+fmtTokens(f.Low)+'~'+fmtTokens(f.High)+', 표본 '+f.Samples+'건, 신뢰도 '+confidenceLabel(f.Confidence)+')':'예측할 실행 기록이 없습니다';
if(d.estimate_source==='manual')return'예상 토큰은 직접 입력된 값입니다. '+forecast+'.';
return'예상 토큰이 0 이라 기록으로 추정합니다: '+forecast+'.'}
function modelNote(d){var m=d.model||{};if(!m.Reason)return'';
return'<section class="panel"><h2>실행 모델</h2><div><strong>'+esc(m.Model||'제공자 기본값')+'</strong> <span class="badge">'+esc(m.Source==='history'?'기록 기반 선택':'설정값')+'</span></div><div class="sub" style="margin-top:4px">'+esc(m.Reason)+'</div>'+
((m.Considered||[]).length?'<table style="margin-top:8px"><tr><th>모델</th><th>실행</th><th>검증 통과율</th><th>평균 비용</th><th>평균 소요</th></tr>'+m.Considered.map(function(c){return'<tr><td class="mono">'+esc(c.Model)+'</td><td>'+c.Runs+'</td><td>'+c.SuccessRate.toFixed(0)+'%</td><td>'+fmtUSD(c.AverageCostPerRun)+'</td><td>'+c.AverageSeconds.toFixed(0)+'초</td></tr>'}).join('')+'</table>':'')+'</section>'}
async function saveWorkPlan(projectID,workID){var body={objective:document.querySelector('#w-objective').value,acceptance:document.querySelector('#w-acceptance').value,change_scope:document.querySelector('#w-scope').value,dependencies:document.querySelector('#w-dep').value.split(',').map(function(v){return v.trim()}).filter(Boolean),risk:document.querySelector('#w-risk').value,priority:parseFloat(document.querySelector('#w-priority').value)||0,weight:parseFloat(document.querySelector('#w-weight').value)||1,estimated_tokens:parseInt(document.querySelector('#w-tokens').value,10)||0};
try{await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/work/'+encodeURIComponent(workID)+'/plan',{method:'POST',body:JSON.stringify(body)});route()}catch(e){alert(e.message)}}
// renderApproval is the reviewed decision: the exact commit, the files, the
// gates that decided it, the destination, and the way back.
async function renderApproval(projectID,approvalID){var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';
var d=await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/approvals/'+encodeURIComponent(approvalID));var a=d.approval,sc=a.Scope||{};
status.className='sub';status.textContent='';
document.querySelector('#crumb').innerHTML='<a class="sub" href="#">프로젝트</a> / <a class="sub" href="#/project/'+encodeURIComponent(projectID)+'">'+esc(projectID)+'</a> / 승인 '+esc(a.ID);
var html='<section class="panel"><div class="row"><div><strong style="font-size:18px">'+esc(a.ActionType)+'</strong><div class="sub">'+esc(a.Reason)+'</div></div><span class="badge">'+esc(d.status)+'</span></div>';
if(d.stale)html+='<div class="attn" style="margin-top:12px"><strong>승인 범위를 벗어난 변경</strong><div class="sub" style="margin-top:2px">'+esc(d.stale_reason)+'</div></div>';
html+='<table style="margin-top:12px"><tr><th style="width:110px">대상 작업</th><td>'+(sc.WorkItemID?'<a class="plain mono" href="#/project/'+encodeURIComponent(projectID)+'/work/'+encodeURIComponent(sc.WorkItemID)+'">'+esc(sc.WorkItemID)+'</a>':'<span class="sub">지정되지 않음</span>')+'</td></tr>';
html+='<tr><th>대상 커밋</th><td class="mono">'+(sc.CommitSHA?shortSHA(sc.CommitSHA):'<span class="sub">지정되지 않음</span>')+'</td></tr>';
html+='<tr><th>출처 브랜치</th><td class="mono">'+esc(sc.SourceBranch||'-')+'</td></tr>';
html+='<tr><th>적용 대상</th><td class="mono">'+esc(sc.TargetRef||'-')+'</td></tr>';
html+='<tr><th>변경 파일</th><td>'+(sc.FilesChanged||0)+'개</td></tr>';
html+='<tr><th>요청 시각</th><td class="sub">'+fmtTime(a.RequestedAt)+'</td></tr></table>';
html+='<div class="why">'+esc(d.rollback)+'</div>';
if(d.status==='PENDING')html+='<div class="actions"><button onclick="decide(\''+esc(projectID)+'\',\''+esc(a.ID)+'\',\'approve\',\'승인\')">승인</button><button onclick="decide(\''+esc(projectID)+'\',\''+esc(a.ID)+'\',\'reject\',\'반려\')">반려</button></div>';
html+='</section>';
html+=verificationPanel(d.verifications,'승인 근거가 되는 검증');
if(d.file_changes&&d.file_changes.length){html+='<section class="panel"><h2>영향 파일 · '+d.file_changes.length+'건</h2><table>';d.file_changes.forEach(function(f){html+='<tr><td class="mono">'+esc(f.Path)+'</td><td><span class="pill">'+esc(f.ChangeType)+'</span></td></tr>'});html+='</table></section>'}
html+=diffPanel(d);
view.innerHTML=html}
// verificationPanel shows every gate with its full output available, because a
// truncated line is not enough to judge a failure.
function verificationPanel(results,title){if(!results||!results.length)return'<section class="panel"><h2>'+title+'</h2><div class="sub">기록된 검증 결과가 없습니다.</div></section>';
var html='<section class="panel"><h2>'+title+'</h2><table><tr><th>게이트</th><th>상태</th><th>측정값</th><th>종료 코드</th><th>소요</th></tr>';
results.forEach(function(v,i){var ok=v.Status==='PASSED';
html+='<tr><td>'+esc(v.CheckType)+(v.Required?'':' <span class="pill">선택</span>')+'</td><td><span class="state '+(ok?'st-ok':'st-bad')+'">'+esc(v.Status)+'</span>'+(v.FailureKind?' <span class="badge unmet">'+esc(failureLabel(v.FailureKind))+'</span>':'')+'</td><td class="mono">'+esc(v.ActualValue||'-')+'</td><td>'+v.ExitCode+'</td><td class="sub">'+((v.Duration||0)/1e9).toFixed(1)+'초</td></tr>';
html+='<tr><td colspan="5"><details'+(ok?'':' open')+'><summary class="sub" style="cursor:pointer">출력 전체 보기</summary><pre class="diff" style="max-height:260px;white-space:pre-wrap">'+esc(v.Output||'(출력 없음)')+'</pre></details></td></tr>'});
return html+'</table></section>'}
function diffPanel(d){if(d.diff_error)return'<section class="panel"><h2>코드 차이</h2><div class="sub">차이를 읽을 수 없습니다: '+esc(d.diff_error)+'</div></section>';
if(!d.diff)return'';
return'<section class="panel"><h2>코드 차이'+(d.diff_truncated?' <span class="pill">일부만 표시</span>':'')+'</h2>'+diffHTML(d.diff)+'</section>'}
// renderRun is the change review: what changed, why, whether it verified, and
// what it contributes to the goal — side by side instead of across a terminal
// and a git client.
async function renderRun(projectID,runID){var status=document.querySelector('#status'),view=document.querySelector('#view');status.textContent='불러오는 중…';view.innerHTML='';var d=await api('/api/v1/projects/'+encodeURIComponent(projectID)+'/runs/'+encodeURIComponent(runID));status.className='sub';status.textContent='';var r=d.run;
document.querySelector('#crumb').innerHTML='<a class="sub" href="#">프로젝트</a> / <a class="sub" href="#/project/'+encodeURIComponent(projectID)+'">'+esc(projectID)+'</a> / '+esc(runID);
var duration='';if(r.StartedAt&&r.EndedAt&&new Date(r.EndedAt).getFullYear()>1){duration=((new Date(r.EndedAt)-new Date(r.StartedAt))/1000).toFixed(1)+'초'}
var html='<section class="panel"><div class="row"><div><strong style="font-size:17px" class="mono">'+esc(r.ID)+'</strong><div class="sub">'+esc(r.TaskType||'유형 미기록')+(r.WorkItemID?' · ':'')+(r.WorkItemID?'<a class="plain mono" href="#/project/'+encodeURIComponent(projectID)+'/work/'+encodeURIComponent(r.WorkItemID)+'">'+esc(r.WorkItemID)+'</a>':'')+(duration?' · '+duration:'')+' · '+fmtTime(r.StartedAt)+'</div></div>'+stateChip(r.State)+'</div>';
var verdict=verdictOf(d);if(verdict)html+='<div class="attn'+(verdict.ok?' warn':'')+'" style="margin-top:12px;'+(verdict.ok?'background:#14301f;border-color:#215a3d':'')+'"><strong>'+esc(verdict.title)+'</strong><div class="sub" style="margin-top:2px">'+esc(verdict.detail)+'</div></div>';
html+='</section>';
html+='<div class="metrics"><div class="metric"><small>입력 토큰</small><b>'+fmtTokens(d.usage.InputTokens)+'</b></div><div class="metric"><small>출력 토큰</small><b>'+fmtTokens(d.usage.OutputTokens)+'</b></div><div class="metric"><small>캐시 읽기</small><b>'+fmtTokens(d.usage.CachedInputTokens)+'</b></div><div class="metric"><small>비용</small><b>'+fmtUSD(d.usage.CostUSD)+'</b></div></div>';
if(d.commit)html+='<section class="panel"><h2>검증 커밋</h2><div class="sub mono">'+shortSHA(d.commit.CommitSHA)+' · '+esc(d.commit.Branch)+' · '+d.commit.FilesCommitted+'개 파일</div><div class="why">머지·푸시는 이 커밋을 대상으로 승인해야 합니다: <code>goalforge approval request --action merge-branch --work-item '+esc(d.commit.WorkItemID)+' --reason "..."</code></div></section>';
html+=verificationPanel(d.verifications,'검증 결과');
if(d.file_changes&&d.file_changes.length){html+='<section class="panel"><h2>파일 변경 · '+d.file_changes.length+'건</h2><table>';d.file_changes.forEach(function(f){html+='<tr><td class="mono">'+esc(f.Path)+'</td><td><span class="pill">'+esc(f.ChangeType)+'</span></td></tr>'});html+='</table></section>'}
html+=diffPanel(d);
if(d.prompt){html+='<section class="panel"><h2>변경 이유 · 프롬프트 '+esc(d.prompt.Template)+'</h2><div class="sub mono" style="font-size:11px;margin-bottom:8px">sha256 '+esc(d.prompt.RenderedHash)+'</div><pre class="diff" style="white-space:pre-wrap;max-height:300px">'+esc(d.prompt.RedactedPrompt)+'</pre></section>'}
if(d.turns&&d.turns.length){html+='<section class="panel"><h2>Turns</h2><table>';d.turns.forEach(function(t){html+='<tr><td class="mono">'+esc(t.ProviderTurnID)+'</td><td>'+stateChip(t.Status==='COMPLETED'?'COMPLETED':t.Status==='FAILED'?'FAILED':'RUNNING')+'</td></tr>'});html+='</table></section>'}
if(d.events&&d.events.length){html+='<section class="panel"><h2>이벤트 로그 · '+d.events.length+'건</h2><div style="max-height:320px;overflow:auto"><table>';d.events.forEach(function(e){html+='<tr><td style="width:110px"><span class="pill">'+esc(e.Type)+'</span></td><td class="sub mono" style="font-size:11px;word-break:break-all">'+esc(e.Raw.length>300?e.Raw.slice(0,300)+'…':e.Raw)+'</td></tr>'});html+='</table></div></section>'}
view.innerHTML=html}
// verdictOf states the run's outcome in one sentence, including which required
// gate decided it, so a reviewer does not have to read the table to find out.
function repairLabel(decision){var m={RETRY_CODE_FIX:'자동 수정 재시도 예정',BLOCK_ENVIRONMENT:'환경 문제 — 자동 복구 불가',BLOCK_FOR_USER:'사람 판단 필요',BLOCK_ATTEMPT_LIMIT:'자동 수정 횟수 한도 도달',BLOCK_COST_LIMIT:'복구 비용 한도 도달',NOTHING_TO_REPAIR:'복구 대상 없음'};return m[decision]||decision}
function failureLabel(kind){var m={test_failure:'테스트 실패',build_failure:'빌드 실패',threshold_not_met:'기준 미달',environment:'실행 환경 문제',dependency:'의존성 문제',auth:'인증 문제',timeout:'제한 시간 초과',misconfigured:'게이트 설정 오류',unknown:'분류되지 않음'};return m[kind]||kind}
// verdictOf states the run outcome in one sentence, naming the gate that
// decided it and what the classifier concluded, so the next action is visible
// without reading the gate table.
function verdictOf(d){var results=d.verifications||[];if(!results.length)return null;
var failed=results.filter(function(v){return v.Required&&v.Status!=='PASSED'});
if(failed.length){var first=failed[0];var detail='종료 코드 '+first.ExitCode+(first.ActualValue?' · 측정값 '+first.ActualValue:'')+'.';
if(d.repair)detail+=' 원인 분류: '+failureLabel(d.repair.FailureKind)+' · '+repairLabel(d.repair.Decision)+'. '+d.repair.Reason+' '+(d.repair.Summary||'');
else detail+=' 작업은 백로그로 돌아갔고, 테스트나 기준을 완화하지 않고 원인을 수정해야 합니다.';
return{ok:false,title:'검증 실패 — 필수 게이트 '+first.CheckType,detail:detail}}
return{ok:true,title:'검증 통과 — 필수 게이트 '+results.filter(function(v){return v.Required}).length+'개',detail:d.commit?'변경은 작업 브랜치에 커밋되었습니다. 기본 브랜치 반영은 승인이 필요합니다.':'변경이 커밋되지 않았습니다 (auto-commit 미설정).'}}
async function route(){var status=document.querySelector('#status');if(timer){clearInterval(timer);timer=null}stopLive();
try{var h=location.hash;
if(h==='#/new'){await renderSetup();return}
var runMatch=h.match(/^#\/project\/([^\/]+)\/run\/(.+)$/);if(runMatch){await renderRun(decodeURIComponent(runMatch[1]),decodeURIComponent(runMatch[2]));return}
var workMatch=h.match(/^#\/project\/([^\/]+)\/work\/(.+)$/);if(workMatch){await renderWork(decodeURIComponent(workMatch[1]),decodeURIComponent(workMatch[2]));return}
var aprMatch=h.match(/^#\/project\/([^\/]+)\/approval\/(.+)$/);if(aprMatch){await renderApproval(decodeURIComponent(aprMatch[1]),decodeURIComponent(aprMatch[2]));return}
var tabMatch=h.match(/^#\/project\/([^\/]+)\/tab\/([a-z]+)$/);if(tabMatch){await renderDetail(decodeURIComponent(tabMatch[1]),tabMatch[2]);timer=setInterval(countdown,1000);return}
var match=h.match(/^#\/project\/(.+)$/);if(match){await renderDetail(decodeURIComponent(match[1]),'overview');timer=setInterval(countdown,1000)}
else{await renderList()}}catch(e){status.className='error';status.textContent=e.message}}
// Keyboard access: / jumps to the backlog search, Escape goes up one level, r
// reloads the current view.
document.addEventListener('keydown',function(e){if(e.target.tagName==='INPUT'||e.target.tagName==='TEXTAREA'||e.target.tagName==='SELECT'){if(e.key==='Escape')e.target.blur();return}
if(e.key==='/'){var box=document.querySelector('#wq');if(box){e.preventDefault();box.focus();box.select()}else if(detailCache){location.hash='#/project/'+encodeURIComponent(detailCache.project.ID)+'/tab/plan'}return}
if(e.key==='Escape'){var h=location.hash;var up=h.match(/^(#\/project\/[^\/]+)\/.+$/);location.hash=up?up[1]:'';return}
if(e.key==='r'){route()}});
window.addEventListener('hashchange',route);route();
</script></body></html>`
