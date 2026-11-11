// Verifies the login gate and the intents checkbox widget.
import { JSDOM } from 'jsdom';
import fs from 'node:fs';
import vm from 'node:vm';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'internal', 'api', 'web') + path.sep;
const BASE = 'http://127.0.0.1:8080';
let failures = 0;
const check = (name, ok, detail = '') => {
  if (!ok) failures++;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
};
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

function makeDom(token) {
  const html = fs.readFileSync(WEB + 'index.html', 'utf8');
  const dom = new JSDOM(html.replace('/static/app.js', ''), { url: BASE + '/', runScripts: 'outside-only', pretendToBeVisual: true });
  const w = dom.window;
  w.fetch = (i, o = {}) => {
    const url = String(i).startsWith('http') ? i : BASE + i;
    const headers = { ...(o.headers || {}) };
    if (token) headers.Authorization = 'Bearer ' + token;
    return fetch(url, { ...o, headers });
  };
  w.WebSocket = class { constructor() { setTimeout(() => this.onopen && this.onopen(), 5); } close() {} };
  w.confirm = () => true;
  vm.runInContext(fs.readFileSync(WEB + 'app.js', 'utf8'), dom.getInternalVMContext(), { filename: 'app.js' });
  return w;
}

// --- 1. no token: the login screen must appear and the shell stay hidden ---
{
  const w = makeDom('');
  await sleep(1200);
  const doc = w.document;
  check('未登录时显示登录页', doc.querySelector('#login').hidden === false);
  check('未登录时隐藏控制台', doc.querySelector('.shell').style.display === 'none');
  w.close?.();
}

// --- 2. valid token: console opens, login hidden ---
{
  const w = makeDom(process.env.ADMIN_TOKEN);
  await sleep(1400);
  const doc = w.document;
  check('token 有效时进入控制台', doc.querySelector('#login').hidden === true);
  check('控制台已显示', doc.querySelector('.shell').style.display === '');
  w.close?.();
}

// --- 3. intents checkboxes render and synthesize the mask ---
{
  const w = makeDom(process.env.ADMIN_TOKEN);
  await sleep(1500);
  const doc = w.document;
  doc.querySelectorAll('.nav button').forEach(b => { if (b.dataset.view === 'bots') b.click(); });
  await sleep(150);
  doc.querySelector('#btn-add-bot').click();
  await sleep(200);

  const boxes = doc.querySelectorAll('input[name=intent_bit]');
  check('intents 复选框已渲染', boxes.length >= 10, `boxes=${boxes.length}`);
  const checked = Array.from(boxes).filter(b => b.checked).map(b => b.value);
  check('默认勾选 25', checked.length === 1 && checked[0] === '25', `checked=${checked.join(',')}`);
  check('合成值显示为 33554432',
    doc.querySelector('#intents-preview').textContent.trim() === '33554432',
    doc.querySelector('#intents-preview').textContent.trim());

  // Toggle bit 30 and confirm the preview recomputes.
  const box30 = Array.from(boxes).find(b => b.value === '30');
  box30.checked = true;
  box30.dispatchEvent(new w.Event('change'));
  await sleep(80);
  const expected = String((1 << 25) | (1 << 30));
  check('勾选 30 后合成值更新',
    doc.querySelector('#intents-preview').textContent.trim() === expected,
    doc.querySelector('#intents-preview').textContent.trim());

  // Uncheck everything: the summary must warn instead of showing 0 silently.
  boxes.forEach(b => { b.checked = false; b.dispatchEvent(new w.Event('change')); });
  await sleep(80);
  const txt = doc.querySelector('#intents-preview').textContent;
  check('全不选时给出警告', txt.includes('不会收到任何事件'), txt);
  w.close?.();
}

console.log(`\n${failures === 0 ? 'ALL LOGIN/INTENTS CHECKS PASSED' : failures + ' FAILED'}`);
process.exit(failures ? 1 : 0);
