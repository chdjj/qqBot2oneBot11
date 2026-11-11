/* qqbot-onebot console front-end (written by AI) */
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const state = {
  view: 'overview',
  bots: [],
  listeners: [],
  groups: [],
  overview: null,
  logs: [],
  recent: [],
  ws: null,
  wsAlive: false,
  logLevel: 'all',
};

const VIEWS = {
  overview: ['概览', '适配器的实时状态与关键指标。'],
  bots: ['官方机器人', '管理 QQ 开放平台的机器人凭据与网关连接。'],
  listeners: ['OneBot 监听', '为每个机器人配置 HTTP / SSE / WebSocket 通信方式。'],
  groups: ['群与 openid', '已记录群 openid 的群，可用于发送消息。'],
  test: ['发送测试消息', '向指定群主动发送一条测试消息，验证链路。'],
  logs: ['实时日志', '适配器与网关的运行日志。'],
};

const TYPE_LABEL = {
  http_server: 'HTTP 服务器',
  http_sse: 'HTTP + SSE 服务器',
  http_client: 'HTTP 客户端',
  ws_server: 'WebSocket 服务器',
  ws_client: 'WebSocket 客户端',
};

const TYPE_NOTE = {
  http_server: '监听端口，事件上报到你填写的地址',
  http_sse: '监听端口，事件以 SSE 流下发',
  http_client: '事件 POST 到你填写的 HTTP 地址',
  ws_server: '客户端连接本适配器的 WebSocket',
  ws_client: '本适配器主动连接你填写的 WebSocket',
};

/* ---------- helpers ---------- */

function fmt(n) {
  if (n == null) return '—';
  return new Intl.NumberFormat('zh-CN').format(n);
}

function fmtTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  return d.toLocaleTimeString('zh-CN', { hour12: false });
}

function fmtDuration(sec) {
  if (sec == null || sec < 0) return '—';
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`;
  return `${Math.floor(sec / 86400)}d ${Math.floor((sec % 86400) / 3600)}h`;
}

function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  });
  let body = null;
  const text = await res.text();
  if (text) {
    try { body = JSON.parse(text); } catch { body = text; }
  }
  if (!res.ok) {
    const msg = (body && (body.error || body.msg)) || `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return body;
}

function toast(msg, bad = false) {
  const el = document.createElement('div');
  el.className = 'toast' + (bad ? ' bad' : '');
  el.textContent = msg;
  $('#toasts').appendChild(el);
  setTimeout(() => el.remove(), 4200);
}

/* ---------- data loading ---------- */

async function loadAll() {
  try {
    const [overview, bots, listeners, groups, logs, recent] = await Promise.all([
      api('/api/overview'),
      api('/api/bots'),
      api('/api/listeners'),
      api('/api/groups'),
      api('/api/logs'),
      api('/api/events'),
    ]);
    state.overview = overview;
    state.bots = bots || [];
    state.listeners = listeners || [];
    state.groups = groups || [];
    state.logs = logs || [];
    state.recent = recent || [];
    renderFoot();
    renderView();
  } catch (e) {
    toast('加载失败：' + e.message, true);
  }
}

/* ---------- rendering ---------- */

function renderFoot() {
  const o = state.overview || {};
  const live = o.bots_online > 0;
  const dot = $('#pulse-dot');
  dot.className = 'dot ' + (live ? 'live' : (o.bots > 0 ? 'warn' : 'dead'));
  $('#pulse-text').textContent = live
    ? `${o.bots_online}/${o.bots} 在线`
    : (o.bots ? '网关未连接' : '未配置机器人');
  $('#foot-gateway').textContent = live ? '已连接' : '离线';
  $('#foot-listeners').textContent = `${o.listeners || 0}`;
  $('#foot-events').textContent = fmt(o.events || 0);
}

