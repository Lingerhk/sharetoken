#!/usr/bin/env node
// Browser test of the version features: the admin console's version page and the
// desktop client's upgrade notice, in a headless Chrome against scripts/gui_stack.sh.
//
//   scripts/test/gui_stack.sh start DIR
//   node scripts/test/version_ui_test.mjs DIR
//   scripts/test/gui_stack.sh stop DIR
import { execFileSync, spawn } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { setTimeout as sleep } from 'node:timers/promises';

const dir = process.argv[2];
const shots = dir + '/shots-version';
mkdirSync(shots, { recursive: true });
const env = Object.fromEntries(readFileSync(dir + '/env', 'utf8').trim().split('\n').map(l => { const i = l.indexOf('='); return [l.slice(0, i), l.slice(i + 1)]; }));
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9336;
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--ignore-certificate-errors', `--remote-debugging-port=${port}`,
  `--user-data-dir=${dir}/chrome-version`, '--window-size=1100,900', 'about:blank'], { stdio: 'ignore' });
const children = [proc];
process.on('exit', () => children.forEach(c => c.kill()));

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
const go = async url => { await cdp('Page.navigate', { url }); await sleep(600); };

function totp(secret) {
  let bits = '';
  for (const c of secret.replace(/=+$/, '')) bits += 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g).map(b => parseInt(b, 2)));
  const msg = Buffer.alloc(8); msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const d = createHmac('sha1', key).update(msg).digest();
  return String((d.readUInt32BE(d[19] & 15) & 0x7fffffff) % 1000000).padStart(6, '0');
}

const DSN = readFileSync(dir + '/dsn', 'utf8').trim();
const portalCmd = (args, extra = {}) => execFileSync(dir + '/bin/portal', ['-db', DSN, ...args], { cwd: dir, env: { ...process.env, SHARETOKEN_DB_PASSWORD: '', ...extra }, encoding: 'utf8' });
// Sign-in codes: nothing delivers mail, scripts/test/gui_stack.sh reads them from the outbox.
const mailedCode = email => execFileSync(new URL('./gui_stack.sh', import.meta.url).pathname, ['code', dir, email], { encoding: 'utf8' }).trim();

await connect();
await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable');
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "try { localStorage.setItem('st_lang', 'zh-CN'); } catch (e) {}" }); // these checks read the Chinese source text

console.log('admin console');
portalCmd(['useradd', '-admin', 'verroot@example.test']);
await go(env.PORTAL + '/admin/');
const web = `{ 'Content-Type': 'application/json', 'X-Sharetoken-Web': '1' }`;
const post = (path, body) => ev(`fetch(${q(path)}, { method: 'POST', headers: ${web}, body: ${q(JSON.stringify(body))} }).then(async r => ({ status: r.status, data: await r.json() }))`);
const sent = await post('/api/v1/auth/code', { email: 'verroot@example.test' });
ok(sent.status === 200 && sent.data.nonce, 'a sign-in code is mailed, with the nonce it redeems with');
const ch = (await post('/api/v1/web/auth/verify', { email: 'verroot@example.test', code: mailedCode('verroot@example.test'), nonce: sent.data.nonce })).data;
const setup = (await post('/api/v1/web/mfa/setup', { challenge: ch.challenge })).data;
ok((await post('/api/v1/web/mfa/confirm', { challenge: ch.challenge, code: totp(setup.secret) })).status === 200, 'an admin session');
await go(env.PORTAL + '/app/'); // a full load, so that the page sees the new session
await go(env.PORTAL + '/admin/#/versions');
await waitFor(`document.body.innerText.includes('客户端版本策略')`);
const page = await ev('document.body.innerText');
ok(page.includes('协调器') && page.includes('邮件服务') && page.includes('中继 relay-local'), 'the page lists the services');
ok(page.includes('客户端分布') && page.includes('API v1'), 'the page shows the client distribution and protocols');
await shot('admin-versions');
const inputs = 'document.querySelectorAll("main .card:last-child input")';
await ev(`(() => { const i = ${inputs}; i[0].value = 'bad'; })()`);
await ev(`[...document.querySelectorAll('main button')].find(b => b.textContent === '保存').click()`);
await sleep(600);
const policy = () => JSON.parse(portalCmd(['version-policy']));
ok(policy().min_client !== 'bad', 'a bad version is refused');
await ev(`(() => { const i = ${inputs}; i[0].value = ''; i[1].value = '9.9.9'; i[2].value = '9.9.9'; i[3].value = 'https://example.com/download'; i[4].checked = true; })()`);
await ev(`[...document.querySelectorAll('main button')].find(b => b.textContent === '保存').click()`);
await sleep(800);
const p = policy();
ok(p.latest_client === '9.9.9' && p.download_url === 'https://example.com/download' && p.allow_dev, 'the policy is saved from the page');

