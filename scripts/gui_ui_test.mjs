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

console.log('sharing: a hosted API');
const choose = (s, v) => ev(`(() => { const e = ${el(s)}; e.value = ${q(v)}; e.dispatchEvent(new Event('change', { bubbles: true })); })()`);
const shown = s => ev(`(() => { const e = ${el(s)}; return !!e && e.getClientRects().length > 0; })()`);
const KEY = 'sk-test-secret-0123456789';
await click('#nav-share');
await waitFor(el('#provider-add'));
ok(await shown('#view-share') && !(await shown('#view-overview')), 'the sharing view replaces the overview');
ok(await text('#share-pill') === '未开启' && await has('#providers-empty') && await has('#local-empty'), 'nothing is shared at first');
await click('#share-toggle');
await waitFor(`${el('#share-error')}?.textContent.includes('先添加')`);
ok(true, 'sharing cannot be turned on with nothing to sell');
await shot('15-share-empty');
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
ok(!(await ev('document.documentElement.outerHTML')).includes(KEY) && !JSON.stringify(await ev(`fetch('/api/v1/status').then(r => r.json())`)).includes(KEY), 'the key is in neither the page nor the API');

console.log('sharing: on, test, edit');
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
await fullShot('20b-share-on-full');

console.log('sharing: the local model server');
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
await cdp('Emulation.setDeviceMetricsOverride', { width: 480, height: 760, deviceScaleFactor: 1, mobile: false });
await sleep(300);
ok(await ev('document.documentElement.scrollWidth <= window.innerWidth'), 'no sideways scrolling in the sharing view at the smallest window');
await shot('21-share-narrow');
await cdp('Emulation.clearDeviceMetricsOverride');

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
await click('#nav-overview');
await fullShot('22c-overview-english');
ok(await ev(`fetch('/ui/i18n/client/de.json').then(r => r.status)`) === 404, 'the other dictionaries are gone');
await cdp('Page.navigate', { url: env.DAEMON + '/?lang=zh-CN' });
await waitFor(`${el('#nav-share')}?.textContent === '共享'`);

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

console.log('a newer build is published');
ok(!(await has('#update-notice')), 'no update notice before a release');
{
  const s0 = await ev(`fetch('/api/v1/session').then(r => r.json())`);
  const sys = { darwin: 'macos', win32: 'windows', linux: 'linux' }[process.platform];
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  const pkg = `sharetoken${s0.env === 'dev' ? '-dev' : ''}-1.0.0-${sys}-${arch}.${{ macos: 'dmg', windows: 'exe', linux: 'deb' }[sys]}`;
  writeFileSync(`${env.DOWNLOADS}/${pkg}`, 'package');
  writeFileSync(`${env.DOWNLOADS}/SHA256SUMS-1.0.0`, `abcd  ${pkg}\n`);
  writeFileSync(`${env.DOWNLOADS}/RELEASE-1.0.0.json`, JSON.stringify({ version: '1.0.0', commit: 'newbuild', build_time: new Date().toISOString().replace(/\.\d+Z$/, 'Z') }));
  const u = await ev(`fetch('/api/v1/update/check', { method: 'POST' }).then(r => r.json())`);
  ok(u.available && !u.required && u.key === '1.0.0+newbuild' && u.package?.url === env.PORTAL + '/downloads/' + pkg && u.package.sha256 === 'abcd', 'the daemon finds the newer build of its version and the package for this system');
  await waitFor(el('#update-notice'), 10000);
  ok((await text('#update-text')).includes('1.0.0'), 'the page shows the new version');
  ok((await ev(`${el('#update-link')}.href`)) === u.package.url && (await ev(`${el('#update-notes')}.href`)) === env.PORTAL + '/changelog/#v1.0.0', 'with the download and what changed');
  await shot('13b-update');
  await click('#update-skip');
  await waitFor(`!${el('#update-notice')}`, 10000);
  const s1 = await ev(`fetch('/api/v1/session').then(r => r.json())`);
  ok(s1.update.skipped && JSON.parse(readFileSync(env.DESKTOP_HOME + '/update.json', 'utf8')).skipped === u.key, 'skipping hides it and is remembered');
  await cdp('Page.reload'); await waitFor(el('#tiles .tile')); await sleep(500);
  ok(!(await has('#update-notice')), 'and it stays hidden after a reload');
}

console.log('portal goes away');
process.kill(Number(env.PORTAL_PID));
await waitFor(`${el('#banner-text')}?.textContent.includes('连不上门户')`, 25000);
ok(true, 'the app says the portal is unreachable');
ok(await text('#who') === NORMAL && (await text('#tile-available .value')) !== '', 'and keeps showing the last account data, still logged in');
await shot('14-portal-down');

ok(problems.length === 0, 'no script errors or CSP violations: ' + problems.join('; '));
console.log(`\nPASS: ${passed} checks`);
process.exit(0);
