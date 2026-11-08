#!/usr/bin/env node
// Drives the desktop app's page in a headless Chrome (nothing appears on
// screen), against the real daemon and network of scripts/gui_stack.sh, and
// checks the DOM after each step; saves screenshots. Node 22, no dependencies.
//
//   scripts/gui_stack.sh start /tmp/guitest
//   node scripts/gui_ui_test.mjs /tmp/guitest /tmp/guitest/shots
//   scripts/gui_stack.sh stop /tmp/guitest
import { spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { setTimeout as sleep } from 'node:timers/promises';

const dir = process.argv[2];
const shots = process.argv[3] || dir + '/shots';
mkdirSync(shots, { recursive: true });
const env = Object.fromEntries(readFileSync(dir + '/env', 'utf8').trim().split('\n').map(l => { const i = l.indexOf('='); return [l.slice(0, i), l.slice(i + 1)]; }));
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9334;
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${port}`,
  `--user-data-dir=${dir}/chrome-profile`, '--window-size=1040,760', 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => proc.kill());

let ws, id = 0; const pending = new Map(); const problems = [];
async function connect() {
  for (let i = 0; i < 50 && !ws; i++) {
    try {
      const page = (await (await fetch(`http://127.0.0.1:${port}/json`)).json()).find(t => t.type === 'page');
      if (page) ws = new WebSocket(page.webSocketDebuggerUrl);
    } catch (e) { /* not up yet */ }
    if (!ws) await sleep(200);
  }
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  ws.onmessage = m => {
    const d = JSON.parse(m.data);
    if (d.id && pending.has(d.id)) { const { res, rej } = pending.get(d.id); pending.delete(d.id); d.error ? rej(new Error(d.error.message)) : res(d.result); }
    // CSP violations and script errors fail the run. Failed requests the app
    // expects and handles (a 4xx from the API) are not page errors.
    else if (d.method === 'Runtime.exceptionThrown') problems.push('exception: ' + JSON.stringify(d.params.exceptionDetails).slice(0, 300));
    else if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error' && d.params.entry.source !== 'network') problems.push('console: ' + d.params.entry.text);
  };
}
const cdp = (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
async function ev(expr) {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error in ' + expr + ': ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}
async function shot(name) {
  const bad = await ev(`document.body.innerText.match(/\\b(null|undefined|NaN)\\b/)?.[0] ?? ''`);
  if (bad) throw new Error(`the page at ${name} shows a stray "${bad}":\n` + await ev('document.body.innerText'));
  const r = await cdp('Page.captureScreenshot', { format: 'png' }); writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64'));
}
async function waitFor(expr, ms = 15000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { if (await ev(`!!(${expr})`)) return; await sleep(100); }
  throw new Error('timeout waiting for ' + expr + '\n--- page text:\n' + await ev('document.body.innerText'));
}
const q = s => JSON.stringify(s);
const el = s => `document.querySelector(${q(s)})`;
const has = s => ev(`!!${el(s)}`);
const text = s => ev(`${el(s)}?.textContent ?? ''`);
// Types the way a user does, so that the page's own input element keeps the value.
const fill = (s, v) => ev(`(() => { const e = ${el(s)}; e.focus(); e.value = ${q(v)}; e.dispatchEvent(new Event('input', { bubbles: true })); })()`);
const click = s => ev(`${el(s)}.click()`);
let passed = 0;
function ok(cond, what) { if (!cond) throw new Error('FAILED: ' + what); passed++; console.log('  ok  ' + what); }
async function errorIs(needle, what) { await waitFor(`${el('#error')}?.textContent.includes(${q(needle)})`); ok(true, what); }

const EMAIL = 'Desk.User+gui@Example.com', NORMAL = EMAIL.toLowerCase(), PASSWORD = 'correct horse battery';

process.env.NODE_NO_WARNINGS = '1';
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0'; // the test calls the self-signed portal directly
// A fake PostHog: the daemon of gui_stack.sh is built to report here.
const collected = [];
const collector = createServer((req, res) => {
  let body = ''; req.on('data', c => { body += c; });
  req.on('end', () => { collected.push({ path: req.url, body }); res.end('{"status":1}'); });
}).listen(18999, '127.0.0.1');
collector.unref();
const events = () => collected.flatMap(c => JSON.parse(c.body).batch);
const eventNames = () => events().map(e => e.event);

await connect();
await cdp('Runtime.enable'); await cdp('Log.enable'); await cdp('Page.enable');
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "try { localStorage.setItem('st_lang', 'zh-CN'); } catch (e) {}" }); // these checks read the Chinese source text
await cdp('Page.navigate', { url: env.DAEMON + '/' });