function renderView() {
  const [title, hint] = VIEWS[state.view] || VIEWS.overview;
  $('#view-title').textContent = title;
  $('#view-hint').textContent = hint;
  $$('.nav button').forEach(b => b.classList.toggle('on', b.dataset.view === state.view));
  $$('.view').forEach(v => { v.hidden = v.dataset.view !== state.view; });
  $('#head-actions').innerHTML = '';

  switch (state.view) {
    case 'overview': renderOverview(); break;
    case 'bots': renderBots(); break;
    case 'listeners': renderListeners(); break;
    case 'groups': renderGroups(); break;
    case 'test': renderTest(); break;
    case 'logs': renderLogs(); break;
  }
}

function renderOverview() {
  const o = state.overview || {};
  const metrics = [
    ['机器人', o.bots ?? 0, `${o.bots_online ?? 0} 在线`, o.bots_online > 0],
    ['OneBot 监听', o.listeners ?? 0, '条配置', false],
    ['已知群', o.groups ?? 0, '个 openid', false],
    ['上行事件', fmt(o.events ?? 0), '累计', false],
    ['下行动作', fmt(o.actions ?? 0), '累计', false],
    ['运行时长', fmtDuration(o.uptime), '自启动', false],
  ];
  $('#metrics').innerHTML = metrics.map(([label, value, sub, sig]) => `
    <div class="metric">
      <div class="label">${esc(label)}</div>
      <div class="value${sig ? ' signal' : ''}">${esc(value)}</div>
      <div class="sub">${esc(sub)}</div>
    </div>`).join('');

  const bots = state.bots;
  $('#overview-bots').innerHTML = bots.length ? bots.map(b => botRow(b, true)).join('')
    : `<p class="empty">尚未添加机器人。</p>`;

  const evs = state.recent.slice(-12).reverse();
  $('#overview-events').innerHTML = evs.length ? evs.map(ev => `
    <div class="feed-item">
      <span class="feed-time">${fmtTime(ev.time)}</span>
      <span class="feed-body">${feedBody(ev)}</span>
      <span class="feed-tag">${esc(ev.post_type)}</span>
    </div>`).join('') : `<p class="feed-empty">还没有收到事件。让用户在群里 @ 机器人试试。</p>`;
}

function feedBody(ev) {
  if (ev.post_type === 'message') {
    if (ev.message_type === 'group') {
      return `<b>群聊</b> ${esc(ev.group_id)} · ${esc(ev.summary || '(空消息)')}`;
    }
    if (ev.message_type === 'private') {
      return `<b>单聊</b> ${esc(ev.user_id)} · ${esc(ev.summary || '(空消息)')}`;
    }
    return `<b>消息</b> · ${esc(ev.summary || '(空消息)')}`;
  }
  const target = ev.group_id ? `群 ${esc(ev.group_id)}` : (ev.user_id ? `用户 ${esc(ev.user_id)}` : '');
  return `${esc(ev.post_type)} ${target} · ${esc(ev.summary || '')}`.trim();
}

function statusTag(b) {
  const st = (b.runtime && b.runtime.state) || 'stopped';
  const map = {
    online: ['已连接', 'sig'],
    connecting: ['连接中', 'warn'],
    resuming: ['恢复中', 'warn'],
    error: ['异常', 'alarm'],
    offline: ['离线', ''],
    stopped: ['未启动', ''],
  };
  const [label, cls] = map[st] || ['未知', ''];
  return `<span class="tag ${cls}">${label}</span>`;
}

