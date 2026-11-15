#!/usr/bin/env node
// Drives the desktop app's page in a headless Chrome (nothing appears on
// screen), against the real daemon and network of scripts/test/gui_stack.sh, and
// checks the DOM after each step; saves screenshots. Node 22, no dependencies.
//
//   scripts/test/gui_stack.sh start /tmp/guitest
//   node scripts/test/gui_ui_test.mjs /tmp/guitest /tmp/guitest/shots
//   scripts/test/gui_stack.sh stop /tmp/guitest
import { execFileSync, spawn } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { setTimeout as sleep } from 'node:timers/promises';

const dir = process.argv[2];
const shots = process.argv[3] || dir + '/shots';
mkdirSync(shots, { recursive: true });
const env = Object.fromEntries(readFileSync(dir + '/env', 'utf8').trim().split('\n').map(l => { const i = l.indexOf('='); return [l.slice(0, i), l.slice(i + 1)]; }));
// The OpenAI endpoint's key and switch (internal/client/apikey.go), as the daemon keeps them.
const apiKeyFile = () => JSON.parse(readFileSync(env.DESKTOP_HOME + '/apikey.json', 'utf8'));
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9334;
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${port}`,
  `--user-data-dir=${dir}/chrome-profile`, '--window-size=880,600', 'about:blank'], { stdio: 'ignore' });
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
async function fullShot(name) {
  const r = await cdp('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true }); writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64'));
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

const EMAIL = 'Desk.User+gui@Example.com', NORMAL = EMAIL.toLowerCase();

// Mail: nothing delivers it, the portal's outbox keeps it (scripts/test/gui_stack.sh).
// nextCode waits for a code newer than the last one read and lifts the
// per-address cooldown, as a minute passing would.
const db = readFileSync(dir + '/dsn', 'utf8').trim().replace(/.*\//, '');
const sql = query => execFileSync(new URL('./local-mysql.sh', import.meta.url).pathname, ['sql', db, query], { encoding: 'utf8' }).trim();
let lastMail = 0;
async function nextCode(email) {
  for (let i = 0; i < 100; i++) {
    const [id, subject] = (sql(`SELECT id, subject FROM mail_outbox WHERE to_addr = '${email}' ORDER BY id DESC LIMIT 1`) || '0\t').split('\t');
    if (Number(id) > lastMail) {
      lastMail = Number(id);
      sql('UPDATE mail_codes SET created_at = created_at - 7200');
      return subject.slice(subject.lastIndexOf(' ') + 1);
    }
    await sleep(100);
  }
  throw new Error('no new code mailed to ' + email);
}
const wrongCode = c => c === '000000' ? '000001' : '000000';
// TOTP (RFC 6238) for the authenticator a user sets up.
function base32(s) {
  let bits = '', out = [];
  for (const ch of s.replace(/=+$/, '')) bits += 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(ch).toString(2).padStart(5, '0');
  for (let i = 0; i + 8 <= bits.length; i += 8) out.push(parseInt(bits.slice(i, i + 8), 2));
  return Buffer.from(out);
}
function totp(secret, t = Date.now()) {
  const msg = Buffer.alloc(8); msg.writeBigUInt64BE(BigInt(Math.floor(t / 30000)));
  const h = createHmac('sha1', secret).update(msg).digest(), o = h[19] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, '0');
}
const nextStep = () => sleep(30000 - Date.now() % 30000 + 100); // a TOTP code works once

process.env.NODE_NO_WARNINGS = '1';
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0'; // the test calls the self-signed portal directly

await connect();
await cdp('Runtime.enable'); await cdp('Log.enable'); await cdp('Page.enable');
// The window's inside at its default size, less a title bar.
const WINDOW = { width: 880, height: 568, deviceScaleFactor: 2, mobile: false };
await cdp('Emulation.setDeviceMetricsOverride', WINDOW);
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "try { localStorage.setItem('st_lang', 'zh-CN'); } catch (e) {}" }); // these checks read the Chinese source text
// The checks' own calls of the local API, with the key the page keeps.
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "window.kfetch = (p, o = {}) => fetch(p, { ...o, headers: { ...(o.headers || {}), Authorization: 'Bearer ' + sessionStorage.getItem('st_local_key') } });" });
// As `sharetoken daemon` prints it: the page takes the key from the fragment
// and takes it out of the address.
await cdp('Page.navigate', { url: env.DAEMON + '/#key=' + env.DAEMON_KEY });

console.log('built-in portal');
// The page never asks for an address: it connects by itself and lands on sign-in.
await waitFor(el('#signin-form'));
ok(await ev('location.hash') === '' && await ev(`sessionStorage.getItem('st_local_key')`) === env.DAEMON_KEY, 'the page keeps the key and takes it out of the address');
ok((await text('#env-banner')).includes('DEV'), 'a dev build says so on the page');
ok((await ev('document.title')).includes('DEV'), 'and in the title');
ok(!(await has('#portal')), 'there is no portal address field');
ok(!(await has('#portal-change')), 'there is no way to change the portal');
ok(await text('.tagline') !== '', 'the logged-out app opens straight on sign-in');
ok(await has('#email') && await has('#mail-code') && !(await has('#password')) && !(await has('[role=tablist]')), 'one form to sign in or up: an email and a code, no password, no tabs');
ok((await text('#signin-hint')).includes('不需要密码'), 'the page says no password is needed');
await shot('01-signin');

console.log('certificate confirmation');
await waitFor(el('#signin-form'));
ok(await text('#fingerprint') === env.PIN, 'the fingerprint shown is the real certificate fingerprint');
ok(await ev(`${el('#submit')}.disabled`), 'nothing can be submitted before the certificate is confirmed');
await shot('03-confirm-cert');
await click('#cert-ok');
await waitFor(`!${el('#submit')}.disabled`);
ok(true, 'confirming enables the form');

console.log('sign-up');
await fill('#email', 'not-an-email'); await click('#send-code');
await errorIs('正确的邮箱', 'an invalid email is refused in the page');
await ev(`${el('#error')}.remove()`); // the portal's answer reads the same
await fill('#email', 'x@y..z'); await click('#send-code');
await errorIs('正确的邮箱', 'an invalid email the portal catches is explained');
await fill('#email', EMAIL); await click('#submit');
await errorIs('请先发送验证码', 'signing in without a code asks to send one first');
await click('#send-code');
await waitFor(el('#new-account'));
ok((await text('#new-account')).includes('自动创建') && (await text('#new-account')).includes('5'), 'a new address is told an account will be created, with the sign-up credit');
ok((await text('#send-code')).includes('秒后可重发') && await ev(`${el('#send-code')}.disabled`), 'the button counts down to a resend');
ok(await ev(`${el('#cert-ok')}.checked`), 'the certificate confirmation stays');
const code1 = await nextCode(NORMAL);
await fill('#mail-code', wrongCode(code1)); await click('#submit');
await errorIs('验证码错误或已过期', 'a wrong code is refused');
await shot('04-signup-error');
await fill('#mail-code', code1);
await shot('05-signup');
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
ok((await text('#status-relay')).includes('relay-local') && await has('#status-relay .rtt.ok'), 'the status bar names the relay in use, its latency coloured');
ok(await has('#network-body td .rtt.ok') && await has('#network-body .badge.accent'), 'the relay table colours the latency and marks the relay in use');
ok(await ev(`${el('#endpoint-hint')}.hidden`), 'what a panel is for stays folded');
await click('#endpoint-hint-btn');
ok(!(await ev(`${el('#endpoint-hint')}.hidden`)) && (await text('#endpoint-hint')).includes('base URL'), 'until its "?" is clicked');
await click('#endpoint-hint-btn');
ok((await text('#devices-body')).includes('（本机）'), 'this device is marked in the list');
ok(await has('#jobs-empty'), 'no jobs yet');
ok(!(await ev('document.documentElement.outerHTML')).includes(code1), 'the code is gone from the page');
await shot('06-dashboard');

console.log('buying shows up');
let chatStatus = 0;
for (let i = 0; i < 20 && chatStatus !== 200; i++) {
  const r = await fetch(env.DAEMON + '/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + apiKeyFile().key }, body: JSON.stringify({ model: 'mock-fast', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }) });
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
const peer = (await ev(`kfetch('/api/v1/session').then(r => r.json()).then(s => s.peer)`));
ok((await portalApi('PATCH', '/api/v1/devices/' + peer, { name: hostile })).ok, 'a device gets a hostile name');
await waitFor(`${el('#devices-body')}.textContent.includes('<img')`, 25000);
ok(!(await has('#devices-body img')) && !(await ev('window.__xss === 1')), 'it shows as text, nothing runs');

ok((await ev(`${el('#app-version')}.textContent`)).length > 2, 'the app shows its version');
console.log('layouts');
// The window never scrolls: a view fits it, and long lists scroll in their panel.
const fits = () => ev('document.documentElement.scrollWidth <= window.innerWidth && document.documentElement.scrollHeight <= window.innerHeight && document.body.scrollHeight <= window.innerHeight');
const viewFits = name => ev(`(() => { const v = ${el('#view-' + name)}; return v.getClientRects().length > 0 && v.scrollHeight <= v.clientHeight && v.scrollWidth <= v.clientWidth; })()`);
for (const name of ['share', 'pairing', 'activity', 'settings', 'overview']) {
  await click('#nav-' + name);
  ok(await fits() && await viewFits(name) && await text('#view-title') !== '', `the ${name} view fits the window`);
  if (name === 'settings') {
    ok((await text('#account-name')) === NORMAL && (await text('#devices-body')).includes('（本机）'), 'settings show the account and its devices');
    await shot('08b-settings');
  }
}
await cdp('Emulation.setDeviceMetricsOverride', { width: 760, height: 488, deviceScaleFactor: 2, mobile: false });
await sleep(300);
ok(await fits(), 'no scrolling of the window at its smallest size');
await click('#nav-settings');
ok(await ev(`${el('#logout')}.getBoundingClientRect().height`) > 0 && await ev(`${el('#logout')}.getBoundingClientRect().height < 40`), 'the logout button stays on one line');
await click('#nav-overview');
await shot('08-narrow');
await cdp('Emulation.setDeviceMetricsOverride', WINDOW);
await cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] });
await sleep(200);
await shot('09-dark');
await cdp('Emulation.setEmulatedMedia', { features: [] });

console.log('the endpoint\'s API key');
const v1Status = async key => (await fetch(env.DAEMON + '/v1/models', { headers: key ? { Authorization: 'Bearer ' + key } : {} })).status;
const key1 = apiKeyFile().key;
ok(apiKeyFile().required && await v1Status('') === 401 && await v1Status(key1) === 200, 'the endpoint wants the API key from the start');
await waitFor(el('#copy-apikey'));
ok(!(await ev('document.documentElement.outerHTML')).includes(key1), 'the overview offers to copy the key without showing it');
await click('#nav-settings');
await waitFor(`${el('#apikey-required')}?.checked && !${el('#apikey-required')}.disabled`);
ok((await text('#apikey-text')).endsWith(key1.slice(-4)) && !(await text('#apikey-text')).includes(key1), 'the settings show the end of the key only');
await click('#apikey-required');
await waitFor(`!${el('#apikey-required')}.checked && !${el('#apikey-required')}.disabled`);
ok(!apiKeyFile().required && await v1Status('') === 200, 'turned off, any request gets in, at once');
ok(await has('#apikey-note.warn-text'), 'and the settings warn about it');
await click('#apikey-required');
await waitFor(`${el('#apikey-required')}.checked && !${el('#apikey-required')}.disabled`);
ok(apiKeyFile().required && await v1Status('') === 401, 'turned on again');
await shot('09b-apikey');
await click('#apikey-rotate'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok'); await waitFor(`!${el('#dlg-confirm')}`);
const key2 = apiKeyFile().key;
ok(key2 !== key1 && await v1Status(key1) === 401 && await v1Status(key2) === 200, 'a new key replaces the old one');
await waitFor(`${el('#apikey-text')}.textContent.endsWith(${q(key2.slice(-4))})`);
ok(true, 'and the settings show it');

console.log('buying settings');
const buyConfig = () => JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).buy;
await click('#tab-settings-buy');
await waitFor(`!${el('#buy-native')}.disabled && ${el('#buy-budget')}.value !== ''`);
ok(!(await ev(`${el('#buy-native')}.checked`)) && await ev(`${el('#buy-budget')}.value`) === '2', 'the defaults: converting sellers allowed, a budget of 2 credits');
ok((await text('#buy-native-hint')).length > 40 && (await text('#buy-budget-hint')).length > 30, 'each setting is explained');
await click('#buy-native');
await waitFor(`${el('#buy-note')}.classList.contains('ok')`);
ok(buyConfig().require_native === true, 'ticking it saves require_native at once');
await fill('#buy-budget', 'lots');
await click('#buy-budget-save');
await waitFor(`${el('#buy-note')}.classList.contains('warn-text')`);
ok(buyConfig().budget_micros === 2000000, 'a budget that is not a number is refused, and says why');
await fill('#buy-budget', '3.5');
await click('#buy-budget-save');
await waitFor(`${el('#buy-note')}.classList.contains('ok')`);
ok(buyConfig().budget_micros === 3500000 && buyConfig().require_native === true, 'the budget is saved in micro-credits, require_native kept');
await shot('09c-buy-settings');
await ev(`${el('#buy-note')}.className = ''`);
await click('#buy-native');
await waitFor(`${el('#buy-note')}.classList.contains('ok') && !${el('#buy-native')}.checked`);
ok(buyConfig().require_native === false || buyConfig().require_native === undefined, 'and unticking it saves that too');
await click('#tab-settings-general');
await click('#nav-overview');

console.log('feedback');
ok(!(await has('#feedback-text')), 'the feedback form is not on the page until asked for');
await click('#nav-feedback'); await waitFor(el('#dlg-feedback'));
await click('#feedback-send');
await waitFor(`${el('#feedback-result')}.textContent === '请填写反馈内容'`);
ok(true, 'empty feedback is stopped in the page');
await fill('#feedback-text', 'it works');
await click('#feedback-send');
await waitFor(`${el('#feedback-result')}.textContent.includes('已提交')`);
ok(await ev(`${el('#feedback-text')}.value`) === '', 'feedback is sent and the box is cleared');
const sent = await ev(`kfetch('/api/v1/feedback/mine').then(r => r.json())`);
ok(sent.feedback[0].message === 'it works' && sent.feedback[0].source === 'client' && sent.feedback[0].os.includes('/'), 'the portal has it, marked as from the client');
await shot('10b-feedback');
await click('#dlg-feedback-close');
ok(!(await has('#dlg-feedback')), 'and the dialog closes');

console.log('sharing: a hosted API');
const choose = (s, v) => ev(`(() => { const e = ${el(s)}; e.value = ${q(v)}; e.dispatchEvent(new Event('change', { bubbles: true })); })()`);
const shown = s => ev(`(() => { const e = ${el(s)}; return !!e && e.getClientRects().length > 0; })()`);
const KEY = 'sk-test-secret-0123456789';
await click('#nav-share');
await waitFor(el('#provider-add'));
ok(await shown('#view-share') && !(await shown('#view-overview')), 'the sharing view replaces the overview');
ok(await shown('#share-state') && !(await shown('#providers')) && await shown('#tabs-share'), 'it opens on its status tab');
ok(await text('#share-pill') === '未开启' && await has('#providers-empty') && await has('#local-empty'), 'nothing is shared at first');
await click('#share-toggle');
await waitFor(`${el('#share-error')}?.textContent.includes('先添加')`);
ok(true, 'sharing cannot be turned on with nothing to sell');
await shot('15-share-empty');
await click('#tab-share-providers');
ok(await shown('#providers') && !(await shown('#share-state')), 'the providers have a tab of their own');
await click('#provider-add'); await waitFor(el('#dlg-provider'));
ok(await ev(`[...${el('#pv-type')}.options].map(o => o.value).join()`) === 'anthropic,openai,gemini,antigravity,grok,deepseek,custom', 'every kind of provider can be added');
await choose('#pv-type', 'openai');
ok(await has('#pv-auth-apikey') && await has('#pv-auth-oauth') && await has('#pv-key-page'), 'OpenAI offers an API key or a login, and says where keys are made');
await click('#pv-save');
await waitFor(`${el('#pv-error')}.textContent.includes('API key')`);
ok(true, 'an empty key is stopped in the page');
await click('#pv-auth-oauth'); await waitFor(el('#pv-oauth-risk'));
await click('#pv-login-start'); await waitFor(el('#pv-login-url'));
ok((await ev(`${el('#pv-login-url')}.href`)).startsWith('https://auth.openai.com/'), 'a login shows the address to open');
await shot('16-share-login');
await click('#pv-login-finish');
await waitFor(`${el('#pv-error')}.textContent.includes('粘贴')`);
await fill('#pv-code', 'http://localhost:1455/auth/callback?code=abc&state=not-this-login'); await click('#pv-login-finish');
await waitFor(`${el('#pv-error')}.textContent.includes('another login')`);
ok(true, 'a code of another login attempt is refused');
await choose('#pv-type', 'antigravity');
ok(!(await has('#pv-auth-apikey')) && await has('#pv-oauth-risk') && await has('#pv-project'), 'a login-only provider shows no key field');
await choose('#pv-type', 'custom');
await fill('#pv-name', 'Bad Name'); await fill('#pv-key', KEY); await click('#pv-save');
await waitFor(`${el('#pv-error')}.textContent.includes('小写字母')`);
await fill('#pv-name', 'lab'); await click('#pv-save');
await waitFor(`${el('#pv-error')}.textContent.includes('API 地址')`);
ok(true, 'a bad name and a missing address are stopped in the page');
await fill('#pv-url', env.MOCK);
await shot('17-share-add-provider');
await click('#pv-save');
await waitFor(el('#dlg-model'));
ok(!(await has('#dlg-provider')) && (await text('#prov-lab')).includes('sk-…6789') && (await text('#provider-flash')).includes('lab'), 'the provider is saved, its key masked');
ok(await ev(`${el('#md-pick')}.value`) === 'mock-fast' && await ev(`${el('#md-id')}.value`) === 'mock-fast', "the provider's models are offered");
ok(await ev(`${el('#md-in')}.value`) === '0.1' && await ev(`${el('#md-out')}.value`) === '0.2', 'with the default prices');
ok((await text('#md-market')).includes('0.1'), "and the network's lowest price for that model");
await fill('#md-in', 'cheap'); await click('#md-save');
await waitFor(`${el('#md-error')}.textContent.includes('价格')`);
await fill('#md-id', 'desk-mock'); await fill('#md-in', '0.3'); await fill('#md-out', '0.6'); await fill('#md-max', '4096');
await shot('18-share-add-model');
await click('#md-save');
await waitFor(`${el('#prov-lab')}.textContent.includes('desk-mock')`);
ok((await text('#prov-lab')).includes('0.3 / M') && (await text('#prov-lab')).includes('4,096') && (await text('#prov-lab')).includes('未出售'), 'the model is listed with its prices, not sold yet');
ok((await text('#share-hint')).includes('1'), 'the page says sharing is still off');
const providersFile = () => JSON.parse(readFileSync(env.DESKTOP_HOME + '/providers.json', 'utf8'));
ok(providersFile().providers[0].api_key === KEY && providersFile().providers[0].models[0].price_in === 300000, 'providers.json holds the key and the model');
ok(!(await ev('document.documentElement.outerHTML')).includes(KEY) && !JSON.stringify(await ev(`kfetch('/api/v1/status').then(r => r.json())`)).includes(KEY), 'the key is in neither the page nor the API');

console.log('sharing: on, test, edit');
await shot('18b-share-provider');
await click('#tab-share-state');
await click('#share-toggle');
await waitFor(`${el('#share-pill')}.textContent === '正在共享'`);
await waitFor(`${el('#prov-lab')}.textContent.includes('在售')`);
ok((await text('#share-stats')).includes('1'), 'sharing is on and the model is sold');
await click('#prov-lab .model-test');
await waitFor(el('#test-ok'), 30000);
ok((await text('#test-text')).includes('alpha') && (await text('#test-usage')).length > 5, 'a test request goes through the provider');
await shot('19-share-test');
await click('#test-done');
await click('#prov-lab .model-edit'); await waitFor(el('#dlg-model'));
ok(await ev(`${el('#md-id')}.disabled && ${el('#md-in')}.value === '0.3' && ${el('#md-max')}.value === '4096'`), 'editing starts from the saved values');
await fill('#md-in', '0.5'); await click('#md-save');
await waitFor(`${el('#prov-lab')}.textContent.includes('0.5 / M')`);
ok(providersFile().providers[0].models[0].price_in === 500000 && providersFile().providers[0].models.length === 1, 'a new price replaces the old one');
await waitFor(`${el('#share-relays-body')}.textContent.includes('已预约')`, 30000);
ok((await text('#share-relays-body')).includes('relay-local'), 'the relay reservation is shown');
ok((await text('#share-tile-active .value')).includes('/') && await has('#share-tile-total') && await has('#sold-empty'), 'tiles show the run and the account totals; no jobs sold yet');
await waitFor(`${el('#prov-lab')}.textContent.includes('0.5 / 0.6')`, 40000);
ok(true, "the network's lowest price stands beside the model's own");
await shot('20-share-on');
ok(await viewFits('share'), 'the sharing status fits the window while sharing');
await click('#tab-share-providers');
await shot('20b-share-providers');
ok(await ev(`(() => { const v = ${el('#view-share')}; return v.scrollHeight <= v.clientHeight; })()`), 'a provider with its models fits too');

console.log('sharing: the local model server');
await click('#tab-share-local');
await click('#local-settings'); await waitFor(el('#dlg-local'));
await fill('#local-url', 'ftp://nope'); await click('#local-save');
await waitFor(`${el('#local-error')}.textContent.includes('http')`);
await fill('#local-url', env.MOCK + '/'); await fill('#local-conc', '3'); await click('#local-save');
await waitFor(`${el('#local-upstream')}?.textContent === ${q(env.MOCK)}`);
ok((await text('#local-concurrency')).includes('3'), 'the model server address and the concurrency are saved');
await click('#local-add-model'); await waitFor(`${el('#md-pick')}?.value === 'mock-fast'`);
ok(true, 'the models of the local server are detected');
await fill('#md-id', 'desk-mock'); await click('#md-save');
await waitFor(`${el('#md-error')}.textContent.includes('其他来源')`);
ok(true, 'one model ID cannot come from two sources');
const hostileModel = '<img src=x onerror=window.__xss=2>';
await fill('#md-id', hostileModel); await click('#md-save');
await waitFor(`${el('#local-models-body')}.textContent.includes('<img')`);
ok(!(await has('#view-share img')) && !(await ev('window.__xss === 2')), 'a hostile model ID shows as text');
await waitFor(`${el('#local-models-body')}.textContent.includes('在售')`);
const cfgShare = () => JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).share;
ok(cfgShare().enabled && cfgShare().models[0].id === hostileModel && cfgShare().concurrency === 3, 'config.json has the local model');
await click('#nav-overview');
ok((await text('#tile-share .value')) === '正在共享', 'the overview has a sharing tile');
await fullShot('20c-overview-full');
await click('#tile-share');
ok(await shown('#view-share'), 'which opens the sharing view');
await click('#nav-overview');
await waitFor(`${el('#models-body')}.textContent.includes('desk-mock')`, 40000);
ok(true, 'the network lists the model this device sells');
await click('#nav-share');
await cdp('Emulation.setDeviceMetricsOverride', { width: 760, height: 488, deviceScaleFactor: 2, mobile: false });
await sleep(300);
ok(await fits(), 'no scrolling of the window in the sharing view at its smallest size');
await shot('21-share-narrow');
await cdp('Emulation.setDeviceMetricsOverride', WINDOW);

console.log('sharing: off and removal');
await click('#prov-lab .prov-toggle');
await waitFor(`${el('#prov-lab .pill')}.textContent === '已停用'`);
await waitFor(`${el('#prov-lab')}.textContent.includes('未出售')`);
ok(await text('#share-pill') === '正在共享', 'a provider that is off is not sold; the rest still is');
await click('#local-models-body .model-remove'); await waitFor(el('#dlg-confirm'));
await click('#confirm-cancel');
ok(cfgShare().models.length === 1, 'cancelling removes nothing');
await click('#local-models-body .model-remove'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok');
await waitFor(el('#local-empty'));
await waitFor(`${el('#share-pill')}.textContent === '未开启'`);
ok(!cfgShare().enabled && cfgShare().models.length === 0, 'with nothing left to sell, sharing turns off');
await click('#prov-lab .prov-toggle');
await waitFor(`${el('#prov-lab .pill')}.textContent === '已启用'`);
await click('#prov-lab .prov-renew'); await waitFor(el('#dlg-provider'));
ok(await ev(`${el('#pv-type')}.disabled && ${el('#pv-name')}.disabled && ${el('#pv-name')}.value === 'lab'`), 'replacing credentials keeps the type and the name');
await fill('#pv-key', 'sk-test-other-000000004321'); await fill('#pv-url', env.MOCK); await click('#pv-save');
await waitFor(`${el('#prov-lab')}?.textContent.includes('sk-…4321')`);
ok(!(await has('#dlg-model')) && providersFile().providers[0].models.length === 1, 'a new key keeps the models');
await click('#prov-lab .prov-remove'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok');
await waitFor(el('#providers-empty'));
ok(providersFile().providers.length === 0, 'removing a provider removes its credentials');
await click('#nav-overview');
ok(await shown('#tiles'), 'back on the overview');

console.log('pairing');
// The other accounts of the stack act through the command line: `seller` sells
// mock-fast, `idle` sells nothing.
const cli = (home, ...args) => execFileSync(`${dir}/bin/sharetoken`, ['-home', `${dir}/${home}`, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const codeOf = out => out.match(/[A-Z2-7]{4}(?:-[A-Z2-7]{4}){3}/)[0];
const buyOnce = async () => {
  const r = await fetch(env.DAEMON + '/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + apiKeyFile().key }, body: JSON.stringify({ model: 'mock-fast', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }) });
  return { status: r.status, body: await r.text() };
};
const clickRow = (panel, needle, button) => ev(`[...document.querySelectorAll(${q(panel + ' tbody tr')})].find(r => r.textContent.includes(${q(needle)})).querySelector(${q(button)}).click()`);
await click('#nav-pairing');
await waitFor(el('#partners-empty'));
ok(await shown('#share-partners') && await shown('#share-settings') && !(await shown('#paired-sellers')), 'the pairing view opens on the share partners');
await click('#tab-pairing-sellers');
await waitFor(el('#pair-sellers-empty'));
ok(await shown('#paired-sellers') && !(await shown('#pairing')), 'the sellers tab shows the paired sellers');
ok(!(await has('#pair-restricted')) && (await text('#paired-sellers-body')).includes('3'), 'an account that is not paired buys from everyone; it may pair with three sellers');
await click('#pair-join'); await waitFor(el('#dlg-pair-join'));
await click('#pair-join-ok');
await waitFor(`${el('#pair-join-error')}.textContent.includes('请输入')`);
await fill('#pair-code-input', 'AAAA-AAAA-AAAA-AAAA'); await click('#pair-join-ok');
await waitFor(`${el('#pair-join-error')}.textContent.includes('配对码不对')`);
ok(true, 'an empty and a wrong pairing code are refused');
const idleCode = codeOf(cli('idle', 'pair', 'code', '-label', '<b>idle</b> team'));
await fill('#pair-code-input', idleCode.toLowerCase().replaceAll('-', ' ')); await click('#pair-join-ok');
await waitFor(el('#pair-restricted'));
ok((await text('#pair-join-flash')).includes('id***') && !(await text('#paired-sellers')).includes('idle-') && !/\bidle\b/.test((await text('#paired-sellers')).replace('<b>idle</b>', '')), "pairing shows the seller's masked name");
ok((await text('#paired-sellers-body')).includes('<b>idle</b> team') && !(await has('#paired-sellers b')), 'and the label of the code, as text');
await waitFor(el('#models-empty'), 40000);
ok(true, 'paired with a seller that sells nothing, the buyer sees no models');
const refused = await buyOnce();
ok(refused.status !== 200 && refused.body.includes('paired'), 'and a request is refused, with the reason');
await shot('23-paired-restricted');
await click('#pair-join'); await waitFor(el('#dlg-pair-join'));
await fill('#pair-code-input', codeOf(cli('seller', 'pair', 'code'))); await click('#pair-join-ok');
await waitFor(`${el('#paired-sellers-body')}.textContent.includes('se***')`);
await waitFor(`${el('#models-body')}.textContent.includes('mock-fast')`, 40000);
ok((await buyOnce()).status === 200, 'paired with a second seller, the buyer buys what that one sells');
await fullShot('23b-paired-two');
await clickRow('#paired-sellers', 'id***', '.pair-leave'); await waitFor(el('#dlg-confirm'));
await click('#confirm-cancel');
ok((await text('#paired-sellers-body')).includes('id***'), 'cancelling leaves the pairing');
await clickRow('#paired-sellers', 'id***', '.pair-leave'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok');
await waitFor(`!${el('#paired-sellers-body')}.textContent.includes('id***')`);
ok(await has('#pair-restricted') && (await text('#paired-sellers-body')).includes('se***'), 'leaving one seller keeps the other');

await click('#tab-pairing-buyers'); await waitFor(el('#pair-new-code'));
ok(await shown('#pairing') && await has('#pair-codes-empty') && await has('#pair-buyers-empty'), 'the buyers tab starts with no codes and no buyers');
const isPrivate = async () => JSON.parse(await ev(`kfetch('/api/v1/pairing').then(r => r.text())`)).private;
ok(!(await ev(`${el('#pair-private')}.checked`)) && !(await isPrivate()), 'private mode is off at first');
await click('#pair-private');
await waitFor(`${el('#pair-private')}.checked && !${el('#pair-private')}.disabled`);
ok(await isPrivate(), 'the switch turns private mode on at the portal');
await shot('23c-pair-private');
await click('#pair-private');
await waitFor(`!${el('#pair-private')}.checked && !${el('#pair-private')}.disabled`);
ok(!(await isPrivate()), 'and off again');
await click('#pair-new-code'); await waitFor(el('#dlg-pair-code'));
ok(await ev(`${el('#pair-ttl')}.value`) === '24' && await ev(`[...${el('#pair-ttl')}.options].map(o => o.value).join()`) === '24,168,720', 'a code lasts one day, or seven or thirty when chosen');
await fill('#pair-uses', 'many'); await click('#pair-code-ok');
await waitFor(`${el('#pair-code-error')}.textContent.includes('整数')`);
await fill('#pair-label', 'friends'); await choose('#pair-ttl', '168'); await fill('#pair-uses', '2'); await click('#pair-code-ok');
await waitFor(el('#dlg-pair-shown'));
const ownCode = await text('#pair-shown-code');
ok(/^[A-Z2-7]{4}(-[A-Z2-7]{4}){3}$/.test(ownCode), 'a new code is shown once it is made');
await shot('24-pair-code');
await click('#pair-shown-done');
const pairingText = () => text('#pairing-body');
ok((await pairingText()).includes(ownCode) && (await pairingText()).includes('friends') && (await pairingText()).includes('0 / 2'), 'and listed with its label and uses');
const days = (JSON.parse(await ev(`kfetch('/api/v1/pairing').then(r => r.text())`)).codes[0].expires_at / 1e6 - Date.now()) / 86400e3;
ok(days > 6.9 && days < 7.1, 'it lasts the seven days chosen');
await click('#tab-pairing-sellers'); await click('#pair-join'); await waitFor(el('#dlg-pair-join'));
await fill('#pair-code-input', ownCode); await click('#pair-join-ok');
await waitFor(`${el('#pair-join-error')}.textContent.includes('自己的配对码')`);
ok(true, "nobody pairs with their own code");
await click('#dlg-pair-join-close');
ok(cli('seller', 'pair', 'join', ownCode).includes('paired with seller de***@example.com'), "another account redeems it and sees this account's masked name");
await click('#tab-pairing-buyers');
await waitFor(`${el('#pairing-body')}.textContent.includes('se***')`, 40000);
ok((await pairingText()).includes('1 / 2') && !(await has('#pair-buyers-empty')), 'the buyer appears, and the code counts it');
await fullShot('24b-pair-buyers');
await cdp('Emulation.setDeviceMetricsOverride', { width: 760, height: 488, deviceScaleFactor: 2, mobile: false });
await sleep(300);
ok(await fits(), 'no scrolling of the window with pairings at its smallest size');
await shot('24c-pair-narrow');
await cdp('Emulation.setDeviceMetricsOverride', WINDOW);
await clickRow('#pairing', 'se***', '.pair-remove'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok');
await waitFor(el('#pair-buyers-empty'));
ok((await pairingText()).includes(ownCode), 'removing a buyer keeps the code');
await click('#pairing .pair-revoke'); await waitFor(el('#dlg-confirm'));
ok((await text('#confirm-text')).includes(ownCode), 'revoking asks first');
await click('#confirm-ok');
await waitFor(el('#pair-codes-empty'));
let revoked = false;
try { cli('seller', 'pair', 'join', ownCode); } catch (e) { revoked = true; }
ok(revoked, 'a revoked code pairs nobody');
await click('#tab-pairing-sellers');
await click('#paired-sellers .pair-leave'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok');
await waitFor(el('#pair-sellers-empty'));
ok(!(await has('#pair-restricted')), 'with the last seller gone, the account buys from everyone again');
await waitFor(`${el('#models-body')}.textContent.includes('mock-fast')`, 40000);
ok(cli('seller', 'pair', 'private', 'on').includes('private mode on'), 'the seller goes private');
await waitFor(el('#models-empty'), 40000);
ok((await buyOnce()).status !== 200, "a private seller's models are hidden from a buyer that is not paired with it, and it cannot buy them");
cli('seller', 'pair', 'private', 'off');
await waitFor(`${el('#models-body')}.textContent.includes('mock-fast')`, 40000);
ok((await buyOnce()).status === 200, 'with private mode off the seller serves everyone again');

console.log('share partners');
// The seller account shares mock-fast; once this account is its partner, it
// buys from it for free.
const buyPaid = async () => {
  const r = await fetch(env.DAEMON + '/v1/chat/completions', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + apiKeyFile().key }, body: JSON.stringify({ model: 'mock-fast', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }) });
  await r.text();
  return { status: r.status, cost: Number(r.headers.get('X-Sharetoken-Cost-Micros') || -1) };
};
const sharing = async () => JSON.parse(await ev(`kfetch('/api/v1/sharing').then(r => r.text())`));
await click('#nav-pairing'); await click('#tab-pairing-partners');
await waitFor(el('#partners-empty'));
ok(await ev(`${el('#partner-pct')}.value`) === '100' && await ev(`${el('#partner-limit')}.value`) === '' && !(await ev(`${el('#partner-only')}.checked`)),
  'by default partners get all of it, with no daily limit, and the market stays open');
const before = await buyPaid();
ok(before.status === 200 && before.cost > 0, `without partners a request is paid (${before.cost} micros)`);
await click('#partner-invite'); await waitFor(el('#dlg-partner-invite'));
ok(await ev(`[...${el('#partner-ttl')}.options].map(o => o.value).join()`) === '24,72,168', 'an invite lasts one, three or seven days');
await click('#partner-invite-ok');
await waitFor(el('#dlg-partner-shown'));
const invite = await text('#partner-shown-code');
ok(/^[A-Z2-7]{4}(-[A-Z2-7]{4}){3}$/.test(invite), 'an invite code is shown');
await shot('25-partner-invite');
await click('#partner-shown-done');
await waitFor(`${el('#share-partners-body')}.textContent.includes(${q(invite)})`);
await click('#partner-join'); await waitFor(el('#dlg-partner-join'));
await fill('#partner-code-input', invite); await click('#partner-join-ok');
await waitFor(`${el('#partner-join-error')}.textContent.includes('自己的邀请码')`);
ok(true, 'nobody joins their own invite');
await click('#dlg-partner-join-close');
ok(cli('seller', 'partner', 'join', invite).includes('now sharing with de***@example.com'), "the other account joins and sees this account's masked name");
await waitFor(`${el('#share-partners-body')}.textContent.includes('se***')`, 40000);
ok(!(await text('#share-partners-body')).includes(invite), 'the used invite is gone and the partner is listed');
let reused = false;
try { cli('idle', 'partner', 'join', invite); } catch (e) { reused = true; }
ok(reused, 'an invite admits one person');
await click('#nav-overview');
await waitFor(el('#models-body .model-shared'), 40000);
ok(true, "the partner's model is marked free on the overview");
const free = await buyPaid();
ok(free.status === 200 && free.cost === 0, 'a request served by a partner costs nothing');
await waitFor(el('#jobs-body .job-shared'), 40000);
ok(true, 'the job is listed as shared');
await click('#nav-pairing'); await click('#tab-pairing-partners');
const usedOf = async () => (await sharing()).partners[0].taken;
for (let i = 0; i < 40 && !(await usedOf()); i++) await sleep(500);
ok(await usedOf() > 0, 'the tokens used of the partner are counted');
await waitFor(`${el('#share-partners-body')}.querySelector('tbody td:nth-child(4)')?.textContent !== '0'`, 40000);
ok(true, 'and shown');
await fullShot('25b-partners');

// The partner pauses sharing: this account goes to the market and pays.
ok(cli('seller', 'partner', 'set', '-share', '0').includes('paused'), 'the partner pauses sharing from the command line');
const paused = await buyPaid();
ok(paused.status === 200 && paused.cost > 0, 'with sharing paused, a request goes to the market and is paid');
// Partners only: no market, so the request fails.
await click('#partner-only'); await click('#partner-save');
await waitFor(el('#partner-settings-flash'));
ok((await sharing()).settings.only_partners, 'partners only is saved');
ok((await buyPaid()).status !== 200, 'with partners only and the partner paused, a request fails instead of going to the market');
cli('seller', 'partner', 'set', '-share', '100');
ok((await buyPaid()).cost === 0, 'sharing again, the partner serves it for free');
await click('#partner-only');
await fill('#partner-limit', 'many'); await click('#partner-save');
await waitFor(`${el('#partner-settings-error')}.textContent.includes('整数')`);
await fill('#partner-limit', '5000'); await fill('#partner-pct', '101'); await click('#partner-save');
await waitFor(`${el('#partner-settings-error')}.textContent.includes('0 到 100')`);
await fill('#partner-pct', '0'); await click('#partner-save');
await waitFor(el('#partner-paused'));
const mine = (await sharing()).settings;
ok(!mine.only_partners && mine.daily_limit_tokens === 5000 && mine.share_pct === 0, 'the limits are checked and saved');
ok((await text('#share-settings-body')).includes('5,000') || (await text('#share-settings-body')).includes('5000'), 'the daily limit is shown with what was given');
await fullShot('25c-partner-settings');
await fill('#partner-limit', ''); await fill('#partner-pct', '100'); await click('#partner-save');
await waitFor(`!${el('#partner-paused')}`);
await cdp('Emulation.setDeviceMetricsOverride', { width: 760, height: 488, deviceScaleFactor: 2, mobile: false });
await sleep(300);
ok(await fits(), 'no scrolling of the window with partners at its smallest size');
await cdp('Emulation.setDeviceMetricsOverride', WINDOW);
await clickRow('#share-partners', 'se***', '.partner-leave'); await waitFor(el('#dlg-confirm'));
await click('#confirm-ok');
await waitFor(el('#partners-empty'));
ok((await buyPaid()).cost > 0, 'after parting, requests are paid again');

console.log('languages');
await cdp('Page.navigate', { url: env.DAEMON + '/?lang=en' });
await waitFor(`${el('#nav-share')}?.textContent === 'Sharing'`);
ok(await ev(`document.documentElement.lang`) === 'en' && !/[\u4e00-\u9fff]/.test(await ev(`${el('#app')}.innerText`)), 'the app is fully in English');
await click('#lang-select');
ok(await ev(`[...document.querySelectorAll('.lang-item')].map(e => e.lang).join()`) === 'en,zh-CN', 'the language menu lists English and Simplified Chinese only');
await click('#nav-share'); await waitFor(el('#provider-add')); await sleep(300);
await fullShot('22-share-english');
await click('#provider-add'); await waitFor(el('#dlg-provider'));
await shot('22b-provider-english');
await click('#dlg-provider-close');
for (const name of ['pairing', 'activity', 'settings', 'overview']) {
  await click('#nav-' + name); await sleep(100);
  ok(!/[\u4e00-\u9fff]/.test(await ev(`${el('#app')}.innerText`)) && await fits() && await viewFits(name), `the ${name} view is in English and fits`);
}
await fullShot('22c-overview-english');
ok(await ev(`fetch('/ui/i18n/client/de.json').then(r => r.status)`) === 404, 'the other dictionaries are gone');
await cdp('Page.navigate', { url: env.DAEMON + '/?lang=zh-CN' });
await waitFor(`${el('#nav-share')}?.textContent === '共享'`);

console.log('reload and logout');
await cdp('Page.reload'); await waitFor(el('#tiles .tile'));
ok(await text('#who') === NORMAL, 'a reload stays logged in');
await click('#logout');
await waitFor(el('#signin-form'));
ok(await ev(`${el('#email')}.value`) === NORMAL, 'after logout the email is pre-filled');
ok(!(await has('#new-account')), 'nothing announces a new account before a code is asked for');
ok(!(await has('#cert')) , 'the confirmed certificate is not asked about again');
const oldToken = config.token;
ok((await portalApi('GET', '/api/v1/me', null, oldToken)).status === 401, 'the token is revoked at the portal');
ok(JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).token === undefined, 'and gone from config.json');

console.log('signing in again');
await fill('#email', EMAIL.toUpperCase()); await click('#send-code');
await waitFor(`${el('#send-code')}.disabled`);
const code2 = await nextCode(NORMAL);
await sleep(200);
ok(!(await has('#new-account')), 'an existing account is not announced as new');
await fill('#mail-code', wrongCode(code2)); await click('#submit');
await errorIs('验证码错误或已过期', 'a wrong code is reported');
ok(await has('#signin-form'), 'and stays on the form');
await shot('10-wrong-code');
await fill('#mail-code', code2); await click('#submit');
await waitFor(el('#tiles .tile'));
ok(await text('#who') === NORMAL, 'signing in works, any capitalization');
await waitFor(`${el('#conn-state')}.textContent === '已连接'`, 30000);
ok(true, 'and rejoins the network');
await shot('11-logged-in-again');

console.log('login revoked elsewhere');
const token2 = JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).token;
ok((await portalApi('POST', '/api/v1/logout', null, token2)).ok, 'the token is revoked behind the app');
await waitFor(el('#notice'), 25000);
ok((await text('#notice')).includes('登录已失效'), 'the app returns to sign-in and says why');
await shot('12-expired');
await click('#send-code'); await fill('#mail-code', await nextCode(NORMAL)); await click('#submit');
await waitFor(el('#tiles .tile'));
ok(true, 'signing in again works');

console.log('two-factor authentication');
{
  const token3 = JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).token;
  const setup = await (await portalApi('POST', '/api/v1/account/mfa/setup', {}, token3)).json();
  const secret = base32(setup.secret);
  ok((await portalApi('POST', '/api/v1/account/mfa/enable', { challenge: setup.challenge, code: totp(secret) }, token3)).ok, 'the user turns two-factor authentication on (the web portal does this)');
  await click('#logout'); await waitFor(el('#signin-form'));
  await click('#send-code'); await fill('#mail-code', await nextCode(NORMAL)); await click('#submit');
  await waitFor(el('#mfa-form'));
  ok((await text('#mfa-form')).includes(NORMAL) && await has('#totp'), 'after the mailed code the app asks for the authenticator code');
  ok(JSON.parse(readFileSync(env.DESKTOP_HOME + '/config.json', 'utf8')).token === undefined, 'and is not signed in yet');
  await shot('12b-two-factor');
  await fill('#totp', wrongCode(totp(secret))); await click('#submit');
  await errorIs('两步验证码不正确', 'a wrong authenticator code is refused');
  await nextStep();
  await fill('#totp', totp(secret)); await click('#submit');
  await waitFor(el('#tiles .tile'));
  ok(await text('#who') === NORMAL, 'the right one signs in');
}

console.log('a newer build is published');
ok(!(await has('#update-notice')), 'no update notice before a release');
{
  const s0 = await ev(`kfetch('/api/v1/session').then(r => r.json())`);
  const sys = { darwin: 'macos', win32: 'windows', linux: 'linux' }[process.platform];
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  const pkg = `sharetoken${s0.env === 'dev' ? '-dev' : ''}-1.0.0-${sys}-${arch}.${{ macos: 'dmg', windows: 'exe', linux: 'deb' }[sys]}`;
  writeFileSync(`${env.DOWNLOADS}/${pkg}`, 'package');
  writeFileSync(`${env.DOWNLOADS}/SHA256SUMS-1.0.0`, `abcd  ${pkg}\n`);
  writeFileSync(`${env.DOWNLOADS}/RELEASE-1.0.0.json`, JSON.stringify({ version: '1.0.0', commit: 'newbuild', build_time: new Date().toISOString().replace(/\.\d+Z$/, 'Z') }));
  // What the release says about itself: a line for the notice, a text for the dialog.
  writeFileSync(`${env.DOWNLOADS}/RELEASE-1.0.0.zh-CN.md`, '# 更小的窗口\n\n窗口更紧凑，**菜单栏**显示中继延迟。\n\n## 界面\n- 侧边栏导航\n- 延迟按快慢着色 `ms`\n\n## 其他\n- <img src=x onerror=window.__xss=3>\n');
  writeFileSync(`${env.DOWNLOADS}/RELEASE-1.0.0.en.md`, '# A smaller window\n\nA tighter window.\n');
  const u = await ev(`kfetch('/api/v1/update/check', { method: 'POST' }).then(r => r.json())`);
  ok(u.available && !u.required && u.key === '1.0.0+newbuild' && u.package?.url === env.PORTAL + '/downloads/' + pkg && u.package.sha256 === 'abcd', 'the daemon finds the newer build of its version and the package for this system');
  await waitFor(el('#update-notice'), 10000);
  ok((await text('#update-text')).includes('1.0.0'), 'the page shows the new version');
  ok(u.notes['zh-CN'].summary === '窗口更紧凑，菜单栏显示中继延迟。' && u.notes.en.summary === 'A tighter window.', 'the daemon passes on what the release says about itself');
  ok(await text('#update-summary') === '窗口更紧凑，菜单栏显示中继延迟。', 'the page adds the release\'s one-line description, in its language');
  ok(await fits(), 'the notice does not make the window scroll');
  await shot('13b-update');
  ok(!(await has('#update-link')), 'the details are not on the page until asked for');
  await click('#update-open'); await waitFor(el('#dlg-update'));
  ok(await text('#update-body h3') === '更小的窗口' && await ev(`[...document.querySelectorAll('#update-body h4')].map(e => e.textContent).join()`) === '界面,其他'
    && await ev(`document.querySelectorAll('#update-body li').length`) === 3 && await text('#update-body p strong') === '菜单栏' && await text('#update-body li code') === 'ms',
    'the dialog draws the release notes: a title, subtitles, items, bold and code');
  ok(!(await has('#update-body img')) && (await text('#update-body')).includes('<img') && !(await ev('window.__xss === 3')), 'markup in the notes shows as text');
  ok((await ev(`${el('#update-link')}.href`)) === u.package.url && (await ev(`${el('#update-notes')}.href`)) === env.PORTAL + '/changelog/#v1.0.0', 'with the download and what changed');
  await sleep(300);
  await shot('13c-update-details');
  await click('#update-close');
  await click('#update-skip');
  await waitFor(`!${el('#update-notice')}`, 10000);
  const s1 = await ev(`kfetch('/api/v1/session').then(r => r.json())`);
  ok(s1.update.skipped && JSON.parse(readFileSync(env.DESKTOP_HOME + '/update.json', 'utf8')).skipped === u.key, 'skipping hides it and is remembered');
  await cdp('Page.reload'); await waitFor(el('#tiles .tile')); await sleep(500);
  ok(!(await has('#update-notice')), 'and it stays hidden after a reload');
  await click('#nav-settings'); await click('#update-check'); await waitFor(el('#dlg-update'));
  ok(await has('#update-body') && !(await has('#update-notice')), 'checking by hand shows a skipped release again, without bringing the notice back');
  await click('#update-close'); await click('#nav-overview');

  // One-click update: offered only for a file the release signed, and only in
  // the desktop window, whose shell marks the page. No shell answers here, so
  // the attempt fails and says so.
  // macOS installs the app from a zip of its own; elsewhere the package is the installer.
  const inst = sys === 'macos' ? pkg.replace(/\.dmg$/, '.app.zip') : pkg;
  if (inst !== pkg) writeFileSync(`${env.DOWNLOADS}/${inst}`, 'app');
  writeFileSync(`${env.DOWNLOADS}/SHA256SUMS-1.0.0`, `abcd  ${pkg}\nabcd  ${inst}\n`);
  writeFileSync(`${env.DOWNLOADS}/SIGNATURES-1.0.0`, `c2ln  ${inst}\n`);
  writeFileSync(`${env.DOWNLOADS}/RELEASE-1.0.0.json`, JSON.stringify({ version: '1.0.0', commit: 'newbuild2', build_time: new Date().toISOString().replace(/\.\d+Z$/, 'Z') }));
  const u2 = await ev(`kfetch('/api/v1/update/check', { method: 'POST' }).then(r => r.json())`);
  ok(u2.install?.signature === 'c2ln' && u2.install.url === env.PORTAL + '/downloads/' + inst, 'a signed package is offered for installing');
  await cdp('Page.reload', {}); await waitFor(el('#update-notice'), 10000);
  ok(!(await has('#update-install')), 'in a browser the notice has no install button');
  await ev(`document.documentElement.dataset.shellUpdate = '1'`);
  await click('#nav-settings'); await click('#update-check'); await waitFor(el('#update-now'));
  await shot('13d-update-now');
  await click('#update-now');
  await waitFor(`${el('#update-progress')}?.textContent.includes('更新失败')`, 10000);
  ok(!(await ev(`${el('#update-now')}.disabled`)), 'without a shell the update fails, says so, and can be tried again');
  await click('#update-close'); await click('#nav-overview');
}

console.log('local API key');
// Opened without the key, the page gets nothing of the user's and says how to open it.
await ev(`sessionStorage.removeItem('st_local_key')`);
await cdp('Page.navigate', { url: env.DAEMON + '/' });
await waitFor(`${el('#boot-error')}?.textContent.includes('#key=')`);
ok(!(await has('#tiles')), 'without the key the page shows nothing of the account');
await cdp('Page.navigate', { url: env.DAEMON + '/#key=' + env.DAEMON_KEY });
await waitFor(el('#tiles .tile'));
ok(true, 'with it, the account again');

console.log('portal goes away');
process.kill(Number(env.PORTAL_PID));
await waitFor(`${el('#banner-text')}?.textContent.includes('连不上门户')`, 25000);
ok(true, 'the app says the portal is unreachable');
ok(await text('#who') === NORMAL && (await text('#tile-available .value')) !== '', 'and keeps showing the last account data, still logged in');
await shot('14-portal-down');

ok(problems.length === 0, 'no script errors or CSP violations: ' + problems.join('; '));
console.log(`\nPASS: ${passed} checks`);
process.exit(0);