console.log('built-in portal');
// The page never asks for an address: it connects by itself and lands on sign-in.
await waitFor(el('#signin-form'));
ok((await text('#env-banner')).includes('DEV'), 'a dev build says so on the page');
ok((await ev('document.title')).includes('DEV'), 'and in the title');
ok(!(await has('#portal')), 'there is no portal address field');
ok(!(await has('#portal-change')), 'there is no way to change the portal');
ok(await text('.tagline') !== '', 'the logged-out app opens straight on sign-in');
await shot('01-signin');

console.log('certificate confirmation');
await waitFor(el('#signin-form'));
ok(await text('#fingerprint') === env.PIN, 'the fingerprint shown is the real certificate fingerprint');
ok(await ev(`${el('#submit')}.disabled`), 'nothing can be submitted before the certificate is confirmed');
await shot('03-confirm-cert');
await click('#cert-ok');
await waitFor(`!${el('#submit')}.disabled`);
ok(true, 'confirming enables the form');

console.log('registration');
await click('#tab-register');
await waitFor(el('#email'));
ok((await text('#signup-credit')).includes('5'), 'the sign-up credit is shown');
ok(await ev(`${el('#cert-ok')}.checked`), 'the confirmation survives switching tabs');
await fill('#email', 'not-an-email'); await fill('#password', PASSWORD); await fill('#password2', PASSWORD); await click('#submit');
await errorIs('正确的邮箱', 'an invalid email is refused in the page');
await fill('#email', 'a@b.c'); await fill('#password', 'short'); await fill('#password2', 'short'); await click('#submit');
await errorIs('至少 8 位', 'a short password is refused');
await fill('#password', PASSWORD); await fill('#password2', PASSWORD + 'x'); await click('#submit');
await errorIs('不一致', 'mismatched passwords are refused');
await fill('#email', 'x@y..z'); await fill('#password2', PASSWORD); await click('#submit');
await errorIs('邮箱地址格式不正确', 'an invalid email the server catches is explained');
await shot('04-register-error');
await fill('#email', EMAIL); await fill('#password', PASSWORD); await fill('#password2', PASSWORD);
await shot('05-register');
await click('#submit');

console.log('dashboard');
await waitFor(el('#tiles .tile'));
ok(await text('#who') === NORMAL, 'the app shows the normalized email');
ok((await text('#tile-available .value')) === '5', 'the sign-up credit arrived: 5');
await waitFor(`${el('#conn-state')}.textContent === '已连接'`, 30000);
ok(true, 'it joined the real network through the relay');
await waitFor(`${el('#network-body')}.textContent.includes('relay-local')`);
ok((await text('#network-body')).includes('当前使用'), 'the relay in use is marked');
await waitFor(`${el('#coord-line')}?.textContent === '协调器已连接'`, 20000);
ok(true, 'the coordinator is connected');
await waitFor(`${el('#models-body')}.textContent.includes('mock-fast')`, 30000);
ok(true, "the seller's model is listed");
ok(await text('#endpoint-url') === env.DAEMON + '/v1', 'the local endpoint is shown');
ok((await text('#devices-body')).includes('（本机）'), 'this device is marked in the list');
ok(await has('#jobs-empty'), 'no jobs yet');
ok(!(await ev('document.documentElement.outerHTML')).includes(PASSWORD), 'the password is gone from the page');
await shot('06-dashboard');

