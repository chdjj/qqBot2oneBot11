// UI smoke test: loads the real console page in jsdom, points its fetch/WebSocket
// at the live adapter, and verifies each view renders with real API data.
import { JSDOM } from 'jsdom';
import fs from 'node:fs';
import vm from 'node:vm';

const BASE = 'http://127.0.0.1:8080';
let failures = 0;
function check(name, ok, detail = '') {
  if (!ok) failures++;
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

const html = fs.readFileSync('E:/qqbottoonebot/internal/api/web/index.html', 'utf8');
const css = fs.readFileSync('E:/qqbottoonebot/internal/api/web/app.css', 'utf8');
const js = fs.readFileSync('E:/qqbottoonebot/internal/api/web/app.js', 'utf8');

const dom = new JSDOM(html.replace('/static/app.js', ''), {
  url: BASE + '/',
  runScripts: 'outside-only',
  pretendToBeVisual: true,
});
const w = dom.window;
w.fetch = (input, init) => {
  const url = String(input).startsWith('http') ? String(input) : BASE + String(input);
  return fetch(url, init);
};
w.WebSocket = class {
  constructor() { setTimeout(() => this.onopen && this.onopen(), 5); }
  close() {}
};
w.confirm = () => true;
dom.window.document.head.appendChild(Object.assign(dom.window.document.createElement('style'), { textContent: css }));
vm.runInContext(js, dom.getInternalVMContext(), { filename: 'app.js' });

await sleep(1500);
const $ = (s) => dom.window.document.querySelector(s);
const $$ = (s) => Array.from(dom.window.document.querySelectorAll(s));

check('概览指标已渲染', $$('#metrics .metric').length >= 6, `metrics=${$$('#metrics .metric').length}`);
check('概览显示在线机器人', $('.row.live') !== null, ($('.row.live') || {}).textContent?.trim().slice(0, 40));
check('侧栏状态显示在线', $('#pulse-text').textContent.includes('在线'), $('#pulse-text').textContent);

// Switch to bots view
$$('.nav button').find(b => b.dataset.view === 'bots').click();
await sleep(200);
check('机器人视图列出机器人', $$('#bot-list .row').length === 1, `rows=${$$('#bot-list .row').length}`);
check('机器人行显示 AppID 与状态',
  $('#bot-list').textContent.includes('123456') && $('#bot-list').textContent.includes('已连接'));

// Listeners view
$$('.nav button').find(b => b.dataset.view === 'listeners').click();
await sleep(200);
const listenerRows = $$('#listener-list .row').length;
check('监听视图列出监听', listenerRows >= 3, `rows=${listenerRows}`);
check('监听显示类型与地址', $('#listener-list').textContent.includes('HTTP 服务器'));
check('监听显示 token 标记', $('#listener-list').textContent.includes('token'));

// Groups view
$$('.nav button').find(b => b.dataset.view === 'groups').click();
await sleep(200);
check('群视图显示已发现的群', $$('#group-list .row').length >= 1);
check('群视图显示 openid', $('#group-list').textContent.includes('A1B2C3D4E5F6'));

// Test view
$$('.nav button').find(b => b.dataset.view === 'test').click();
await sleep(200);
check('测试视图机器人下拉已填充', $('#test-bot').options.length === 1);
check('测试视图群下拉已填充', $('#test-group').options.length >= 1, `options=${$('#test-group').options.length}`);

// Logs view
$$('.nav button').find(b => b.dataset.view === 'logs').click();
await sleep(200);
check('日志视图有内容', $$('#log-stream .log-line').length > 0, `lines=${$$('#log-stream .log-line').length}`);

// Modal: add listener form shows correct fields per type
$$('.nav button').find(b => b.dataset.view === 'listeners').click();
$('#btn-add-listener').click();
await sleep(100);
check('添加监听模态框打开', !$('#modal').hidden);
const typeSel = $('#lf-type');
typeSel.value = 'ws_client';
typeSel.dispatchEvent(new dom.window.Event('change'));
await sleep(50);
check('WS 客户端类型隐藏端口字段', $('#lf-server').style.display === 'none');
check('WS 客户端类型显示连接地址', $('#lf-url-label').textContent.includes('WebSocket'), $('#lf-url-label').textContent);
typeSel.value = 'http_server';
typeSel.dispatchEvent(new dom.window.Event('change'));
await sleep(50);
check('HTTP 服务器类型显示端口字段', $('#lf-server').style.display !== 'none');
$('#modal-close').click();
await sleep(50);
check('关闭模态框', $('#modal').hidden);

console.log(`\n${failures === 0 ? 'ALL UI CHECKS PASSED' : failures + ' FAILED'}`);
process.exit(failures ? 1 : 0);
