// Dumps the rendered DOM of each view so layout can be reviewed as text.
import { JSDOM } from 'jsdom';
import fs from 'node:fs';
import vm from 'node:vm';

const BASE = process.env.BASE || 'http://127.0.0.1:8080';
const html = fs.readFileSync('E:/qqbottoonebot/internal/api/web/index.html', 'utf8');
const dom = new JSDOM(html.replace('/static/app.js', ''), { url: BASE + '/', runScripts: 'outside-only', pretendToBeVisual: true });
const w = dom.window;
w.fetch = (i, o) => fetch(String(i).startsWith('http') ? i : BASE + i, o);
w.WebSocket = class { constructor() { setTimeout(() => this.onopen && this.onopen(), 5); } close() {} };
w.confirm = () => true;
vm.runInContext(fs.readFileSync('E:/qqbottoonebot/internal/api/web/app.js', 'utf8'), dom.getInternalVMContext(), { filename: 'app.js' });
await new Promise(r => setTimeout(r, 1400));

const $ = (s) => w.document.querySelector(s);
const clean = (el) => el ? el.textContent.replace(/\s+/g, ' ').trim() : '';
const view = process.argv[2] || 'overview';
w.document.querySelectorAll('.nav button').forEach(b => { if (b.dataset.view === view) b.click(); });
await new Promise(r => setTimeout(r, 250));
console.log(`\n===== [${view}] ${clean($('#view-title'))} — ${clean($('#view-hint'))} =====`);
const section = w.document.querySelector(`.view[data-view="${view}"]`);
section.querySelectorAll('.panel, .notes, .metric-grid, .two-col, .form').forEach(el => {
  const cls = el.className.split(' ')[0];
  console.log(`\n<${cls}> ${clean(el).slice(0, 600)}`);
});
process.exit(0);