function botRow(b, compact) {
  const st = b.runtime || {};
  const online = st.state === 'online';
  const meta = [
    `AppID <b>${esc(b.app_id)}</b>`,
    b.sandbox ? `<span class="tag warn">沙箱</span>` : `<span class="tag">正式</span>`,
    st.self_id ? `self_id <b>${esc(st.self_id)}</b>` : '',
    st.username ? `昵称 <b>${esc(st.username)}</b>` : '',
    st.event_count ? `事件 <b>${fmt(st.event_count)}</b>` : '',
    st.last_error ? `<span class="tag alarm">${esc(st.last_error)}</span>` : '',
  ].filter(Boolean).join(' · ');

  const actions = compact ? '' : `
    <div class="row-actions">
      ${b.enabled
        ? `<button class="btn small" data-bot-action="stop" data-id="${b.id}">停止</button>`
        : `<button class="btn small" data-bot-action="start" data-id="${b.id}">启动</button>`}
      <button class="btn small" data-bot-action="check" data-id="${b.id}">校验凭据</button>
      <button class="btn small" data-bot-action="edit" data-id="${b.id}">编辑</button>
      <button class="btn small danger" data-bot-action="delete" data-id="${b.id}">删除</button>
    </div>`;

  return `
    <div class="row ${online ? 'live' : (st.state === 'error' ? 'error' : '')}">
      <div class="row-main">
        <div class="row-title">
          <strong>${esc(b.name || '机器人 ' + b.app_id)}</strong>
          ${statusTag(b)}
        </div>
        <div class="row-meta">${meta}</div>
      </div>
      ${actions}
    </div>`;
}

function renderBots() {
  const list = state.bots;
  $('#bot-empty').hidden = list.length > 0;
  $('#bot-list').innerHTML = list.map(b => botRow(b, false)).join('');
}

function renderListeners() {
  const list = state.listeners;
  $('#listener-empty').hidden = list.length > 0;
  const botsById = Object.fromEntries(state.bots.map(b => [b.id, b]));
  $('#listener-list').innerHTML = list.map(l => {
    const bot = botsById[l.bot_id];
    const botName = bot ? (bot.name || '机器人 ' + bot.app_id) : `机器人 #${l.bot_id}`;
    const target = (l.type === 'http_server' || l.type === 'http_sse' || l.type === 'ws_server')
      ? `${esc(l.host || '0.0.0.0')}:${l.port}${l.path ? ' / ' + esc(l.path) : ''}`
      : esc(l.url || '(未填写地址)');
    const extra = l.type === 'http_server' && l.url ? ` · 上报 ${esc(l.url)}` : '';
    return `
      <div class="row ${l.enabled ? 'live' : ''}">
        <div class="row-main">
          <div class="row-title">
            <strong>${esc(l.name || TYPE_LABEL[l.type])}</strong>
            <span class="tag sig">${esc(TYPE_LABEL[l.type] || l.type)}</span>
            ${l.token ? '<span class="tag">token</span>' : '<span class="tag warn">无 token</span>'}
            ${l.enabled ? '' : '<span class="tag">已停用</span>'}
          </div>
          <div class="row-meta">${esc(botName)} · ${target}${extra}</div>
        </div>
        <div class="row-actions">
          <button class="btn small" data-listener-action="edit" data-id="${l.id}">编辑</button>
          <button class="btn small danger" data-listener-action="delete" data-id="${l.id}">删除</button>
        </div>
      </div>`;
  }).join('');
}

function renderGroups() {
  const sel = $('#group-bot-filter');
  const current = sel.value;
  sel.innerHTML = `<option value="0">全部机器人</option>` +
    state.bots.map(b => `<option value="${b.id}">${esc(b.name || '机器人 ' + b.app_id)}</option>`).join('');
  sel.value = current || '0';

  const filter = Number(sel.value || 0);
  const list = state.groups.filter(g => !filter || g.bot_id === filter);
  $('#group-empty').hidden = list.length > 0;
  $('#group-list').innerHTML = list.map(g => `
    <div class="row">
      <div class="row-main">
        <div class="row-title">
          <strong>${esc(g.name || '未命名群')}</strong>
          <span class="tag">ID ${g.num_id}</span>
        </div>
        <div class="row-meta">openid <b>${esc(g.open_id)}</b> · ${esc(g.bot_name || '')} · 最近 ${fmtTime(g.last_seen)}</div>
      </div>
      <div class="row-actions">
        <button class="btn small" data-group-action="rename" data-id="${g.id}">重命名</button>
        <button class="btn small danger" data-group-action="delete" data-id="${g.id}">删除</button>
      </div>
    </div>`).join('');
}

