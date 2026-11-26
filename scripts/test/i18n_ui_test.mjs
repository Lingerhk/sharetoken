#!/usr/bin/env node
// Renders the landing page, the user portal and the desktop client in every
// language in headless Chrome, at desktop and phone width: the page takes the
// language and direction, no Chinese source text is left, nothing runs off the
// screen, and the language picker and pinned admin console behave.
//
//   scripts/test/gui_stack.sh start DIR && . DIR/env
//   STACK=DIR node scripts/test/i18n_ui_test.mjs "$PORTAL" "$DAEMON" [shots-dir]
//
// It signs in on the portal and in the client with codes read from the stack's outbox.
//
// LANGS=en,ar limits the run. Screenshots land in the shots directory, a new
// temporary one by default. A client left signed in by another test is signed out.
import { execFileSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const [portal, daemon, shots] = [process.argv[2], process.argv[3], process.argv[4] || mkdtempSync(join(tmpdir(), 'st-i18n-'))];
const STACK = process.env.STACK;
if (!portal || !daemon || !STACK) { console.error('usage: STACK=<gui_stack.sh directory> i18n_ui_test.mjs <portal> <daemon> [shots-dir]'); process.exit(2); }
// The last sign-in code mailed to an address, from the stack's outbox.
const mailedCode = email => execFileSync(new URL('gui_stack.sh', import.meta.url).pathname, ['code', STACK, email], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
mkdirSync(shots, { recursive: true });
const runtime = readFileSync(new URL('../../internal/portal/web/i18n.js', import.meta.url), 'utf8');
const langsAt = runtime.indexOf('const ALL_LANGS = {');
const ALL = [...runtime.slice(langsAt, runtime.indexOf('};', langsAt)).matchAll(/'?([A-Za-z-]+)'?: '/g)].map(m => m[1]);
if (ALL.length < 2) { console.error('no languages found in i18n.js'); process.exit(2); }
const LANGS = process.env.LANGS ? process.env.LANGS.split(',') : ALL;
const HAN_OK = new Set(['zh-CN', 'zh-TW', 'ja']);
// The desktop client has languages of its own (client/ui/index.html, data-langs).
const CLIENT_LANGS = readFileSync(new URL('../../internal/client/ui/index.html', import.meta.url), 'utf8').match(/data-langs="([^"]+)"/)[1].split(',');

const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9341;
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--ignore-certificate-errors',
  `--remote-debugging-port=${port}`, `--user-data-dir=${shots}/profile`, '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => proc.kill());

let ws, id = 0; const pending = new Map(); const problems = [];
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
  else if (d.method === 'Runtime.exceptionThrown') problems.push('exception: ' + JSON.stringify(d.params.exceptionDetails).slice(0, 300));
  else if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error' && d.params.entry.source !== 'network') problems.push('console: ' + d.params.entry.text);
};
const cdp = (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
async function ev(expr) {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error in ' + expr + ': ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}
async function waitFor(expr, ms = 10000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { if (await ev(`!!(${expr})`)) return; await sleep(50); }
  throw new Error('timeout waiting for ' + expr + '\n--- page text:\n' + await ev('document.body.innerText'));
}
const size = (w, h) => cdp('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: false });
const go = async url => { await cdp('Page.navigate', { url }); await sleep(120); await waitFor(`document.readyState === 'complete' && !document.documentElement.classList.contains('i18n-pending')`); };

let passed = 0; const failures = [];
const ok = (cond, what) => { if (cond) passed++; else { failures.push(what); console.log('  FAIL ' + what); } };

// Visible text and text-bearing attributes, with the Chinese parts picked out.
const leftovers = `(() => {
  const han = /\\p{Script=Han}/u, out = [];
  const walk = document.createTreeWalker(document.documentElement, NodeFilter.SHOW_TEXT);
  for (let n = walk.nextNode(); n; n = walk.nextNode()) {
    if (!n.parentElement.closest('script,style,.lang-picker') && han.test(n.nodeValue)) out.push(n.nodeValue.trim().slice(0, 60));
  }
  for (const el of document.querySelectorAll('[aria-label],[title],[alt],[placeholder],meta[name=description],meta[property^="og:"]')) {
    for (const a of ['aria-label', 'title', 'alt', 'placeholder', 'content']) { const v = el.getAttribute(a); if (v && han.test(v)) out.push(a + '=' + v.slice(0, 60)); }
  }
  return out;
})()`;
// Elements whose box leaves the viewport, ignoring anything inside a scroller or a clipped canvas.
const overflow = `(() => {
  const W = document.documentElement.clientWidth, out = [];
  for (const el of document.body.querySelectorAll('*')) {
    if (el.tagName === 'CANVAS' || !el.getClientRects().length || getComputedStyle(el).visibility === 'hidden') continue;
    const r = el.getBoundingClientRect();
    if (r.width === 0 || (r.right <= W + 1 && r.left >= -1)) continue;
    let scrolled = false;
    for (let p = el.parentElement; p; p = p.parentElement) { const o = getComputedStyle(p).overflowX; if ((o === 'auto' || o === 'scroll') && p !== document.body) scrolled = true; }
    if (!scrolled) out.push(el.tagName + '.' + el.className + ': ' + el.textContent.trim().slice(0, 40));
  }
  return out.slice(0, 5);
})()`;

async function inspect(lang, where) {
  const label = `${lang} ${where}`;
  const seen = await ev(`[document.documentElement.lang, document.documentElement.dir]`);
  ok(seen[0] === lang, `${label}: html lang is ${seen[0]}`);
  ok(seen[1] === (lang === 'ar' || lang === 'ur' ? 'rtl' : 'ltr'), `${label}: dir is ${seen[1]}`);
  if (!HAN_OK.has(lang)) { const left = await ev(leftovers); ok(left.length === 0, `${label}: Chinese left: ${JSON.stringify(left.slice(0, 5))}`); }
  const over = await ev(overflow);
  ok(over.length === 0, `${label}: off screen: ${JSON.stringify(over)}`);
  const noise = await ev(`document.body.innerText.match(/\\b(null|undefined|NaN)\\b|\\{\\w+\\}/)?.[0] ?? ''`);
  ok(!noise, `${label}: stray "${noise}" on the page`);
}
async function shot(name) {
  const r = await cdp('Page.captureScreenshot', { format: 'png' });
  writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64'));
}

await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable');
// The daemon's page and these checks' calls get the local API's key, as the
// page opened from the address `sharetoken daemon` prints would keep it.
const daemonKey = JSON.parse(readFileSync(STACK + '/desktop/run.json', 'utf8')).key;
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: `if (location.origin === ${JSON.stringify(new URL(daemon).origin)}) try { sessionStorage.setItem('st_local_key', ${JSON.stringify(daemonKey)}); } catch (e) {}
window.kfetch = (p, o = {}) => fetch(p, { ...o, headers: { ...(o.headers || {}), Authorization: 'Bearer ' + sessionStorage.getItem('st_local_key') } });` });
await fetch(`${daemon}/api/v1/logout`, { method: 'POST', headers: { Authorization: 'Bearer ' + daemonKey } });

// ---- the landing page and the logged-out portal and client ----
// Nothing chosen yet: English, whatever the browser's language is.
const userAgent = await ev('navigator.userAgent');
await cdp('Emulation.setUserAgentOverride', { userAgent, acceptLanguage: 'de-DE' });
for (const url of [`${portal}/`, `${portal}/app/`, `${daemon}/`]) {
  await go(url);
  await sleep(300);
  ok(await ev('navigator.language') === 'de-DE', `${url}: the browser says German`);
  ok(await ev('document.documentElement.lang') === 'en', `${url}: English by default, not the browser's language`);
}
await cdp('Emulation.setUserAgentOverride', { userAgent, acceptLanguage: 'en-US' });

for (const lang of LANGS) {
  await size(1280, 900);
  await go(`${portal}/?lang=${lang}`);
  await inspect(lang, 'site');
  // The live demos: tampering flips the verdict, the TEE switch changes the answer.
  await waitFor(`document.getElementById('verdict').textContent !== ''`);
  const before = await ev(`document.getElementById('verdict').textContent`);
  await ev(`document.querySelectorAll('.chunk')[2].click()`);
  await waitFor(`document.getElementById('verdict').classList.contains('bad')`);
  ok(await ev(`document.getElementById('verdict').textContent`) !== before, `${lang} site: tampering changes the verdict`);
  const answer = await ev(`document.getElementById('node-verdict').textContent`);
  await ev(`document.getElementById('tee').click()`);
  ok(await ev(`document.getElementById('node-verdict').textContent`) !== answer, `${lang} site: the TEE switch changes the answer`);
  await inspect(lang, 'site after demos');
  await shot(`site-${lang}`);
  await ev(`document.getElementById('security').scrollIntoView({ behavior: 'instant' })`); await sleep(200); await shot(`site-security-${lang}`);
  await size(390, 800);
  await go(`${portal}/?lang=${lang}`);
  await inspect(lang, 'site phone');
  await shot(`site-phone-${lang}`);

  await size(1280, 900);
  await go(`${portal}/app/?lang=${lang}#register`);
  await waitFor(`document.getElementById('login-email')`);
  await inspect(lang, 'login');
  await shot(`login-${lang}`);

  if (CLIENT_LANGS.includes(lang)) {
    await size(1040, 760);
    await go(`${daemon}/?lang=${lang}`);
    await waitFor(`document.getElementById('signin-form')`, 20000);
    await inspect(lang, 'client sign-in');
    await shot(`client-auth-${lang}`);
  }
}

// ---- signed in ----
await size(1280, 900);
await go(`${portal}/app/?lang=en`);
await waitFor(`document.getElementById('login-email')`);
const user = `i18n${Date.now()}@example.com`;
// A code redeems only with the nonce its request returned.
const nonce = await ev(`ST.api('POST', '/api/v1/auth/code', { email: ${JSON.stringify(user)} }).then(r => r.nonce)`);
await ev(`ST.api('POST', '/api/v1/web/auth/verify', { email: ${JSON.stringify(user)}, code: ${JSON.stringify(mailedCode(user))}, nonce: ${JSON.stringify(nonce)} }).then(() => true)`);
const ROUTES = ['home', 'ledger', 'jobs', 'tokens', 'devices', 'pairing', 'network', 'account', 'feedback'];
for (const lang of LANGS) {
  await size(1280, 900);
  await go(`${portal}/app/?lang=${lang}`);
  await waitFor(`document.querySelector('.sidenav')`);
  for (const r of ROUTES) {
    await ev(`location.hash = '#/${r}'`);
    await waitFor(`document.querySelector('.sidenav a.active')?.getAttribute('href') === '#/${r}' && document.querySelector('main h1')`);
    await sleep(120);
    await inspect(lang, `portal ${r}`);
    if (r === 'home' || r === 'account' || r === 'feedback') await shot(`portal-${r}-${lang}`);
  }
  await size(390, 800);
  await ev(`location.hash = '#/home'`); await sleep(400); // the sidebar takes .22 s to slide away after the resize
  await inspect(lang, 'portal phone');
  await shot(`portal-phone-${lang}`);
}

// The page an invite link opens (web/invite.js), signed in, at desktop and phone width.
const inviteToken = await ev(`ST.api('POST', '/api/v1/pairing/codes', { label: 'team' }).then(c => c.link_token)`);
for (const lang of LANGS) {
  await size(1280, 900);
  await go(`${portal}/p?lang=${lang}#${inviteToken}`);
  await waitFor(`document.getElementById('invite-title')`);
  await ev(`document.querySelector('.invite-later').open = true`);
  await inspect(lang, 'invite link page');
  await shot(`invite-${lang}`);
  await size(390, 800);
  await go(`${portal}/p?lang=${lang}#${inviteToken}`);
  await waitFor(`document.getElementById('invite-title')`);
  await inspect(lang, 'invite link page, phone');
}

// A dialog built from a translated sentence.
await size(1280, 900);
await go(`${portal}/app/?lang=${LANGS[0]}#/tokens`);
await waitFor(`document.querySelector('main tbody button.danger')`);
await ev(`document.querySelector('main tbody button.danger').click()`);
await waitFor(`document.querySelector('.modal p')`);
ok(await ev(`document.querySelector('.modal p').textContent.includes('web session')`), 'a confirm dialog fills in its placeholder');
await ev(`document.querySelector('.modal .actions button').click()`);

// ---- the picker, across pages, and the admin console that ignores it ----
if (LANGS.includes('ja') && LANGS.includes('de')) {
  await go(`${portal}/?lang=de`);
  await ev(`(() => { document.getElementById('lang-select').click(); document.querySelector('.lang-item[lang="ja"]').click(); })()`);
  await waitFor(`document.documentElement.lang === 'ja'`);
  ok(!(await ev('location.search')).includes('lang='), 'choosing a language drops ?lang= from the address');
  await go(`${portal}/app/`);
  ok(await ev('document.documentElement.lang') === 'ja', 'the choice carries from the site to the portal');
}
await go(`${portal}/admin/?lang=de`);
await waitFor(`document.querySelector('#app *')`);
ok(await ev('document.documentElement.lang') === 'zh-CN', 'the admin console stays in Chinese');
ok(!(await ev(`!!document.getElementById('lang-select')`)), 'the admin console has no language picker');
ok((await ev('document.body.innerText')).includes('管理控制台') || (await ev('document.body.innerText')).includes('登录'), 'the admin console shows its Chinese text');

// ---- the desktop client, signed in ----
await size(1040, 760);
await go(`${daemon}/?lang=en`);
const probe = await ev(`kfetch('/api/v1/portal/probe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }).then(r => r.json())`);
const clientUser = `i18n-client${Date.now()}@example.com`;
const post = (path, body) => ev(`kfetch(${JSON.stringify(path)}, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: ${JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
const sent = await post('/api/v1/auth/code', { pin: probe.fingerprint, email: clientUser });
const reg = await post('/api/v1/auth/verify', { pin: probe.fingerprint, email: clientUser, code: mailedCode(clientUser) });
ok(sent === 200 && reg === 200, `the client signs up with a mailed code (HTTP ${sent}, ${reg})`);
for (const lang of LANGS.filter(l => CLIENT_LANGS.includes(l))) {
  await go(`${daemon}/?lang=${lang}`);
  await waitFor(`document.getElementById('tile-available')`, 20000);
  await waitFor(`document.querySelector('#network-body table') || document.getElementById('network-down')`, 20000);
  await sleep(150);
  await inspect(lang, 'client');
  await shot(`client-${lang}`);
}
await ev(`kfetch('/api/v1/logout', { method: 'POST' })`);

for (const p of problems) ok(false, p);
console.log(`\n${passed} checks passed, ${failures.length} failed`);
process.exit(failures.length ? 1 : 0);