console.log('buying shows up');
let chatStatus = 0;
for (let i = 0; i < 20 && chatStatus !== 200; i++) {
  const r = await fetch(env.DAEMON + '/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ model: 'mock-fast', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }) });
  chatStatus = r.status; await r.text();
  if (chatStatus !== 200) await sleep(1000);
}
ok(chatStatus === 200, 'a request through the local endpoint succeeds');
await waitFor(`${el('#jobs-body')}.textContent.includes('已完成')`, 40000);
ok(true, 'the job appears in the list');
const after = await text('#tile-available .value');
ok(Number(after.replaceAll(',', '')) < 5, `the balance went down (${after})`);
ok((await text('#tile-bought .sub')).startsWith('1 次'), 'usage counts one purchase');
await shot('07-after-buying');

console.log('untrusted text stays text');
const config = JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8'));
const hostile = '<img src=x onerror=window.__xss=1>';
const portalApi = async (method, path, body, token = config.token) => {
  // The test talks to the self-signed portal directly.
  return fetch(env.PORTAL + path, { method, headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
};
const peer = (await ev(`fetch('/api/v1/session').then(r => r.json()).then(s => s.peer)`));
ok((await portalApi('PATCH', '/api/v1/devices/' + peer, { name: hostile })).ok, 'a device gets a hostile name');
await waitFor(`${el('#devices-body')}.textContent.includes('<img')`, 25000);
ok(!(await has('#devices-body img')) && !(await ev('window.__xss === 1')), 'it shows as text, nothing runs');

ok((await ev(`${el('#app-version')}.textContent`)).length > 2, 'the app shows its version');
console.log('layouts');
await cdp('Emulation.setDeviceMetricsOverride', { width: 480, height: 760, deviceScaleFactor: 1, mobile: false });
await sleep(300);
ok(await ev('document.documentElement.scrollWidth <= window.innerWidth'), 'no sideways scrolling at the smallest window');
ok(await ev(`${el('#logout')}.getBoundingClientRect().height < 40`), 'the logout button stays on one line');
await shot('08-narrow');
await cdp('Emulation.clearDeviceMetricsOverride');
await cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });
await sleep(200);
await shot('09-dark');
await cdp('Emulation.setEmulatedMedia', { features: [] });

console.log('feedback');
await click('#feedback-send');
await waitFor(`${el('#feedback-result')}.textContent === '请填写反馈内容'`);
ok(true, 'empty feedback is stopped in the page');
await fill('#feedback-text', 'it works');
await click('#feedback-send');
await waitFor(`${el('#feedback-result')}.textContent.includes('已提交')`);
ok(await ev(`${el('#feedback-text')}.value`) === '', 'feedback is sent and the box is cleared');
const sent = await ev(`fetch('/api/v1/feedback/mine').then(r => r.json())`);
ok(sent.feedback[0].message === 'it works' && sent.feedback[0].source === 'client' && sent.feedback[0].os.includes('/'), 'the portal has it, marked as from the client');
await shot('10b-feedback');

console.log('reload and logout');
await cdp('Page.reload'); await waitFor(el('#tiles .tile'));
ok(await text('#who') === NORMAL, 'a reload stays logged in');
await click('#logout');
await waitFor(el('#signin-form'));
ok(await ev(`${el('#account')}.value`) === NORMAL, 'after logout the email is pre-filled');
ok(!(await has('#cert')) , 'the confirmed certificate is not asked about again');
const oldToken = config.token;
ok((await portalApi('GET', '/api/v1/me', null, oldToken)).status === 401, 'the token is revoked at the portal');
ok(JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).token === undefined, 'and gone from config.json');