function renderTest() {
  const botSel = $('#test-bot');
  botSel.innerHTML = state.bots.map(b =>
    `<option value="${b.id}">${esc(b.name || '机器人 ' + b.app_id)}${b.runtime && b.runtime.state === 'online' ? '（在线）' : ''}</option>`).join('');
  if (!state.bots.length) {
    botSel.innerHTML = '<option value="">请先添加机器人</option>';
  }
  syncTestGroups();
}

function syncTestGroups() {
  const botID = Number($('#test-bot').value || 0);
  const groups = state.groups.filter(g => g.bot_id === botID);
  const sel = $('#test-group');
  sel.innerHTML = groups.length
    ? groups.map(g => `<option value="${esc(g.open_id)}">${esc(g.name || '未命名群')} — openid ${esc(g.open_id)}</option>`).join('')
    : '<option value="">（该机器人暂无已知群）</option>';

  // Explain how to obtain an openid instead of leaving the user guessing.
  const hint = $('#test-group-hint');
  if (groups.length) {
    hint.hidden = true;
    hint.innerHTML = '';
    return;
  }
  hint.hidden = false;
  const online = state.bots.find(b => b.id === botID);
  const isOnline = online && online.runtime && online.runtime.state === 'online';
  hint.innerHTML = `
    <p>还没有记录到群 openid。群 openid 不是 QQ 群号，只能从机器人收到的事件里获得：</p>
    <ol class="steps">
      <li>${isOnline
        ? '机器人已连接。请在你想要的那个群里 <b>@ 机器人</b> 发送任意一句话。'
        : '<b>机器人当前未连接</b>，请先在「官方机器人」页点「启动」，确认状态为「已连接」。'}</li>
      <li>发完消息后回到本页，目标群下拉框会自动出现这个群。</li>
      <li>也可以打开「群与 openid」页查看已记录的 openid 并复制。</li>
    </ol>`;
}

function renderLogs() {
  const stream = $('#log-stream');
  const lv = state.logLevel;
  const filtered = state.logs.filter(l => lv === 'all' || l.level === lv);
  stream.innerHTML = filtered.map(l => logLine(l)).join('');
  stream.scrollTop = stream.scrollHeight;
}

function logLine(l) {
  const d = new Date(l.time);
  const t = d.toLocaleTimeString('zh-CN', { hour12: false }) + '.' + String(d.getMilliseconds()).padStart(3, '0');
  return `<div class="log-line">
    <span class="t">${t}</span>
    <span class="lv ${esc(l.level)}">${esc(l.level)}</span>
    <span class="msg">${esc(l.msg)}</span>
  </div>`;
}

