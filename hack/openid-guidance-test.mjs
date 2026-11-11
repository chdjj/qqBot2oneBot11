// Checks the "how do I get a group openid" guidance in both states.
import { JSDOM } from 'jsdom';
import fs from 'node:fs';
import vm from 'node:vm';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'internal', 'api', 'web') + path.sep;

const BASE = process.env.OPENID_BASE || 'http://127.0.0.1:18083';
const html = fs.readFileSync(WEB + 'index.html', 'utf8');
const dom = new JSDOM(html.replace('/static/app.js', ''), { url: BASE + '/', runScripts: 'outside-only', pretendToBeVisual: true });
const w = dom.window;
w.fetch = (i, o) => fetch(String(i).startsWith('http') ? i : BASE + i, o);
w.WebSocket = class { constructor() { setTimeout(() => this.onopen && this.onopen(), 5); } close() {} };
w.confirm = () => true;
vm.runInContext(fs.readFileSync(WEB + 'app.js', 'utf8'), dom.getInternalVMContext(), { filename: 'app.js' });
await new Promise(r => setTimeout(r, 1500));

const $ = (s) => w.document.querySelector(s);
const clean = (el) => el ? el.textContent.replace(/\s+/g, ' ').trim() : '';

w.document.querySelectorAll('.nav button').forEach(b => { if (b.dataset.view === 'test') b.click(); });
await new Promise(r => setTimeout(r, 250));
console.log('目标群下拉:', $$('#test-group').map(o => o.textContent.trim()));
console.log('指引框隐藏(有群时):', $('#test-group-hint').hidden);

// Front-end guard: a bare QQ 群号 must be rejected before any request.
$('#test-openid').value = '1075750473';
$('#test-message').value = 'test';
$('#test-form').dispatchEvent(new w.Event('submit', { cancelable: true, bubbles: true }));
await new Promise(r => setTimeout(r, 400));
console.log('前端拦截结果:', clean($('#test-result')));

function $$(s) { return Array.from(w.document.querySelectorAll(s)); }
process.exit(0);