console.log('desktop client');
// A build with a version number: one without has nothing to compare.
const ROOT = new URL('../..', import.meta.url).pathname;
execFileSync('go', ['build', '-ldflags', '-X sharetoken/internal/version.Version=1.0.0', '-o', dir + '/bin/sharetoken-100', './cmd/sharetoken'], { cwd: ROOT, stdio: 'inherit' });
const bin = dir + '/bin/sharetoken-100', home = dir + '/version-desktop', api = 'http://127.0.0.1:19795';
const daemonEnv = { ...process.env, SHARETOKEN_PORTAL: env.PORTAL };
const startDaemon = async () => {
  const c = spawn(bin, ['-home', home, 'daemon', '-http', '127.0.0.1:19795', '-listen', '/ip4/127.0.0.1/tcp/0', '-nat=false'], { env: daemonEnv, stdio: 'ignore' });
  children.push(c);
  for (let i = 0; i < 50; i++) { try { if ((await daemonGet('/api/v1/session')).ok) break; } catch { /* starting */ } await sleep(200); }
  return c;
};
// The management API takes the key the daemon writes to run.json at each start.
const runKey = () => { try { return JSON.parse(readFileSync(home + '/run.json', 'utf8')).key || ''; } catch { return ''; } };
const daemonGet = path => fetch(api + path, { headers: { Authorization: 'Bearer ' + runKey() } });
let d = await startDaemon();
const daemonPost = (path, body) => fetch(api + path, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + runKey() }, body: JSON.stringify(body) });
ok((await daemonPost('/api/v1/auth/code', { pin: env.PIN, email: 'version@example.com' })).ok, 'a code through the daemon');
const login = await daemonPost('/api/v1/auth/verify', { pin: env.PIN, email: 'version@example.com', code: mailedCode('version@example.com'), device: 'version-box' });
ok(login.status === 200, 'signed up through the daemon');
d.kill(); await sleep(800);
d = await startDaemon();
for (let i = 0; i < 40; i++) { const s = await (await daemonGet('/api/v1/session')).json(); if (s.update.available) break; await sleep(250); }
await go(api + '/?lang=zh-CN#key=' + runKey());
await waitFor(el('#update-notice'));
ok((await text('#update-text')).includes('9.9.9'), 'the client offers the new release');
await click('#update-open'); await waitFor(el('#dlg-update'));
ok((await text('#update-link')) === 'https://example.com/download', 'with where to get it');
await click('#update-close');
ok((await text('#app-version')).startsWith('版本 '), 'the client shows its version');
ok(!(await ev(`${el('#update-notice')}.classList.contains('bad')`)), 'an optional upgrade is a warning');
await shot('client-update');
await go(api + '/?lang=en#key=' + runKey());
await waitFor(el('#update-text'));
ok(!/[\u4e00-\u9fa5]版本/.test(await text('#update-text')) && (await text('#update-text')).includes('9.9.9'), 'the notice is translated');
if (problems.length) throw new Error('page problems:\n' + problems.join('\n'));
console.log(`${passed} checks passed`);
process.exit(0);