/* ---------- live updates ---------- */

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/api/ws`);
  state.ws = ws;

  ws.onopen = () => { state.wsAlive = true; };
  ws.onclose = () => {
    state.wsAlive = false;
    setTimeout(connectWS, 2000);
  };
  ws.onerror = () => { try { ws.close(); } catch (_) {} };
  ws.onmessage = (ev) => {
    let m;
    try { m = JSON.parse(ev.data); } catch { return; }
    switch (m.type) {
      case 'hello':
        if (m.logs) state.logs = m.logs.slice();
        if (m.recent) state.recent = m.recent.slice();
        if (state.view === 'logs') renderLogs();
        if (state.view === 'overview') renderOverview();
        break;
      case 'log':
        state.logs.push({ time: Date.now(), level: m.level || 'info', msg: m.msg });
        if (state.logs.length > 600) state.logs = state.logs.slice(-600);
        if (state.view === 'logs') {
          const stream = $('#log-stream');
          const nearBottom = stream.scrollHeight - stream.scrollTop - stream.clientHeight < 40;
          if (state.logLevel === 'all' || state.logLevel === m.level) {
            stream.insertAdjacentHTML('beforeend', logLine({ time: Date.now(), level: m.level || 'info', msg: m.msg }));
            if (nearBottom) stream.scrollTop = stream.scrollHeight;
          }
        }
        break;
      case 'status':
        refreshBotRuntime(m.bot_id, m.status);
        break;
      case 'event':
        state.recent.push({
          time: (m.event && m.event.time) || Math.floor(Date.now() / 1000),
          post_type: m.post_type,
          message_type: m.event && m.event.message_type,
          group_id: m.event && m.event.group_id,
          user_id: m.event && m.event.user_id,
          self_id: m.self_id,
          summary: m.event && m.event.raw_message,
        });
        if (state.recent.length > 200) state.recent = state.recent.slice(-200);
        if (state.view === 'overview') renderOverview();
        scheduleRefresh();
        break;
      case 'listener':
        scheduleRefresh();
        break;
    }
  };
}

function refreshBotRuntime(botID, status) {
  const b = state.bots.find(x => x.id === botID);
  if (b && status) { b.runtime = status; }
  if (state.view === 'overview' || state.view === 'bots') renderView();
}

let refreshTimer = null;
function scheduleRefresh() {
  if (refreshTimer) return;
  refreshTimer = setTimeout(async () => {
    refreshTimer = null;
    try {
      const [groups, overview, bots, listeners] = await Promise.all([
        api('/api/groups'), api('/api/overview'), api('/api/bots'), api('/api/listeners'),
      ]);
      state.groups = groups || [];
      state.overview = overview;
      state.bots = bots || [];
      state.listeners = listeners || [];
      renderFoot();
      if (['groups', 'test', 'overview', 'listeners'].includes(state.view)) renderView();
    } catch (_) { /* keep old state */ }
  }, 800);
}

/* ---------- modal ---------- */

function openModal(title, bodyHTML, submitLabel, onSubmit) {
  $('#modal-title').textContent = title;
  $('#modal-body').innerHTML = bodyHTML;
  $('#modal-error').textContent = '';
  $('#modal-submit').textContent = submitLabel || '保存';
  $('#modal').hidden = false;
  const form = $('#modal-form');
  form.onsubmit = async (e) => {
    e.preventDefault();
    const btn = $('#modal-submit');
    btn.disabled = true;
    try {
      await onSubmit(form);
      closeModal();
    } catch (err) {
      $('#modal-error').textContent = err.message;
    } finally {
      btn.disabled = false;
    }
  };
  const first = $('#modal-body input, #modal-body textarea');
  if (first) first.focus();
}

function closeModal() { $('#modal').hidden = true; $('#modal-form').onsubmit = null; }

/* ---------- bot dialogs ---------- */

function botFormHTML(b) {
  const editing = b.id != null;
  return `
    <label class="field"><span>显示名称（可选）</span>
      <input name="name" value="${esc(b.name || '')}" placeholder="例如 社区助手"></label>
    <label class="field"><span>AppID</span>
      <input name="app_id" required value="${esc(b.app_id || '')}" placeholder="开放平台提供的 AppID"></label>
    <label class="field"><span>AppSecret${editing ? '（留空保持不变）' : ''}</span>
      <input name="secret" type="password" ${editing ? '' : 'required'} value="" placeholder="${editing && b.has_secret ? '已保存，留空则不修改' : '开放平台提供的 AppSecret'}"></label>
    <div class="grid-2">
      <label class="check"><input type="checkbox" name="sandbox" ${b.sandbox ? 'checked' : ''}><span>使用沙箱环境</span></label>
      <label class="check"><input type="checkbox" name="enabled" ${b.enabled ? 'checked' : ''}><span>保存后立即启动</span></label>
    </div>
    <p class="field-hint">默认订阅 <code>GROUP_AND_C2C_EVENT (1&lt;&lt;25)</code>，接收群 @ 消息与单聊消息。无权限的 intents 位会导致网关拒绝连接。</p>
    <label class="field"><span>Intents（十进制，可选）</span>
      <input name="intents" type="number" value="${b.intents || 33554432}"></label>
  `;
}

function readBotForm(form) {
  const data = new FormData(form);
  return {
    name: data.get('name') || '',
    app_id: (data.get('app_id') || '').trim(),
    secret: (data.get('secret') || '').trim(),
    sandbox: data.get('sandbox') === 'on',
    enabled: data.get('enabled') === 'on',
    intents: Number(data.get('intents') || 33554432),
  };
}

function openAddBot() {
  openModal('添加官方机器人', botFormHTML({}), '添加', async (form) => {
    await api('/api/bots', { method: 'POST', body: JSON.stringify(readBotForm(form)) });
    toast('已添加机器人');
    await loadAll();
  });
}

function openEditBot(b) {
  openModal('编辑机器人', botFormHTML(b), '保存', async (form) => {
    await api(`/api/bots/${b.id}`, { method: 'PUT', body: JSON.stringify(readBotForm(form)) });
    toast('已保存');
    await loadAll();
  });
}

/* ---------- listener dialogs ---------- */

function listenerFormHTML(l) {
  const type = l.type || 'http_server';
  return `
    <label class="field"><span>所属机器人</span>
      <select name="bot_id" required>
        ${state.bots.map(b => `<option value="${b.id}" ${l.bot_id === b.id ? 'selected' : ''}>${esc(b.name || '机器人 ' + b.app_id)}</option>`).join('')}
      </select></label>
    <label class="field"><span>名称</span>
      <input name="name" value="${esc(l.name || '')}" placeholder="例如 正向 WS"></label>
    <label class="field"><span>类型</span>
      <select name="type" id="lf-type">
        ${Object.entries(TYPE_LABEL).map(([k, v]) => `<option value="${k}" ${type === k ? 'selected' : ''}>${v}</option>`).join('')}
      </select></label>
    <div id="lf-server" class="grid-2">
      <label class="field"><span>监听地址</span>
        <input name="host" value="${esc(l.host || '0.0.0.0')}" placeholder="0.0.0.0"></label>
      <label class="field"><span>监听端口</span>
        <input name="port" type="number" value="${l.port || ''}" placeholder="8081"></label>
    </div>
    <label class="field" id="lf-path-wrap"><span>路径（可选）</span>
      <input name="path" value="${esc(l.path || '')}" placeholder="留空则使用 /"></label>
    <label class="field" id="lf-url-wrap"><span id="lf-url-label">地址</span>
      <input name="url" value="${esc(l.url || '')}" placeholder=""></label>
    <label class="field"><span>Token（可选，用于鉴权）</span>
      <input name="token" value="${esc(l.token || '')}" placeholder="留空则不校验"></label>
    <label class="check"><input type="checkbox" name="enabled" ${l.enabled !== false ? 'checked' : ''}><span>启用此监听</span></label>
    <p class="field-hint" id="lf-hint"></p>
  `;
}

function syncListenerForm() {
  const t = $('#lf-type').value;
  const server = ['http_server', 'http_sse', 'ws_server'].includes(t);
  $('#lf-server').style.display = server ? '' : 'none';
  $('#lf-path-wrap').style.display = server ? '' : 'none';
  const urlWrap = $('#lf-url-wrap');
  urlWrap.style.display = (t === 'http_client' || t === 'ws_client' || t === 'http_server') ? '' : 'none';
  const label = $('#lf-url-label');
  const input = urlWrap.querySelector('input');
  if (t === 'http_client') { label.textContent = '上报事件到 HTTP 地址'; input.placeholder = 'http://127.0.0.1:5700/'; }
  if (t === 'ws_client') { label.textContent = '连接 WebSocket 地址'; input.placeholder = 'ws://127.0.0.1:8080/onebot/v11/ws'; }
  if (t === 'http_server') { label.textContent = '事件上报地址（可选）'; input.placeholder = 'http://127.0.0.1:5700/'; }
  $('#lf-hint').textContent = TYPE_NOTE[t] || '';
}

function readListenerForm(form) {
  const data = new FormData(form);
  return {
    bot_id: Number(data.get('bot_id')),
    name: data.get('name') || '',
    type: data.get('type'),
    host: data.get('host') || '',
    port: Number(data.get('port') || 0),
    path: data.get('path') || '',
    url: (data.get('url') || '').trim(),
    post_url: '',
    token: data.get('token') || '',
    enabled: data.get('enabled') === 'on',
  };
}

function openAddListener() {
  if (!state.bots.length) { toast('请先添加并启动一个机器人', true); return; }
  openModal('添加 OneBot 监听', listenerFormHTML({ enabled: true }), '添加', async (form) => {
    await api('/api/listeners', { method: 'POST', body: JSON.stringify(readListenerForm(form)) });
    toast('已添加监听');
    await loadAll();
  });
  $('#lf-type').onchange = syncListenerForm;
  syncListenerForm();
}

function openEditListener(l) {
  openModal('编辑 OneBot 监听', listenerFormHTML(l), '保存', async (form) => {
    await api(`/api/listeners/${l.id}`, { method: 'PUT', body: JSON.stringify(readListenerForm(form)) });
    toast('已保存');
    await loadAll();
  });
  $('#lf-type').onchange = syncListenerForm;
  syncListenerForm();
}

/* ---------- group dialogs ---------- */

function openAddGroup() {
  if (!state.bots.length) { toast('请先添加一个机器人', true); return; }
  openModal('手动添加群 openid', `
    <label class="field"><span>机器人</span>
      <select name="bot_id" required>
        ${state.bots.map(b => `<option value="${b.id}">${esc(b.name || '机器人 ' + b.app_id)}</option>`).join('')}
      </select></label>
    <label class="field"><span>群 openid</span>
      <input name="open_id" required placeholder="从群 @ 消息事件中获取的 group_openid"></label>
    <label class="field"><span>备注名称（可选）</span>
      <input name="name" placeholder="例如 测试群"></label>
  `, '添加', async (form) => {
    const data = new FormData(form);
    await api('/api/groups', { method: 'POST', body: JSON.stringify({
      bot_id: Number(data.get('bot_id')),
      open_id: (data.get('open_id') || '').trim(),
      name: data.get('name') || '',
    }) });
    toast('已添加群');
    await loadAll();
  });
}

/* ---------- event wiring ---------- */

function wireEvents() {
  $('#modal-close').onclick = closeModal;
  $('#modal').addEventListener('click', (e) => { if (e.target.id === 'modal-backdrop' || e.target.id === 'modal') closeModal(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !$('#modal').hidden) closeModal(); });

  $('#nav').addEventListener('click', (e) => {
    const b = e.target.closest('button[data-view]');
    if (!b) return;
    state.view = b.dataset.view;
    renderView();
    if (state.view === 'logs') renderLogs();
  });

  $('#btn-add-bot').onclick = openAddBot;
  $('#btn-add-listener').onclick = openAddListener;
  $('#btn-add-group').onclick = openAddGroup;
  $('#btn-clear-log').onclick = () => { state.logs = []; renderLogs(); };
  $('#log-level').onchange = (e) => { state.logLevel = e.target.value; renderLogs(); };
  $('#test-bot').onchange = syncTestGroups;
  $('#group-bot-filter').onchange = renderGroups;

  document.addEventListener('click', async (e) => {
    const botBtn = e.target.closest('button[data-bot-action]');
    if (botBtn) {
      const id = Number(botBtn.dataset.id);
      const action = botBtn.dataset.botAction;
      const b = state.bots.find(x => x.id === id);
      if (action === 'edit') { openEditBot(b); return; }
      if (action === 'delete') {
        if (!confirm(`删除机器人「${b.name || b.app_id}」？其监听与群记录也会一并删除。`)) return;
        await api(`/api/bots/${id}`, { method: 'DELETE' }).then(() => { toast('已删除'); loadAll(); })
          .catch(err => toast(err.message, true));
        return;
      }
      botBtn.disabled = true;
      try {
        if (action === 'start') { await api(`/api/bots/${id}/start`, { method: 'POST' }); toast('正在连接 QQ 网关…'); }
        if (action === 'stop') { await api(`/api/bots/${id}/stop`, { method: 'POST' }); toast('已停止'); }
        if (action === 'check') {
          const r = await api(`/api/bots/${id}/check`, { method: 'POST' });
          toast(`凭据有效：${r.nickname || ''} (${r.self_id})`);
        }
        await loadAll();
      } catch (err) { toast(err.message, true); }
      finally { botBtn.disabled = false; }
      return;
    }

    const lBtn = e.target.closest('button[data-listener-action]');
    if (lBtn) {
      const id = Number(lBtn.dataset.id);
      const l = state.listeners.find(x => x.id === id);
      if (lBtn.dataset.listenerAction === 'edit') { openEditListener(l); return; }
      if (lBtn.dataset.listenerAction === 'delete') {
        if (!confirm(`删除监听「${l.name || TYPE_LABEL[l.type]}」？`)) return;
        await api(`/api/listeners/${id}`, { method: 'DELETE' }).then(() => { toast('已删除'); loadAll(); })
          .catch(err => toast(err.message, true));
      }
      return;
    }

    const gBtn = e.target.closest('button[data-group-action]');
    if (gBtn) {
      const id = Number(gBtn.dataset.id);
      const g = state.groups.find(x => x.id === id);
      if (gBtn.dataset.groupAction === 'rename') {
        openModal('重命名群', `<label class="field"><span>群名称</span><input name="name" value="${esc(g.name || '')}"></label>`, '保存', async (form) => {
          const name = form.querySelector('[name=name]').value;
          await api(`/api/groups/${id}`, { method: 'PUT', body: JSON.stringify({ name }) });
          toast('已保存'); await loadAll();
        });
        return;
      }
      if (gBtn.dataset.groupAction === 'delete') {
        if (!confirm('删除该群记录？仅影响适配器的记录，不影响 QQ 群本身。')) return;
        await api(`/api/groups/${id}`, { method: 'DELETE' }).then(() => { toast('已删除'); loadAll(); })
          .catch(err => toast(err.message, true));
      }
    }
  });

  $('#test-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const res = $('#test-result');
    res.className = 'result';
    res.textContent = '发送中…';
    try {
      const bot_id = Number($('#test-bot').value);
      const openid = ($('#test-openid').value || '').trim() || $('#test-group').value;
      const message = $('#test-message').value;
      if (!openid) throw new Error('请选择群或填写群 openid');
      if (!message.trim()) throw new Error('消息内容不能为空');
      // A bare numeric id is a QQ 群号, which the official API cannot address.
      if (/^\d+$/.test(openid)) {
        throw new Error(`${openid} 看起来是 QQ 群号，不是群 openid。请在群里 @ 机器人发一句话，让适配器自动记录 openid 后再试。`);
      }
      const r = await api('/api/test-message', { method: 'POST', body: JSON.stringify({
        bot_id, group_openid: openid, message, markdown: $('#test-markdown').checked,
      }) });
      res.className = 'result ok';
      res.textContent = `已发送，消息 ID：${r.message_id || '—'}`;
      toast('测试消息已发送');
    } catch (err) {
      res.className = 'result bad';
      res.textContent = err.message;
    }
  });
}

/* ---------- boot ---------- */

wireEvents();
connectWS();
loadAll();
setInterval(() => { if (!state.wsAlive) loadAll(); }, 15000);
