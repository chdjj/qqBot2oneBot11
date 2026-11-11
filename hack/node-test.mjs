// Node-based integration checks for the OneBot transports.
// Usage: node test_onebot.mjs
// Assumes the adapter console runs at 127.0.0.1:8080 with a bot already added.
import { WebSocket } from 'ws';

const BASE = process.env.BASE || 'http://127.0.0.1:8080';
// The console API requires the admin token printed at startup.
const TOKEN = process.env.ADMIN_TOKEN || '';
const AUTH = TOKEN ? { Authorization: 'Bearer ' + TOKEN } : {};
const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}

async function j(path, opts = {}) {
  const r = await fetch(BASE + path, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...AUTH, ...(opts.headers || {}) },
  });
  const t = await r.text();
  let b; try { b = JSON.parse(t); } catch { b = t; }
  if (!r.ok) throw new Error(b?.error || t);
  return b;
}

const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function main() {
  // Clean slate for listeners 2..n created by earlier curl runs.
  const existing = await j('/api/listeners');
  for (const l of existing) await j(`/api/listeners/${l.id}`, { method: 'DELETE' });
  await sleep(1500);

  // --- HTTP server listener with token ---
  const httpL = await j('/api/listeners', { method: 'POST', body: JSON.stringify({
    bot_id: 1, name: 'HTTP服务器', type: 'http_server',
    host: '127.0.0.1', port: 8081, token: 'tk-http', enabled: true,
  }) });
  await sleep(1200);

  const noAuth = await fetch('http://127.0.0.1:8081/', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'get_login_info', echo: 1 }),
  });
  const noAuthBody = await noAuth.json().catch(() => null);
  // OneBot implementations answer HTTP 200 with retcode 403 so clients that
  // drop non-2xx bodies still see the error.
  check('HTTP 拒绝无 token 请求',
    noAuth.status === 200 && noAuthBody && noAuthBody.retcode === 403,
    `status=${noAuth.status} body=${JSON.stringify(noAuthBody)}`);

  const withAuth = await fetch('http://127.0.0.1:8081/', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer tk-http' },
    body: JSON.stringify({ action: 'get_login_info', echo: 2 }),
  });
  const loginInfo = await withAuth.json();
  check('HTTP 带 token 可调用 API', loginInfo.status === 'ok' && loginInfo.data?.user_id,
    JSON.stringify(loginInfo));

  // --- WS server ---
  const wsL = await j('/api/listeners', { method: 'POST', body: JSON.stringify({
    bot_id: 1, name: 'WS服务器', type: 'ws_server',
    host: '127.0.0.1', port: 8082, token: 'tk-ws', enabled: true,
  }) });
  await sleep(1200);

  const wsMessages = [];
  const ws = new WebSocket('ws://127.0.0.1:8082/api', {
    headers: { Authorization: 'Bearer tk-ws', 'X-Client-Role': 'Universal', 'X-Self-ID': '8888888' },
  });
  await new Promise((res, rej) => { ws.on('open', res); ws.on('error', rej); });
  ws.on('message', (m) => wsMessages.push(JSON.parse(m.toString())));
  await sleep(300);
  check('WS 连接后收到 lifecycle 事件',
    wsMessages.some(m => m.post_type === 'meta_event' && m.meta_event_type === 'lifecycle'));

  // Discover the group first (openid is only learnable from an inbound event).
  await j('/api/__test/inject', { method: 'POST', body: JSON.stringify({ type: 'GROUP_AT_MESSAGE_CREATE' }) }).catch(() => {});
  await sleep(800);
  check('WS 收到群消息事件',
    wsMessages.some(m => m.post_type === 'message' && m.message_type === 'group'),
    `messages=${wsMessages.length}`);

  const groups = await j('/api/groups');
  const groupId = groups[0]?.num_id;

  ws.send(JSON.stringify({ action: 'send_group_msg', params: { group_id: groupId, message: '来自 WS 的回复' }, echo: 'ws-1' }));
  await sleep(1200);
  const wsResp = wsMessages.find(m => m.echo === 'ws-1');
  check('WS 动作调用返回结果', wsResp && wsResp.status === 'ok', JSON.stringify(wsResp));

  const sent = await fetch('http://127.0.0.1:19090/debug/sent').then(r => r.json()).catch(() => []);
  check('动作已转发到 QQ 上游且为被动回复',
    Array.isArray(sent) && sent.some(s => s.includes('/v2/groups/') && s.includes('msg_id')),
    JSON.stringify(sent?.slice(-1)));
  // --- SSE ---
  const sseL = await j('/api/listeners', { method: 'POST', body: JSON.stringify({
    bot_id: 1, name: 'SSE服务器', type: 'http_sse',
    host: '127.0.0.1', port: 8083, enabled: true,
  }) });
  await sleep(1200);
  const ctrl = new AbortController();
  const sseLines = [];
  fetch('http://127.0.0.1:8083/_events', { signal: ctrl.signal })
    .then(async (r) => {
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        sseLines.push(dec.decode(value));
      }
    }).catch(() => {});
  await sleep(600);
  check('SSE 建立连接并发送 ready 事件',
    sseLines.join('').includes('event: ready'), sseLines.join('').slice(0, 120));
  ctrl.abort();

  // --- Reverse WS ---
  const { WebSocketServer } = await import('ws');
  const rws = new WebSocketServer({ port: 8090, path: '/ws' });
  let reverseConn = null;
  const reverseGot = [];
  rws.on('connection', (c, req) => {
    reverseConn = c;
    const h = req.headers;
    check('反向 WS 携带 X-Self-ID 与角色头',
      h['x-self-id'] === '8888888' && h['x-client-role'] === 'Universal', JSON.stringify(h));
    c.on('message', (m) => reverseGot.push(JSON.parse(m.toString())));
  });
  await j('/api/listeners', { method: 'POST', body: JSON.stringify({
    bot_id: 1, name: '反向WS', type: 'ws_client',
    url: 'ws://127.0.0.1:8090/ws', token: 'rev-token', enabled: true,
  }) });
  // The reverse client backs off on the first failed dial, so poll briefly.
  for (let i = 0; i < 20 && !reverseConn; i++) await sleep(500);
  check('反向 WS 已连接', reverseConn !== null, reverseConn ? '' : 'timed out after 10s');

  if (reverseConn) {
    reverseConn.send(JSON.stringify({ action: 'get_version_info', echo: 'rev-1' }));
    await sleep(1200);
    const r = reverseGot.find(m => m.echo === 'rev-1');
    check('反向 WS 动作可往返', r && r.status === 'ok', JSON.stringify(r));
  }

  const failed = results.filter(r => !r.ok);
  console.log(`\n${results.length - failed.length}/${results.length} passed`);
  ws.close();
  rws.close();
  await sleep(200);
  process.exit(failed.length ? 1 : 0);
}

main().catch(e => { console.error('harness error:', e); process.exit(2); });