console.log('login errors');
await fill('#password', 'wrong-password'); await click('#submit');
await errorIs('账号或密码错误', 'a wrong password is reported');
ok(await has('#signin-form'), 'and stays on the form');
await click('#tab-register');
await waitFor(el('#email'));
await fill('#email', EMAIL); await fill('#password', PASSWORD); await fill('#password2', PASSWORD); await click('#submit');
await errorIs('已经注册', 'registering a taken address says so');
await shot('10-email-taken');
await click('#tab-login');
await waitFor(el('#account'));
await fill('#account', EMAIL.toUpperCase()); await fill('#password', PASSWORD); await click('#submit');
await waitFor(el('#tiles .tile'));
ok(await text('#who') === NORMAL, 'logging in works, any capitalization');
await waitFor(`${el('#conn-state')}.textContent === '已连接'`, 30000);
ok(true, 'and rejoins the network');
await shot('11-logged-in-again');

console.log('login revoked elsewhere');
const token2 = JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).token;
ok((await portalApi('POST', '/api/v1/logout', null, token2)).ok, 'the token is revoked behind the app');
await waitFor(el('#notice'), 25000);
ok((await text('#notice')).includes('登录已失效'), 'the app returns to sign-in and says why');
await shot('12-expired');
await fill('#password', PASSWORD); await click('#submit');
await waitFor(el('#tiles .tile'));
ok(true, 'signing in again works');

console.log('usage statistics');
await waitFor(el('#telemetry-toggle'));
ok(await ev(`${el('#telemetry-toggle')}.checked`), 'the setting is shown, on by default');
await sleep(500);
for (const name of ['sharetoken_signed_in', 'sharetoken_network_joined', 'sharetoken_signed_out', 'sharetoken_sign_in_failed']) {
  ok(eventNames().includes(name), 'collected ' + name);
}
ok(collected.every(c => c.path === '/batch/' && JSON.parse(c.body).api_key === 'phc_test_key'), 'events use the PostHog batch format and the built-in key');
const raw = collected.map(c => c.body).join('\n');
const lastConfig = JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8'));
const sessionNow = await ev(`fetch('/api/v1/session').then(r => r.json())`);
for (const [what, secret] of [['email', NORMAL], ['password', PASSWORD], ['token', lastConfig.token], ['device id', sessionNow.peer], ['portal address', env.PORTAL], ['device name', hostile]]) {
  ok(!raw.includes(secret), 'events do not contain the ' + what);
}
ok(new Set(events().map(e => e.distinct_id)).size === 1 && events()[0].distinct_id.startsWith('install:'), 'one anonymous install ID');
await click('#telemetry-toggle');
await waitFor(`!${el('#telemetry-toggle')}.disabled && !${el('#telemetry-toggle')}.checked`);
const before = collected.length;
await cdp('Page.reload'); await waitFor(el('#telemetry-toggle'));
ok(!(await ev(`${el('#telemetry-toggle')}.checked`)), 'turning it off is remembered');
await click('#logout'); await waitFor(el('#signin-form'));
await fill('#password', PASSWORD); await click('#submit'); await waitFor(el('#tiles .tile'));
await sleep(1500);
ok(collected.length === before, 'with it off, logging out and in again sends nothing');
await shot('13-telemetry-off');

console.log('portal goes away');
process.kill(Number(env.PORTAL_PID));
await waitFor(`${el('#banner-text')}?.textContent.includes('连不上门户')`, 25000);
ok(true, 'the app says the portal is unreachable');
ok(await text('#who') === NORMAL && (await text('#tile-available .value')) !== '', 'and keeps showing the last account data, still logged in');
await shot('14-portal-down');

ok(problems.length === 0, 'no script errors or CSP violations: ' + problems.join('; '));
console.log(`\nPASS: ${passed} checks`);
process.exit(0);
