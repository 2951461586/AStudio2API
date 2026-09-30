// Package web serves the single-file operator panel. It is deliberately
// dependency-free: one HTML document with inline CSS/JS that talks to
// /admin/api/*, so the gateway ships as a single binary with no asset pipeline.
package web

import (
	"html/template"
	"net/http"
)

// Panel renders the console.
type Panel struct {
	version string
	page    *template.Template
}

// NewPanel builds the panel for the given gateway version.
func NewPanel(version string) *Panel {
	return &Panel{
		version: version,
		page:    template.Must(template.New("panel").Parse(panelHTML)),
	}
}

// ServeHTTP renders the panel.
func (p *Panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = p.page.Execute(w, map[string]any{"Version": p.version})
}

const panelHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AStudio2API 控制台</title>
<style>
:root{
  --bg:#f6f7f9;--panel:#fff;--ink:#1f2328;--muted:#6b7280;--line:#e5e7eb;
  --accent:#2563eb;--accent-soft:#eff6ff;--ok:#059669;--warn:#d97706;--bad:#dc2626;
  --radius:10px;--shadow:0 1px 2px rgba(16,24,40,.06),0 1px 3px rgba(16,24,40,.1);
}
@media (prefers-color-scheme:dark){
  :root{--bg:#0f1115;--panel:#171a21;--ink:#e6e8eb;--muted:#9aa3af;--line:#272c36;
    --accent:#60a5fa;--accent-soft:#1b2433;--ok:#34d399;--warn:#fbbf24;--bad:#f87171;}
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
header{display:flex;align-items:center;gap:12px;padding:14px 22px;background:var(--panel);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:10}
header h1{font-size:15px;margin:0;font-weight:650;letter-spacing:.01em}
header .ver{font-size:12px;color:var(--muted);border:1px solid var(--line);padding:1px 7px;border-radius:999px}
header .spacer{flex:1}
main{max-width:1180px;margin:0 auto;padding:22px}
nav{display:flex;gap:4px;flex-wrap:wrap;margin-bottom:18px}
nav button{background:transparent;border:1px solid transparent;color:var(--muted);padding:6px 13px;border-radius:999px;cursor:pointer;font-size:13px}
nav button:hover{color:var(--ink)}
nav button.on{background:var(--accent-soft);color:var(--accent);border-color:color-mix(in srgb,var(--accent) 30%,transparent);font-weight:600}
section{display:none}
section.on{display:block}
.card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:18px;margin-bottom:16px;box-shadow:var(--shadow)}
.card h2{font-size:14px;margin:0 0 4px;font-weight:650}
.card p.hint{color:var(--muted);font-size:12.5px;margin:0 0 14px}
.grid{display:grid;gap:12px}
.grid.c4{grid-template-columns:repeat(auto-fit,minmax(170px,1fr))}
.grid.c2{grid-template-columns:repeat(auto-fit,minmax(280px,1fr))}
.metric{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:14px 16px}
.metric .k{color:var(--muted);font-size:12px;margin-bottom:6px}
.metric .v{font-size:22px;font-weight:660;font-variant-numeric:tabular-nums}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:9px 10px;border-bottom:1px solid var(--line);vertical-align:middle}
th{color:var(--muted);font-weight:600;font-size:12px;text-transform:none}
tbody tr:hover{background:color-mix(in srgb,var(--accent) 5%,transparent)}
code,pre{font-family:ui-monospace,SFMono-Regular,"Cascadia Code",Consolas,monospace;font-size:12.5px}
pre{background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:12px;overflow:auto;margin:0}
input,select,textarea{background:var(--bg);border:1px solid var(--line);color:var(--ink);border-radius:8px;padding:8px 10px;font:inherit;width:100%}
label{display:block;font-size:12px;color:var(--muted);margin:10px 0 4px}
button.btn{background:var(--accent);color:#fff;border:0;border-radius:8px;padding:8px 15px;cursor:pointer;font:inherit;font-weight:600}
button.btn:hover{filter:brightness(1.06)}
button.btn.ghost{background:transparent;color:var(--ink);border:1px solid var(--line)}
button.btn.danger{background:transparent;color:var(--bad);border:1px solid color-mix(in srgb,var(--bad) 40%,transparent)}
button.btn.sm{padding:4px 10px;font-size:12px;font-weight:500}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.pill{display:inline-block;padding:1px 8px;border-radius:999px;font-size:11.5px;border:1px solid var(--line)}
.pill.ok{color:var(--ok);border-color:color-mix(in srgb,var(--ok) 40%,transparent);background:color-mix(in srgb,var(--ok) 10%,transparent)}
.pill.bad{color:var(--bad);border-color:color-mix(in srgb,var(--bad) 40%,transparent);background:color-mix(in srgb,var(--bad) 10%,transparent)}
.pill.warn{color:var(--warn);border-color:color-mix(in srgb,var(--warn) 40%,transparent);background:color-mix(in srgb,var(--warn) 10%,transparent)}
.muted{color:var(--muted)}
.mono{font-family:ui-monospace,Consolas,monospace}
.right{text-align:right}
.toast{position:fixed;right:20px;bottom:20px;background:var(--panel);border:1px solid var(--line);border-left:3px solid var(--accent);border-radius:8px;padding:11px 15px;box-shadow:var(--shadow);max-width:420px;opacity:0;transform:translateY(8px);transition:.18s;pointer-events:none;z-index:50;font-size:13px}
.toast.on{opacity:1;transform:none}
.toast.err{border-left-color:var(--bad)}
.login{max-width:340px;margin:14vh auto;text-align:center}
.bar{height:8px;border-radius:999px;background:var(--accent);opacity:.75;min-width:2px}
</style>
</head>
<body>
<header>
  <h1>AStudio2API</h1><span class="ver">v{{.Version}}</span>
  <span class="spacer"></span>
  <span id="who" class="muted" style="font-size:12px"></span>
  <button class="btn ghost sm" id="logout" style="display:none">退出</button>
</header>

<div id="loginview" class="login">
  <div class="card">
    <h2>控制台登录</h2>
    <p class="hint">输入管理密码。默认密码为 <code>admin</code>，请尽快修改。</p>
    <input id="pwd" type="password" placeholder="密码" autocomplete="current-password">
    <div style="height:12px"></div>
    <button class="btn" id="login" style="width:100%">登录</button>
  </div>
</div>

<main id="appview" style="display:none">
<nav>
  <button data-tab="overview" class="on">总览</button>
  <button data-tab="accounts">账号</button>
  <button data-tab="benefits">权益</button>
  <button data-tab="keys">API 密钥</button>
  <button data-tab="models">模型</button>
  <button data-tab="logs">请求日志</button>
  <button data-tab="settings">设置</button>
</nav>

<section id="tab-overview" class="on">
  <div class="grid c4" id="metrics"></div>
  <div class="card" style="margin-top:16px">
    <h2>接入信息</h2>
    <p class="hint">把下面的 Base URL 与密钥填入任何 OpenAI 兼容客户端。</p>
    <pre id="quickstart"></pre>
  </div>
  <div class="card">
    <h2>近 24 小时</h2>
    <table><thead><tr><th>时段</th><th class="right">请求</th><th class="right">输入</th><th class="right">输出</th><th style="width:32%">占比</th></tr></thead>
    <tbody id="trend"></tbody></table>
  </div>
</section>

<section id="tab-accounts">
  <div class="card">
    <h2>手机号验证码登录</h2>
    <p class="hint">Docker / 无桌面端场景的推荐方式。安全验证（GeeTest）必须在本页浏览器内完成，网关不代解题。</p>
    <div class="grid c2">
      <div><label>手机号</label><input id="smsMobile" placeholder="11 位手机号" maxlength="11" inputmode="numeric"></div>
      <div><label>短信验证码</label>
        <div class="row">
          <input id="smsCode" placeholder="短信验证码" style="flex:1" inputmode="numeric">
          <button class="btn ghost" id="sendSmsBtn" style="flex:0 0 auto">发送验证码</button>
        </div>
      </div>
    </div>
    <div style="height:12px"></div>
    <button class="btn" id="smsLoginBtn">登录并添加账号</button>
    <div class="muted" id="smsHint" style="margin-top:10px;font-size:12px"></div>
  </div>
  <div class="card">
    <h2>导入本机 AStudio 登录态</h2>
    <p class="hint">留空即自动探测 AStudio 数据目录（<code>&lt;安装目录&gt;/../AStudio Data</code>）。导入后会立即换取模型凭据并刷新模型目录。</p>
    <label>AStudio 数据目录（可留空）</label>
    <input id="acctdir" placeholder="例如 F:\\IDE\\AStudio Data">
    <div style="height:12px"></div>
    <div class="row">
      <button class="btn" id="importBtn">一键导入</button>
      <button class="btn ghost" id="manualToggle">手动填写凭据</button>
    </div>
    <div id="manualBox" style="display:none;margin-top:12px">
      <div class="grid c2">
        <div><label>账号 ID (account_id)</label><input id="mAcctId" placeholder="如 12345678901"></div>
        <div><label>会话 token</label><input id="mToken" placeholder="登录会话 token"></div>
        <div><label>ssoSessionId</label><input id="mSso" placeholder="32 位会话 ID"></div>
        <div><label>模型凭据 Bearer（可留空）</label><input id="mBearer" placeholder="32位hex:base64"></div>
      </div>
      <div style="height:12px"></div>
      <button class="btn" id="manualSave">保存账号</button>
    </div>
  </div>
  <div class="card">
    <h2>账号池</h2>
    <p class="hint">多个账号按轮询负载，遇到 401/5xx 自动换号并重试。</p>
    <table><thead><tr><th>名称</th><th>UID</th><th>凭据</th><th class="right">积分/Spark</th><th>状态</th><th class="right">成功/失败</th><th class="right">操作</th></tr></thead>
    <tbody id="acctRows"></tbody></table>
  </div>
</section>

<section id="tab-benefits">
  <div class="card">
    <h2>权益总览</h2>
    <p class="hint">星辰的「签到」由运营弹窗下发（<code>DAILY_REWARD_DIALOG</code>），网关代为完成领取；同时刷新积分、会员与保活。</p>
    <div class="grid c4" id="benefitMetrics"></div>
    <div style="height:14px"></div>
    <div class="row">
      <button class="btn" id="checkinAll">一键签到（全部账号）</button>
      <button class="btn ghost" id="keepaliveAll">全部保活</button>
      <input id="redeemCode" placeholder="兑换码" style="flex:0 0 180px">
      <button class="btn ghost" id="redeemBtn">兑换</button>
    </div>
  </div>
  <div class="card">
    <h2>账号权益</h2>
    <table><thead><tr><th>账号</th><th class="right">积分</th><th class="right">Spark</th><th>会员</th><th class="right">待领</th><th>上次签到</th><th>会话</th><th class="right">操作</th></tr></thead>
    <tbody id="benefitRows"></tbody></table>
  </div>
  <div class="card">
    <h2>权益事件</h2>
    <div class="row" style="margin-bottom:12px">
      <button class="btn ghost sm" id="reloadBenefits">刷新</button>
      <button class="btn danger sm" id="clearBenefits">清空</button>
    </div>
    <table><thead><tr><th>时间</th><th>账号</th><th>动作</th><th>结果</th><th>详情</th></tr></thead>
    <tbody id="benefitLogRows"></tbody></table>
  </div>
</section>

<section id="tab-keys">
  <div class="card">
    <h2>API 密钥</h2>
    <p class="hint">客户端用 <code>Authorization: Bearer &lt;key&gt;</code> 调用。未创建任何密钥时，控制台密码可临时充当密钥。</p>
    <div class="row">
      <input id="newKey" placeholder="留空自动生成 sk-..." style="flex:1;min-width:220px">
      <input id="newNote" placeholder="备注" style="flex:0 0 160px">
      <button class="btn" id="addKey">新建</button>
    </div>
    <table style="margin-top:14px"><thead><tr><th>密钥</th><th>备注</th><th>创建时间</th><th>状态</th><th class="right">操作</th></tr></thead>
    <tbody id="keyRows"></tbody></table>
  </div>
</section>

<section id="tab-models">
  <div class="card">
    <h2>模型目录</h2>
    <p class="hint" id="modelMeta"></p>
    <div class="row" style="margin-bottom:12px">
      <button class="btn ghost" id="syncModels">从上游刷新</button>
    </div>
    <table><thead><tr><th>上游 Slug</th><th>显示名</th><th>目录 ID</th><th class="right">上下文</th><th>思考档位</th><th class="right">倍率</th></tr></thead>
    <tbody id="modelRows"></tbody></table>
  </div>
</section>

<section id="tab-logs">
  <div class="card">
    <h2>请求日志</h2>
    <p class="hint">仅记录元信息，不保存对话内容。保留条数与天数可在设置中调整。</p>
    <div class="row" style="margin-bottom:12px">
      <button class="btn ghost sm" id="reloadLogs">刷新</button>
      <button class="btn danger sm" id="clearStats">清空统计与日志</button>
    </div>
    <table><thead><tr><th>时间</th><th>模型</th><th>密钥</th><th>流式</th><th class="right">状态</th><th class="right">输入/输出</th><th class="right">耗时</th><th>错误</th></tr></thead>
    <tbody id="logRows"></tbody></table>
  </div>
</section>

<section id="tab-settings">
  <div class="card">
    <h2>上游配置</h2>
    <div class="grid c2">
      <div><label>推理 Base URL</label><input id="sUpstream"></div>
      <div><label>模型目录 API</label><input id="sModels"></div>
      <div><label>工作区 API</label><input id="sWorkspace"></div>
      <div><label>客户端版本 (studioVersion)</label><input id="sVersion"></div>
      <div><label>AStudio 数据目录</label><input id="sDataDir"></div>
      <div><label>上游并发上限</label><input id="sConc" type="number" min="1" max="64"></div>
      <div><label>响应超时（秒）</label><input id="sReqTimeout" type="number" min="1" max="3600"></div>
      <div><label>流空闲超时（秒）</label><input id="sIdleTimeout" type="number" min="1" max="3600"></div>
      <div><label>日志保留天数</label><input id="sLogDays" type="number" min="1" max="3650"></div>
      <div><label>日志条数上限</label><input id="sLogMax" type="number" min="100" max="100000"></div>
      <div><label>允许的 CORS 来源（逗号分隔，留空=仅本机）</label><input id="sCors"></div>
      <div><label>模型别名（别名=目标，逗号分隔）</label><input id="sAliases"></div>
      <div><label>保活间隔（分钟，0=关闭）</label><input id="sKeepalive" type="number" min="0" max="1440"></div>
      <div><label>自动签到时间（0-23 点）</label><input id="sCheckinHour" type="number" min="0" max="23"></div>
    </div>
    <div style="height:12px"></div>
    <label style="display:flex;align-items:center;gap:8px;color:var(--ink)">
      <input type="checkbox" id="sAutoCheckin" style="width:auto"> 每日自动签到
    </label>
    <label style="display:flex;align-items:center;gap:8px;color:var(--ink)">
      <input type="checkbox" id="sBalanceAware" style="width:auto"> 余额感知轮转（积分耗尽的账号降权）
    </label>
    <label style="display:flex;align-items:center;gap:8px;color:var(--ink)">
      <input type="checkbox" id="sClaimPopups" style="width:auto"> 领取运营弹窗（含每日签到）
    </label>
    <label style="display:flex;align-items:center;gap:8px;color:var(--ink)">
      <input type="checkbox" id="sClaimDownload" style="width:auto"> 领取客户端下载奖励
    </label>
    <label style="display:flex;align-items:center;gap:8px;color:var(--ink)">
      <input type="checkbox" id="sClaimBeta" style="width:auto"> 领取 Beta 资格（需填写域账号）
    </label>
    <div style="height:14px"></div>
    <button class="btn" id="saveSettings">保存设置</button>
  </div>
  <div class="card">
    <h2>修改管理密码</h2>
    <div class="grid c2">
      <div><label>当前密码</label><input id="pCur" type="password"></div>
      <div><label>新密码</label><input id="pNew" type="password"></div>
    </div>
    <div style="height:14px"></div>
    <button class="btn" id="savePwd">修改密码</button>
  </div>
</section>
</main>

<div class="toast" id="toast"></div>

<script>
const $ = s => document.querySelector(s);
const state = { keys: [], accounts: [], models: [], logs: [], benefits: [] };

function toast(msg, isErr){
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast on' + (isErr ? ' err' : '');
  clearTimeout(el._t);
  el._t = setTimeout(()=>{ el.className='toast'; }, 3600);
}

async function api(path, opts={}){
  const res = await fetch('/admin/api/'+path, {
    method: opts.method || 'GET',
    headers: opts.body ? {'Content-Type':'application/json'} : {},
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch(e) { data = {error:text}; }
  if(!res.ok) throw new Error(data.error || ('HTTP '+res.status));
  return data;
}

const esc = s => String(s==null?'':s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const num = n => (n==null?0:n).toLocaleString('en-US');
const when = t => t ? new Date(t*1000).toLocaleString('zh-CN',{hour12:false}) : '';

function show(){
  $('#loginview').style.display='none';
  $('#appview').style.display='block';
  $('#logout').style.display='inline-block';
}
function hide(){
  $('#loginview').style.display='block';
  $('#appview').style.display='none';
  $('#logout').style.display='none';
}

$('#login').onclick = async () => {
  try{
    await api('login', {method:'POST', body:{password: $('#pwd').value}});
    show(); refresh();
  }catch(e){ toast(e.message, true); }
};
$('#pwd').addEventListener('keydown', e => { if(e.key==='Enter') $('#login').click(); });
$('#logout').onclick = async () => { await api('logout',{method:'POST'}); hide(); };

document.querySelectorAll('nav button').forEach(b => b.onclick = () => {
  document.querySelectorAll('nav button').forEach(x=>x.classList.toggle('on', x===b));
  document.querySelectorAll('section').forEach(s=>s.classList.toggle('on', s.id==='tab-'+b.dataset.tab));
});

async function refresh(){
  let data;
  try{ data = await api('state'); }
  catch(e){ hide(); return; }
  show();
  state.keys = data.keys||[]; state.accounts = data.accounts||[]; state.models = (data.models&&data.models.items)||[]; state.logs = data.logs||[]; state.benefits = data.benefits||[];
  renderMetrics(data); renderAccounts(); renderBenefits(); renderKeys(); renderModels(data.models); renderLogs(); renderSettings(data.settings); renderQuick(data);
}

function renderMetrics(data){
  const s = data.stats || {};
  const lat = s.first_token_count ? Math.round(s.first_token_ms_sum/s.first_token_count) : 0;
  const cards = [
    ['账号', (data.accounts||[]).length + ' / ' + (data.accounts||[]).filter(a=>a.has_credential).length + ' 就绪'],
    ['模型', (data.models&&data.models.count)||0],
    ['请求总数', num(s.requests)],
    ['成功率', s.requests ? (100*s.successes/s.requests).toFixed(1)+'%' : '—'],
    ['输入 Token', num(s.prompt_tokens)],
    ['输出 Token', num(s.completion_tokens)],
    ['缓存命中 Token', num(s.cached_tokens)],
    ['平均首字', lat ? lat+' ms' : '—'],
  ];
  $('#metrics').innerHTML = cards.map(([k,v]) =>
    '<div class="metric"><div class="k">'+esc(k)+'</div><div class="v">'+esc(v)+'</div></div>').join('');

  api('stats').then(d => {
    const rows = (d.hourly||[]);
    const max = Math.max(1, ...rows.map(r=>r.requests));
    $('#trend').innerHTML = rows.length ? rows.map(r =>
      '<tr><td class="mono">'+esc(r.hour.replace('T',' '))+':00</td><td class="right">'+num(r.requests)+
      '</td><td class="right">'+num(r.prompt_tokens)+'</td><td class="right">'+num(r.completion_tokens)+
      '</td><td><div class="bar" style="width:'+Math.max(2,100*r.requests/max)+'%"></div></td></tr>').join('')
      : '<tr><td colspan="5" class="muted">暂无数据</td></tr>';
  }).catch(()=>{});
}

function renderQuick(data){
  const key = state.keys.find(k=>k.enabled)?.key || '<先在「API 密钥」创建>';
  const origin = location.origin;
  $('#who').textContent = data.models ? ((data.models.count||0)+' 个模型 · '+(data.accounts||[]).length+' 个账号') : '';
  $('#quickstart').textContent =
    'Base URL : ' + origin + '/v1\n' +
    'API Key  : ' + key + '\n\n' +
    'curl ' + origin + '/v1/chat/completions \\\n' +
    '  -H "Authorization: Bearer ' + (state.keys.find(k=>k.enabled)?.key || 'sk-...') + '" \\\n' +
    '  -H "Content-Type: application/json" \\\n' +
    '  -d \'{"model":"' + ((state.models[0]&&state.models[0].slug)||'xopglm52') + '","messages":[{"role":"user","content":"你好"}]}\'';
}

function renderAccounts(){
  $('#acctRows').innerHTML = state.accounts.length ? state.accounts.map(a => {
    const status = a.enabled
      ? (a.has_credential ? '<span class="pill ok">就绪</span>' : '<span class="pill warn">缺凭据</span>')
      : '<span class="pill bad">已停用</span>';
    const err = a.last_error ? '<div class="muted" style="font-size:12px">'+esc(a.last_error.slice(0,120))+'</div>' : '';
    const points = a.points_known ? (num(a.points_balance)+' / '+num(a.spark_balance)) : '—';
    return '<tr><td>'+esc(a.name||a.account_id)+'<div class="muted mono" style="font-size:11.5px">'+esc(a.account_id)+'</div></td>'+
      '<td class="mono">'+esc(a.uid||'—')+'</td>'+
      '<td class="mono" style="font-size:11.5px">'+esc(a.credential_hint||'—')+'</td>'+
      '<td class="right">'+esc(points)+'</td>'+
      '<td>'+status+err+'</td>'+
      '<td class="right">'+num(a.successes)+' / '+num(a.failures)+'</td>'+
      '<td class="right"><button class="btn ghost sm" onclick="refreshAcct(\''+a.id+'\')">换新凭据</button> '+
      '<button class="btn ghost sm" onclick="toggleAcct(\''+a.id+'\','+(!a.enabled)+')">'+(a.enabled?'停用':'启用')+'</button> '+
      '<button class="btn danger sm" onclick="removeAcct(\''+a.id+'\')">删除</button></td></tr>';
  }).join('') : '<tr><td colspan="7" class="muted">尚未导入账号，请使用上方「一键导入」。</td></tr>';
}

function renderBenefits(){
  const accts = state.accounts||[];
  const totalPoints = accts.reduce((s,a)=>s+(a.points_balance||0),0);
  const totalSpark = accts.reduce((s,a)=>s+(a.spark_balance||0),0);
  const pending = accts.reduce((s,a)=>s+(a.pending_popups||0),0);
  const unchecked = accts.filter(a=>!a.last_checked_at).length;
  $('#benefitMetrics').innerHTML = [
    ['账号积分合计', num(totalPoints)],
    ['Spark 积分合计', num(totalSpark)],
    ['待领弹窗', num(pending)],
    ['未检查账号', num(unchecked)],
  ].map(([k,v])=>'<div class="metric"><div class="k">'+esc(k)+'</div><div class="v">'+esc(v)+'</div></div>').join('');

  $('#benefitRows').innerHTML = accts.length ? accts.map(a=>{
    const sess = !a.last_checked_at ? '<span class="pill">未知</span>'
      : (a.session_ok ? '<span class="pill ok">正常</span>' : '<span class="pill bad">异常</span>');
    const plan = a.plan_name || a.plan_code || '—';
    return '<tr><td>'+esc(a.name||a.account_id)+'<div class="muted mono" style="font-size:11.5px">'+esc(a.account_id)+'</div>'+
      (a.domain_account?'<div class="muted" style="font-size:11.5px">域账号 '+esc(a.domain_account)+'</div>':'')+'</td>'+
      '<td class="right">'+(a.points_known?num(a.points_balance):'—')+'</td>'+
      '<td class="right">'+(a.points_known?num(a.spark_balance):'—')+'</td>'+
      '<td>'+esc(plan)+(a.plan_active?' <span class="pill ok">生效</span>':'')+
        (a.status_note?'<div class="muted" style="font-size:11.5px">'+esc(a.status_note.slice(0,110))+'</div>':'')+'</td>'+
      '<td class="right">'+num(a.pending_popups)+'</td>'+
      '<td class="muted">'+when(a.last_claim_at||a.last_checked_at)+'</td>'+
      '<td>'+sess+'</td>'+
      '<td class="right">'+
        '<button class="btn sm" onclick="checkinOne(\''+a.id+'\')">签到</button> '+
        '<button class="btn ghost sm" onclick="keepaliveOne(\''+a.id+'\')">保活</button> '+
        '<button class="btn ghost sm" onclick="setDomain(\''+a.id+'\')">域账号</button>'+
      '</td></tr>';
  }).join('') : '<tr><td colspan="8" class="muted">尚未导入账号。</td></tr>';

  $('#benefitLogRows').innerHTML = (state.benefits||[]).length ? state.benefits.map(b=>{
    const cls = b.result==='ok'?'ok':(b.result==='failed'?'bad':'warn');
    return '<tr><td class="muted mono" style="font-size:11.5px">'+when(b.time)+'</td><td>'+esc(b.account)+'</td>'+
      '<td>'+esc(b.action)+'</td><td><span class="pill '+cls+'">'+esc(b.result)+'</span></td>'+
      '<td class="muted">'+esc(b.detail||'')+'</td></tr>';
  }).join('') : '<tr><td colspan="5" class="muted">暂无事件。</td></tr>';
}

window.checkinOne = async id => {
  toast('签到中…');
  try{
    const d = await api('checkin',{method:'POST',body:{id}});
    const r = (d.results||[])[0]||{};
    if(r.error) toast('签到失败：'+r.error, true);
    else if(r.points_delta>0) toast('签到成功：+'+r.points_delta+' 积分（'+((r.actions||[]).join('、'))+'）');
    else toast('签到完成：'+(r.actions&&r.actions.length?((r.actions.join('、'))+'，积分无变化（可能今日已领）'):'当前无可领取项'));
    refresh();
  }catch(e){ toast(e.message,true); }
};
window.keepaliveOne = async id => {
  try{ await api('keepalive',{method:'POST',body:{id}}); toast('保活完成'); refresh(); }
  catch(e){ toast(e.message,true); }
};
window.setDomain = async id => {
  const v = prompt('域账号（用于 Beta 资格领取，留空则清除）：');
  if(v===null) return;
  try{ await api('accounts',{method:'POST',body:{action:'domain',id,domain_account:v.trim()}}); toast('已保存'); refresh(); }
  catch(e){ toast(e.message,true); }
};

window.refreshAcct = async id => { try{ await api('accounts',{method:'POST',body:{action:'refresh',id}}); toast('凭据已刷新'); refresh(); }catch(e){ toast(e.message,true); } };
window.toggleAcct = async (id,enabled) => { try{ await api('accounts',{method:'POST',body:{action:'toggle',id,enabled}}); refresh(); }catch(e){ toast(e.message,true); } };
window.removeAcct = async id => { if(!confirm('确认删除该账号？')) return; try{ await api('accounts',{method:'POST',body:{action:'remove',id}}); refresh(); }catch(e){ toast(e.message,true); } };

function renderKeys(){
  $('#keyRows').innerHTML = state.keys.length ? state.keys.map(k =>
    '<tr><td class="mono">'+esc(k.key)+'</td><td>'+esc(k.note||'—')+'</td><td class="muted">'+when(k.created_at)+'</td>'+
    '<td>'+(k.enabled?'<span class="pill ok">启用</span>':'<span class="pill bad">停用</span>')+'</td>'+
    '<td class="right"><button class="btn ghost sm" onclick="copyKey(\''+esc(k.key)+'\')">复制</button> '+
    '<button class="btn ghost sm" onclick="toggleKey(\''+k.id+'\','+(!k.enabled)+')">'+(k.enabled?'停用':'启用')+'</button> '+
    '<button class="btn danger sm" onclick="removeKey(\''+k.id+'\')">删除</button></td></tr>').join('')
    : '<tr><td colspan="5" class="muted">还没有密钥。</td></tr>';
}
window.copyKey = k => { navigator.clipboard.writeText(k).then(()=>toast('已复制')); };
window.toggleKey = async (id,enabled) => { try{ await api('keys',{method:'POST',body:{action:'toggle',id,enabled}}); refresh(); }catch(e){ toast(e.message,true); } };
window.removeKey = async id => { if(!confirm('确认删除该密钥？')) return; try{ await api('keys?id='+encodeURIComponent(id),{method:'DELETE'}); refresh(); }catch(e){ toast(e.message,true); } };

function renderModels(meta){
  meta = meta||{};
  $('#modelMeta').textContent = '来源：' + (meta.source||'—') + ' · 共 ' + (meta.count||0) + ' 个' +
    (meta.refreshed_at ? ' · 更新于 ' + new Date(meta.refreshed_at).toLocaleString('zh-CN',{hour12:false}) : '') +
    (meta.error ? ' · ' + meta.error : '');
  $('#modelRows').innerHTML = state.models.length ? state.models.map(m =>
    '<tr><td class="mono">'+esc(m.slug)+'</td><td>'+esc(m.name)+'</td><td class="mono muted">'+esc(m.directory_id||'—')+'</td>'+
    '<td class="right">'+num(m.context_window)+'</td><td class="muted">'+esc((m.reasoning_efforts||[]).join(' / ')||'—')+'</td>'+
    '<td class="right">'+esc(m.point_multiplier||'—')+'</td></tr>').join('')
    : '<tr><td colspan="6" class="muted">模型目录为空，请先导入账号后点击「从上游刷新」。</td></tr>';
}

function renderLogs(){
  $('#logRows').innerHTML = state.logs.length ? state.logs.map(l => {
    const ok = l.status>=200 && l.status<400 && !l.error;
    return '<tr><td class="muted mono" style="font-size:11.5px">'+when(l.time)+'</td><td class="mono">'+esc(l.model)+'</td>'+
      '<td class="muted">'+esc(l.key_note||'—')+'</td><td>'+(l.stream?'流式':'普通')+'</td>'+
      '<td class="right">'+(ok?'<span class="pill ok">'+l.status+'</span>':'<span class="pill bad">'+l.status+'</span>')+'</td>'+
      '<td class="right">'+num(l.prompt_tokens)+' / '+num(l.completion_tokens)+'</td>'+
      '<td class="right">'+(l.duration_ms||0)+' ms</td>'+
      '<td class="muted" style="max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="'+esc(l.error||'')+'">'+esc((l.error||'').slice(0,90))+'</td></tr>';
  }).join('') : '<tr><td colspan="8" class="muted">暂无请求。</td></tr>';
}

function renderSettings(s){
  if(!s) return;
  $('#sUpstream').value = s.upstream_base||''; $('#sModels').value = s.models_base||'';
  $('#sWorkspace').value = s.workspace_api||''; $('#sVersion').value = s.studio_version||'';
  $('#sDataDir').value = s.astron_data_dir||''; $('#sConc').value = s.max_concurrency||1;
  $('#sReqTimeout').value = s.request_timeout_seconds||300; $('#sIdleTimeout').value = s.idle_timeout_seconds||300;
  $('#sLogDays').value = s.log_retention_days||30; $('#sLogMax').value = s.log_max_entries||2000;
  $('#sCors').value = (s.cors_origins||[]).join(', ');
  $('#sAliases').value = Object.entries(s.model_aliases||{}).map(([k,v])=>k+'='+v).join(', ');
  $('#sKeepalive').value = s.keepalive_minutes==null?0:s.keepalive_minutes;
  $('#sCheckinHour').value = s.checkin_hour==null?9:s.checkin_hour;
  $('#sAutoCheckin').checked = !!s.auto_checkin;
  $('#sBalanceAware').checked = !!s.balance_aware_rotation;
  $('#sClaimPopups').checked = !!s.checkin_complete_popups;
  $('#sClaimDownload').checked = !!s.checkin_claim_download_reward;
  $('#sClaimBeta').checked = !!s.checkin_claim_beta;
}

function parseAliases(text){
  const out = {};
  text.split(',').map(x=>x.trim()).filter(Boolean).forEach(pair => {
    const i = pair.indexOf('=');
    if(i>0) out[pair.slice(0,i).trim()] = pair.slice(i+1).trim();
  });
  return out;
}

$('#importBtn').onclick = async () => {
  $('#importBtn').disabled = true;
  try{
    await api('accounts',{method:'POST',body:{action:'import', data_dir: $('#acctdir').value.trim()}});
    toast('导入成功'); refresh();
  }catch(e){ toast(e.message, true); }
  finally{ $('#importBtn').disabled = false; }
};
$('#manualToggle').onclick = () => {
  const box = $('#manualBox');
  box.style.display = box.style.display==='none' ? 'block' : 'none';
};
$('#manualSave').onclick = async () => {
  try{
    await api('accounts',{method:'POST',body:{action:'manual',
      account_id: $('#mAcctId').value.trim(), token: $('#mToken').value.trim(),
      sso_session_id: $('#mSso').value.trim(), bearer_token: $('#mBearer').value.trim()}});
    toast('已保存'); refresh();
  }catch(e){ toast(e.message,true); }
};
$('#addKey').onclick = async () => {
  try{
    await api('keys',{method:'POST',body:{key:$('#newKey').value.trim(), note:$('#newNote').value.trim()}});
    $('#newKey').value=''; $('#newNote').value=''; toast('已创建'); refresh();
  }catch(e){ toast(e.message,true); }
};
$('#syncModels').onclick = async () => {
  try{ const d = await api('models',{method:'POST'}); toast('已刷新 '+(d.items||[]).length+' 个模型'); refresh(); }
  catch(e){ toast(e.message,true); }
};
$('#checkinAll').onclick = async () => {
  toast('正在为全部账号签到…');
  try{
    const d = await api('checkin',{method:'POST',body:{}});
    const rs = d.results||[];
    const ok = rs.filter(r=>!r.error);
    const gained = ok.reduce((s,r)=>s+(r.points_delta||0),0);
    const failed = rs.filter(r=>r.error);
    toast('签到完成：'+ok.length+' 个账号正常，获得 '+gained+' 积分'+(failed.length?('，'+failed.length+' 个失败'):''), failed.length>0);
    refresh();
  }catch(e){ toast(e.message,true); }
};
$('#keepaliveAll').onclick = async () => {
  try{ const d = await api('keepalive',{method:'POST',body:{}}); toast(d.ok?'全部保活成功':('保活完成，'+(d.errors||[]).length+' 个账号失败'), !d.ok); refresh(); }
  catch(e){ toast(e.message,true); }
};
$('#redeemBtn').onclick = async () => {
  const code = $('#redeemCode').value.trim();
  if(!code) return toast('请输入兑换码', true);
  try{
    const d = await api('redeem',{method:'POST',body:{code}});
    toast('兑换成功：+'+(d.result&&d.result.points||0)+' 积分');
    $('#redeemCode').value=''; refresh();
  }catch(e){ toast(e.message,true); }
};
$('#reloadBenefits').onclick = refresh;
$('#clearBenefits').onclick = async () => {
  if(!confirm('确认清空权益事件记录？')) return;
  try{ await api('benefits',{method:'DELETE'}); toast('已清空'); refresh(); }catch(e){ toast(e.message,true); }
};
$('#reloadLogs').onclick = refresh;

// --- 手机号验证码登录 -------------------------------------------------------
// 严格对齐官方客户端：initGeetest({...cfg, https:true, offline:!success,
// product:'bind', width:'100%'})，onReady 后 verify()，30s 超时。
const GT_SDK = 'https://static.geetest.com/static/tools/gt.js';
let gtLoading = null;
let smsCountdown = null;

function loadGeetestSdk(){
  if(typeof window.initGeetest === 'function') return Promise.resolve();
  if(gtLoading) return gtLoading;
  gtLoading = new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = GT_SDK; s.async = true; s.dataset.astudioGeetest = 'true';
    s.addEventListener('load', () => resolve(), {once:true});
    s.addEventListener('error', () => {
      s.remove(); gtLoading = null;
      reject(new Error('安全验证脚本加载失败，请检查网络（需能访问 static.geetest.com）'));
    }, {once:true});
    document.head.appendChild(s);
  });
  return gtLoading;
}

async function solveGeetest(){
  await loadGeetestSdk();
  if(typeof window.initGeetest !== 'function') throw new Error('安全验证组件不可用，请刷新页面重试');
  const d = await api('login/geetest');
  const cfg = d.geetest || {};
  return new Promise((resolve, reject) => {
    let done = false, obj = null, timer = null;
    const finish = r => {
      if(done) return;
      done = true;
      window.clearTimeout(timer);
      try{ obj && obj.destroy && obj.destroy(); }catch(e){}
      if(r.payload) resolve(r.payload); else reject(r.error || new Error('安全验证失败'));
    };
    timer = window.setTimeout(() => finish({error:new Error('安全验证超时，请重试')}), 30000);
    window.initGeetest({...cfg, https:true, offline:!cfg.success, product:'bind', width:'100%'}, inst => {
      obj = inst;
      inst.onReady(() => { try{ inst.verify(); }catch(e){ finish({error:new Error('安全验证启动失败，请重试')}); } });
      inst.onError(() => finish({error:new Error('安全验证异常，请重试')}));
      if(inst.onClose) inst.onClose(() => finish({error:new Error('安全验证已取消')}));
      inst.onSuccess(() => {
        const v = inst.getValidate();
        if(!v){ finish({error:new Error('安全验证结果无效，请重试')}); return; }
        finish({payload:{
          geetest_challenge: v.geetest_challenge,
          geetest_validate: v.geetest_validate,
          geetest_seccode: v.geetest_seccode,
        }});
      });
    });
  });
}

function startSmsCountdown(seconds){
  const btn = $('#sendSmsBtn');
  let n = seconds;
  window.clearInterval(smsCountdown);
  const tick = () => {
    if(n <= 0){ window.clearInterval(smsCountdown); btn.disabled = false; btn.textContent = '发送验证码'; return; }
    btn.disabled = true; btn.textContent = '重新发送 '+n+'s'; n--;
  };
  tick();
  smsCountdown = window.setInterval(tick, 1000);
}

$('#sendSmsBtn').onclick = async () => {
  const mobile = $('#smsMobile').value.trim();
  if(!/^1[3-9]\d{9}$/.test(mobile)) return toast('请输入正确的 11 位手机号', true);
  const btn = $('#sendSmsBtn');
  btn.disabled = true;
  $('#smsHint').textContent = '请在弹出的安全验证中完成滑块…';
  try{
    const cap = await solveGeetest();
    $('#smsHint').textContent = '正在发送…';
    await api('login/sms',{method:'POST',body:{mobile, ...cap}});
    toast('验证码已发送');
    $('#smsHint').textContent = '验证码已发送，请查看短信。';
    startSmsCountdown(60);
  }catch(e){
    toast(e.message, true);
    $('#smsHint').textContent = e.message;
    btn.disabled = false;
  }
};

$('#smsLoginBtn').onclick = async () => {
  const mobile = $('#smsMobile').value.trim();
  const code = $('#smsCode').value.trim();
  if(!/^1[3-9]\d{9}$/.test(mobile)) return toast('请输入正确的 11 位手机号', true);
  if(!code) return toast('请输入短信验证码', true);
  const btn = $('#smsLoginBtn');
  btn.disabled = true;
  btn.textContent = '登录中…';
  try{
    const d = await api('login/verify',{method:'POST',body:{mobile, verify_code:code}});
    const a = d.account || {};
    toast('登录成功：'+(a.nickname || a.mobile || a.account_id)+'，已加入账号池');
    $('#smsCode').value = '';
    $('#smsHint').textContent = '';
    refresh();
  }catch(e){ toast(e.message, true); }
  finally{ btn.disabled = false; btn.textContent = '登录并添加账号'; }
};
$('#clearStats').onclick = async () => {
  if(!confirm('确认清空全部统计与日志？')) return;
  try{ await api('stats',{method:'DELETE'}); toast('已清空'); refresh(); }catch(e){ toast(e.message,true); }
};
$('#saveSettings').onclick = async () => {
  const cors = $('#sCors').value.split(',').map(x=>x.trim()).filter(Boolean);
  try{
    await api('settings',{method:'POST',body:{
      upstream_base: $('#sUpstream').value.trim(), models_base: $('#sModels').value.trim(),
      workspace_api: $('#sWorkspace').value.trim(), studio_version: $('#sVersion').value.trim(),
      astron_data_dir: $('#sDataDir').value.trim(),
      max_concurrency: +$('#sConc').value, request_timeout_seconds: +$('#sReqTimeout').value,
      idle_timeout_seconds: +$('#sIdleTimeout').value, log_retention_days: +$('#sLogDays').value,
      log_max_entries: +$('#sLogMax').value, cors_origins: cors,
      model_aliases: parseAliases($('#sAliases').value),
      keepalive_minutes: +$('#sKeepalive').value,
      checkin_hour: +$('#sCheckinHour').value,
      auto_checkin: $('#sAutoCheckin').checked,
      balance_aware_rotation: $('#sBalanceAware').checked,
      checkin_complete_popups: $('#sClaimPopups').checked,
      checkin_claim_download_reward: $('#sClaimDownload').checked,
      checkin_claim_beta: $('#sClaimBeta').checked,
    }});
    toast('已保存'); refresh();
  }catch(e){ toast(e.message,true); }
};
$('#savePwd').onclick = async () => {
  try{
    await api('password',{method:'POST',body:{current:$('#pCur').value, next:$('#pNew').value}});
    $('#pCur').value=''; $('#pNew').value=''; toast('密码已修改');
  }catch(e){ toast(e.message,true); }
};

refresh();
setInterval(() => { if($('#appview').style.display!=='none') refresh(); }, 15000);
</script>
</body>
</html>
`
