#!/usr/bin/env node
// Browser test of the portal's web pages: drives a headless Chrome over the
// DevTools protocol (no dependencies beyond Node 22) through the user portal
// and the admin console, checking the DOM after each step and saving
// screenshots.
//
//   scripts/test/gui_stack.sh start DIR && . DIR/env
//   STACK=DIR ST_INSECURE=1 node scripts/test/portal_ui_test.mjs "$PORTAL" ./shots
//
// Signing in takes a code mailed to the address: the test reads it from the
// stack's outbox (scripts/test/gui_stack.sh code), and creates what it needs there:
// alice-<run>@example.test and the admin root@example.test.
import { execFileSync, spawn } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createHmac } from 'node:crypto';
import { setTimeout as sleep } from 'node:timers/promises';

const base = process.argv[2] || 'https://127.0.0.1:18092';
const STACK = process.env.STACK;
if (!STACK) { console.error('STACK=<gui_stack.sh directory> is required: sign-in codes are read from its outbox'); process.exit(2); }
const ROOT = new URL('../..', import.meta.url).pathname;
const run = (cmd, ...args) => execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
const db = () => readFileSync(STACK + '/dsn', 'utf8').trim().replace(/.*\//, '');
const sql = q => run(ROOT + 'scripts/test/local-mysql.sh', 'sql', db(), q);
const portalCmd = (...args) => { try { return run(STACK + '/bin/portal', '-db', readFileSync(STACK + '/dsn', 'utf8').trim(), ...args); } catch (e) { return ''; } };
// An address may get one code a minute and five an hour: a test signing in
// again and again starts each time from a clean slate.
const forgetCodes = email => sql(`DELETE FROM mail_codes WHERE email='${email}'; DELETE FROM mail_outbox WHERE to_addr='${email}'`);
const mailedCode = email => run(ROOT + 'scripts/test/gui_stack.sh', 'code', STACK, email);
// alice is new on every run, so that a run finds her as the page leaves a new account.
const ALICE = `alice-${Date.now().toString(36)}@example.test`, ADMIN = 'root@example.test';
portalCmd('useradd', ALICE); portalCmd('useradd', '-admin', ADMIN);
const shots = process.argv[3] || './shots';
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9333;
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${port}`,
  `--user-data-dir=${shots}/profile`, '--window-size=1280,900', ...(process.env.ST_INSECURE ? ['--ignore-certificate-errors'] : []), 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => proc.kill());

let ws, id = 0; const pending = new Map(); const events = [];
async function connect() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
      const page = list.find(t => t.type === 'page');
      if (page) { ws = new WebSocket(page.webSocketDebuggerUrl); break; }
    } catch (e) { /* not up yet */ }
    await sleep(200);
  }
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  ws.onmessage = m => {
    const d = JSON.parse(m.data);
    if (d.id && pending.has(d.id)) { const { res, rej } = pending.get(d.id); pending.delete(d.id); d.error ? rej(new Error(d.error.message)) : res(d.result); }
    else if (d.method) events.push(d);
  };
}
function cdp(method, params = {}) {
  return new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
}
async function evaluate(expr) {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}
async function shot(name) {
  const r = await cdp('Page.captureScreenshot', { format: 'png' });
  writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64'));
}
async function goto(url) { await cdp('Page.navigate', { url }); await sleep(600); }
async function waitFor(expr, ms = 8000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { if (await evaluate(`!!(${expr})`)) return; await sleep(100); }
  throw new Error('timeout waiting for ' + expr);
}
const text = () => evaluate('document.body.innerText');
let failures = 0;
function check(cond, what) { if (cond) console.log('  ok   ' + what); else { failures++; console.log('  FAIL ' + what); } }
async function assertText(sub, what) { check((await text()).includes(sub), what || `page shows "${sub}"`); }
async function noNull(where) { check(!/\bnull\b|undefined|NaN/.test(await text()), 'no stray null/undefined/NaN on ' + where); }
const consoleErrors = [];

// ---- helpers that act like a user ----
async function type(selector, value) {
  await evaluate(`(() => { const el = document.querySelector(${JSON.stringify(selector)}); el.focus(); el.value = ${JSON.stringify(value)}; el.dispatchEvent(new Event('input', {bubbles:true})); return true; })()`);
}
async function click(selector) { await evaluate(`(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) throw new Error('no element ' + ${JSON.stringify(selector)}); el.click(); return true; })()`); await sleep(400); }
async function clickText(tag, txt) {
  await evaluate(`(() => { const el = [...document.querySelectorAll(${JSON.stringify(tag)})].find(e => e.textContent.trim() === ${JSON.stringify(txt)}); if (!el) throw new Error('no ' + ${JSON.stringify(tag)} + ' with text ' + ${JSON.stringify(txt)}); el.click(); return true; })()`);
  await sleep(500);
}
// RFC 6238 TOTP (SHA-1, 6 digits, 30 s) for the admin's two-factor step.
function totp(secretB32, at = Date.now()) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '', bytes = [];
  for (const ch of secretB32.replace(/[\s=]/g, '').toUpperCase()) bits += alphabet.indexOf(ch).toString(2).padStart(5, '0');
  for (let i = 0; i + 8 <= bits.length; i += 8) bytes.push(parseInt(bits.slice(i, i + 8), 2));
  const step = Math.floor(at / 1000 / 30);
  const msg = Buffer.alloc(8); msg.writeUInt32BE(Math.floor(step / 2 ** 32), 0); msg.writeUInt32BE(step >>> 0, 4);
  const mac = createHmac('sha1', Buffer.from(bytes)).update(msg).digest();
  const off = mac[mac.length - 1] & 0x0f;
  const n = ((mac[off] & 0x7f) << 24 | mac[off + 1] << 16 | mac[off + 2] << 8 | mac[off + 3]) % 1_000_000;
  return String(n).padStart(6, '0');
}
const secretFile = user => `${shots}/mfa-${user}.secret`;
// A TOTP code works once: a second sign-in within the same 30 s step waits for the next.
const usedStep = {};
async function freshTotp(user, secret) {
  while (Math.floor(Date.now() / 30000) === usedStep[user]) await sleep(500);
  usedStep[user] = Math.floor(Date.now() / 30000);
  return totp(secret);
}

// sendCode fills in the address, has a code mailed and returns it.
async function sendCode(email) {
  await waitFor('document.querySelector("#login-email")');
  forgetCodes(email);
  await type('#login-email', email);
  await click('#send-code');
  return mailedCode(email);
}
// login signs in with a mailed code; an account with two-factor authentication
// is taken through that step: enrollment for an admin the first time (the
// secret is kept beside the screenshots), a code afterwards.
async function login(user) {
  const code = await sendCode(user);
  await type('#login-code', code);
  await click('#login-submit');
  await sleep(800);
  if (!await evaluate('!!document.querySelector("#mfa-code")')) return;
  let secret = await evaluate('(document.querySelector("#mfa-secret") || {}).textContent || ""');
  if (secret) {
    check(/^[A-Z2-7]{32}$/.test(secret) && await evaluate('!!document.querySelector(".qr img[src^=\'data:image/png\']")'), 'admin is made to enroll two-factor: QR code and secret shown');
    await shot('mfa-enroll');
    await type('#mfa-code', '000000'); await click('#mfa-code ~ * button[type=submit], form button[type=submit]'); await sleep(500);
    await assertText('验证码错误', 'wrong enrollment code is rejected');
    writeFileSync(secretFile(user), secret);
  } else {
    if (!existsSync(secretFile(user))) throw new Error(`no stored two-factor secret for ${user}; reset it with: portal mfa-reset ${user}`);
    secret = readFileSync(secretFile(user), 'utf8').trim();
    check(true, 'admin login asks for a two-factor code');
  }
  await evaluate(`(() => { const i = document.querySelector('#mfa-code'); i.value = ${JSON.stringify(await freshTotp(user, secret))}; i.form.requestSubmit(); return true; })()`);
  await sleep(1000);
  check(!await evaluate('!!document.querySelector("#mfa-code")'), 'two-factor code accepted');
}

try {
  await connect();
  await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable');
  await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "try { localStorage.setItem('st_lang', 'zh-CN'); } catch (e) {}" }); // these checks read the Chinese source text
  const origEvents = events;
  setInterval(() => { while (origEvents.length) { const e = origEvents.shift(); if (e.method === 'Runtime.exceptionThrown') consoleErrors.push(e.params.exceptionDetails.text + ' ' + (e.params.exceptionDetails.exception?.description || '')); if (e.method === 'Log.entryAdded' && e.params.entry.level === 'error' && !/Failed to load resource/.test(e.params.entry.text)) consoleErrors.push(e.params.entry.text); } }, 50);

  // ---- user portal ----
  console.log('user portal');
  await goto(base + '/app/');
  await waitFor('document.querySelector("#login-email")');
  await shot('01-login');
  check(!(await evaluate('!!document.querySelector(".tabs, input[type=password]")')), 'one sign-in form: no login/sign-up tabs, no password');
  await click('#login-submit');
  await assertText('请先填写邮箱', 'an empty address is stopped in the page');
  await type('#login-email', ALICE); await click('#login-submit');
  await assertText('请填写邮箱收到的验证码', 'no code, no request');
  {
    const code = await sendCode(ALICE);
    check(/^\d{6}$/.test(code), 'a code is mailed to an existing account');
    check(await evaluate('document.querySelector("#login-new").hidden'), 'an existing address is not told an account will be created');
    check((await evaluate('document.querySelector("#send-code").disabled')) && /\d/.test(await evaluate('document.querySelector("#send-code").textContent')), 'sending is paused with a countdown');
    await type('#login-code', code === '000000' ? '111111' : '000000'); await click('#login-submit'); await sleep(600);
    await assertText('验证码错误或已过期', 'a wrong code is reported');
    await shot('01b-bad-code');
    await type('#login-code', code); await click('#login-submit');
  }
  await waitFor('document.querySelector(".sidenav")');
  await shot('02-home');
  await noNull('home');
  await assertText('可用余额');
  await assertText(ALICE);
  check(!(await text()).includes('管理控制台'), 'ordinary user sees no admin link');

  // XSS: a token named with HTML must render as text, not markup.
  await goto(base + '/app/#/tokens'); await waitFor('document.querySelector("input[placeholder]")');
  const xss = '<img src=x onerror="document.title=\'pwned\'"><b>bold</b>';
  await type('main form input[type=text]', xss);
  await click('main form button[type=submit]');
  await waitFor('document.querySelector(".modal .secret code")');
  const secret = await evaluate('document.querySelector(".modal .secret code").textContent');
  check(secret.startsWith('st_') && secret.length === 67, 'new token shown once: ' + secret.slice(0, 10) + '…');
  await shot('03-token-created');
  await clickText('.modal button', '我已保存');
  await sleep(500);
  check((await text()).includes(xss), 'token name with HTML rendered literally');
  check(!(await evaluate('document.querySelector("main img, main b")')), 'no element was created from the token name');
  check((await evaluate('document.title')) !== 'pwned', 'onerror payload did not run');
  // the new token works as a bearer token for the CLI API
  const meViaToken = await evaluate(`fetch('/api/v1/me', {headers: {Authorization: 'Bearer ${secret}'}}).then(r => r.status)`);
  check(meViaToken === 200, 'created token authenticates on the API');
  // revoke it
  const rowsBefore = await evaluate('document.querySelectorAll("main tbody tr").length');
  await evaluate(`(() => { const tr = [...document.querySelectorAll('main tbody tr')].find(r => r.textContent.includes('<img')); tr.querySelector('button.danger').click(); return true; })()`);
  await sleep(300); await clickText('.modal button', '吊销'); await sleep(600);
  check((await evaluate('document.querySelectorAll("main tbody tr").length')) === rowsBefore - 1, 'token revoked from the list');
  const meAfter = await evaluate(`fetch('/api/v1/me', {headers: {Authorization: 'Bearer ${secret}'}}).then(r => r.status)`);
  check(meAfter === 401, 'revoked token is refused');

  // CSRF from the page's own context: a fetch without the custom header is refused even with the cookie.
  const noHeader = await evaluate(`fetch('/api/v1/me', {credentials: 'same-origin'}).then(r => r.status)`);
  check(noHeader === 403, 'cookie without X-Sharetoken-Web header → 403');
  const cookieVisible = await evaluate('document.cookie');
  check(!cookieVisible.includes('st_session'), 'session cookie is HttpOnly (invisible to scripts)');

  // network, account pages
  await goto(base + '/app/#/network'); await sleep(600);
  check(await evaluate('document.querySelectorAll("main tbody tr").length') >= 1, 'relay listed on the network page');
  await assertText('延迟', 'latency column on the network page');
  await goto(base + '/app/#/account'); await sleep(600);
  await shot('04-account');
  check(!(await evaluate('!!document.querySelector("main input[type=password]")')), 'the account page has no password to change');
  await assertText('未启用', 'two-factor authentication is off at first');
  // Turning two-factor authentication on: QR code and secret, a wrong first code refused.
  await click('#mfa-on');
  await waitFor('document.querySelector("#mfa-on-form .qr img")');
  const aliceSecret = await evaluate('document.querySelector("#mfa-secret").textContent');
  check(/^[A-Z2-7]{32}$/.test(aliceSecret) && await evaluate('document.querySelector(".qr img").src.startsWith("data:image/png")'), 'turning it on shows a QR code and the secret');
  await shot('04b-mfa-setup');
  await type('#mfa-on-code', '000000'); await click('#mfa-on-form button[type=submit]'); await sleep(600);
  check((await evaluate('document.querySelector("#mfa-on-form .error").textContent')) !== '', 'a wrong first code is refused');
  await type('#mfa-on-code', await freshTotp(ALICE, aliceSecret)); await click('#mfa-on-form button[type=submit]');
  await waitFor('document.querySelector("#mfa-off-form")');
  check((await evaluate('document.querySelector("#mfa-state").textContent')).includes('已启用'), 'two-factor authentication is on');
  writeFileSync(secretFile(ALICE), aliceSecret);

  // logout
  await clickText('.topbar button', '退出'); await sleep(900);
  await waitFor('document.querySelector("#login-email")');
  check(true, 'logout returns to the login page');
  const after = await evaluate(`fetch('/api/v1/me', {headers: {'X-Sharetoken-Web': '1'}}).then(r => r.status)`);
  check(after === 401, 'session is gone after logout');

  // With two-factor authentication, the mailed code alone is not enough.
  await type('#login-code', await sendCode(ALICE)); await click('#login-submit');
  await waitFor('document.querySelector("#mfa-form")');
  check(!(await evaluate('!!document.querySelector(".sidenav")')), 'the mailed code alone does not sign in an account with two-factor authentication');
  await shot('04c-mfa-login');
  await type('#mfa-code', await freshTotp(ALICE, aliceSecret)); await click('#mfa-form button[type=submit]');
  await waitFor('document.querySelector(".sidenav")');
  check(true, 'the authenticator code completes the sign-in');
  await goto(base + '/app/#/account');
  await waitFor('document.querySelector("#mfa-off-form")');
  await type('#mfa-off-code', '000000'); await click('#mfa-off-form button[type=submit]'); await sleep(600);
  check((await evaluate('document.querySelector("#mfa-off-form .error").textContent')) !== '', 'turning it off needs a right code');
  await type('#mfa-off-code', await freshTotp(ALICE, aliceSecret)); await click('#mfa-off-form button[type=submit]');
  await waitFor('document.querySelector("#mfa-on")');
  check((await evaluate('document.querySelector("#mfa-state").textContent')).includes('未启用'), 'and it is off again');
  await clickText('.topbar button', '退出'); await sleep(900);

  // A new address: the same form signs it up.
  const newUser = 'web' + Date.now().toString(36) + '@example.test';
  {
    const code = await sendCode(newUser);
    await waitFor('!document.querySelector("#login-new").hidden');
    await assertText('验证后会自动创建', 'a new address is told an account will be created');
    await assertText('新账户赠送 5 credit', 'with the sign-up credit');
    await shot('04d-new-account');
    await type('#login-code', code); await click('#login-submit');
  }
  await waitFor('document.querySelector(".sidenav")');
  await assertText(newUser, 'signed up and signed in from the page');
  await clickText('.topbar button', '退出'); await sleep(800);

  // ---- feedback ----
  console.log('feedback');
  await login(ALICE);
  await waitFor('document.querySelector(".sidenav")');
  await goto(base + '/app/#/feedback');
  await waitFor('document.querySelector("#feedback-form")');
  await evaluate(`document.querySelector('#feedback-send').click()`);
  await assertText('请填写反馈内容', 'empty feedback is stopped in the page');
  const HOSTILE = '<img src=x onerror="window.__fb=1">反馈内容 ' + Date.now().toString(36);
  await evaluate(`document.querySelector('#feedback-text').value = ${JSON.stringify(HOSTILE)}; document.querySelector('#feedback-send').click()`);
  await waitFor(`document.querySelector('#feedback-list td .feedback-text')`);
  check(await evaluate(`document.querySelector('.feedback-text').textContent`) === HOSTILE && !(await evaluate(`!!document.querySelector('#feedback-list img') || !!window.__fb`)), 'feedback is listed as text, not markup');
  await assertText('已收到', 'and shows that it was received');
  await shot('05b-feedback');

  // ---- pairing ----
  console.log('pairing');
  const q = sel => `document.querySelector(${JSON.stringify(sel)})`;
  await goto(base + '/app/#/pairing');
  await waitFor(q('#pair-code-form'));
  await assertText('最多同时配对 3 个卖家', 'the pairing page states the limit of three sellers');
  check(await evaluate(`[...${q('#pair-ttl')}.options].map(o => o.value).join()`) === '24,168,720', 'a code lasts one, seven or thirty days');
  check(!(await evaluate(`!!${q('#pair-restricted')}`)) && !(await evaluate(`${q('#pair-private')}.checked`)), 'not paired and not private at first');
  await click('#pair-private');
  await waitFor(`${q('#pair-private')}?.checked && !${q('#pair-private')}.disabled`);
  await assertText('私有模式已开启', 'private mode is switched on from the page');
  const LABEL = '<b onclick="window.__pl=1">team</b>';
  await type('#pair-label', LABEL); await type('#pair-uses', '5');
  await evaluate(`(() => { const t = ${q('#pair-ttl')}; t.value = '720'; t.dispatchEvent(new Event('change')); return true; })()`);
  await click('#pair-new-code');
  await waitFor(q('#pair-new .secret code'));
  const pairCode = await evaluate(`${q('#pair-new .secret code')}.textContent`);
  check(/^[A-Z2-7]{5}-[A-Z2-7]{5}$/.test(pairCode), 'a new pairing code is shown');
  check((await evaluate(`${q('#p-new-link')}.textContent`)).startsWith(base + '/p#') && /\/p#[0-9A-Za-z]{12}$/.test(await evaluate(`${q('#p-new-link')}.textContent`)), 'and its link on this portal, /p#<12 characters>');
  check(await evaluate(`!!${q('#p-new-text')}`), 'with a button that copies an invitation message');
  await shot('05c-pairing-code');
  await click('.modal .actions button.primary');
  check((await evaluate(`${q('#pair-codes')}.textContent`)).includes(pairCode) && (await evaluate(`${q('#pair-codes')}.textContent`)).includes('0 / 5')
    && (await evaluate(`${q('#pair-codes')}.textContent`)).includes(LABEL) && !(await evaluate(`!!${q('#pair-codes b')} || !!window.__pl`)), 'the code is listed with its uses, and its label as text');
  const days = await evaluate(`fetch('/api/v1/pairing', { headers: { 'X-Sharetoken-Web': '1' }, credentials: 'same-origin' }).then(r => r.json()).then(v => (v.codes[0].expires_at / 1e6 - Date.now()) / 86400e3)`);
  check(days > 29.9 && days < 30.1, 'and lasts the thirty days chosen');
  check((await evaluate(`${q('#pair-codes')}.textContent`)).includes('复制链接') && (await evaluate(`${q('#pair-codes')}.textContent`)).includes('复制邀请文案'), 'the listed code offers its link and message');
  await type('#pair-code', pairCode); await click('#pair-join');
  await assertText('自己的配对码', 'nobody pairs with their own code');
  await clickText('.topbar button', '退出'); await sleep(800);
  await login(newUser);
  await waitFor('document.querySelector(".sidenav")');
  await goto(base + '/app/#/pairing');
  await waitFor(q('#pair-join-form'));
  await type('#pair-code', 'AAAAA-AAAAA'); await click('#pair-join');
  await assertText('配对码不对', 'a wrong code is refused');
  await type('#pair-code', pairCode.toLowerCase()); await click('#pair-join');
  await waitFor(q('#pair-restricted'));
  check((await evaluate(`${q('#pair-sellers')}.textContent`)).includes('al***') && !(await evaluate(`${q('#pair-sellers')}.textContent`)).includes('alice'), "the buyer is paired and sees the seller's masked name");
  await shot('05d-pairing-buyer');
  await noNull('pairing');
  await clickText('.topbar button', '退出'); await sleep(800);
  await login(ALICE);
  await waitFor('document.querySelector(".sidenav")');
  await goto(base + '/app/#/pairing');
  await waitFor(q('#pair-buyers table'));
  check((await evaluate(`${q('#pair-buyers')}.textContent`)).includes('we***') && (await evaluate(`${q('#pair-codes')}.textContent`)).includes('1 / 5'), 'the seller sees its buyer, and the code counts it');
  await shot('05e-pairing-seller');

  // ---- invite links: the page a link opens, before and after signing in ----
  console.log('invite links');
  const web = (method, path, body) => evaluate(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'X-Sharetoken-Web': '1', 'Content-Type': 'application/json' }, credentials: 'same-origin', body: ${JSON.stringify(body ? JSON.stringify(body) : null)} }).then(r => r.json())`);
  const pairLink = (await web('POST', '/api/v1/pairing/codes', { label: 'friends' })).link_token;
  const shareAsk = (await web('POST', '/api/v1/sharing/invites', { ttl_hours: 24, approval: true })).link_token;
  check(/^[0-9A-Za-z]{12}$/.test(pairLink) && /^[0-9A-Za-z]{12}$/.test(shareAsk), 'a code and an invite (asking for approval) get link tokens');
  await clickText('.topbar button', '退出'); await sleep(800);
  // The downloads the page offers are the portal's own packages, this system's first.
  const dlDir = readFileSync(STACK + '/env', 'utf8').match(/^DOWNLOADS=(.*)$/m)[1];
  const fakePkgs = ['macos-arm64.dmg', 'macos-amd64.dmg', 'windows-amd64.exe', 'linux-amd64.deb'].map(s => `${dlDir}/sharetoken-9.9.9-${s}`);
  for (const f of fakePkgs) writeFileSync(f, 'package');
  await goto(base + '/p#' + pairLink);
  await waitFor(q('#invite-title'));
  check((await evaluate(`${q('#invite-title')}.textContent`)).includes('al***') && !(await text()).includes(ALICE), "the page names the seller, masked, before anyone signs in");
  check((await evaluate(`${q('#invite-label')}.textContent`)).includes('friends') && await evaluate(`!!${q('#invite-expiry')}`), 'with the label and how long the link works');
  check((await evaluate(`${q('#invite-accept')}.textContent`)).includes('登录并接受邀请') && await evaluate(`!!${q('#invite-open')}`), 'it offers to sign in and accept, and to open the installed app');
  check(!(await evaluate(`!!${q('#invite-inapp')}`)), 'no chat-app warning in a browser');
  await shot('06a-invite-page');
  await noNull('invite page');
  // A newcomer signs up from the page and the invite is accepted on the new account.
  const NEWCOMER = 'link' + Date.now().toString(36) + '@example.test';
  await click('#invite-accept');
  await waitFor(q('#login-email'));
  await assertText('登录或注册后接受邀请', 'accepting asks to sign in or sign up first');
  {
    const code = await sendCode(NEWCOMER);
    await type('#login-code', code); await click('#login-submit');
  }
  await waitFor(q('#invite-result'), 12000);
  check(await evaluate(`${q('#invite-result')}.dataset.pending`) === '0' && (await text()).includes('已与卖家配对'), 'signed up, the newcomer is paired at once');
  check((await text()).includes('用 ' + NEWCOMER + ' 登录客户端'), 'and told to sign in to the app with the same address');
  check(await evaluate(`!!${q('#invite-download')}`), 'with the downloads next');
  check(await evaluate(`[...document.querySelectorAll('#invite-dl-here a')].map(a => a.dataset.os + ' ' + a.getAttribute('href')).join()`) === 'macos /downloads/sharetoken-9.9.9-macos-arm64.dmg,macos /downloads/sharetoken-9.9.9-macos-amd64.dmg'
    && await evaluate(`document.querySelectorAll('#invite-download details a[data-os]').length`) === 2, "this system's packages first (Apple Silicon, then Intel), the others behind a fold");
  await shot('06b2-invite-downloads');
  for (const f of fakePkgs) rmSync(f);
  await shot('06b-invite-accepted');
  const paired = await web('GET', '/api/v1/pairing');
  check(paired.sellers.length === 1 && paired.sellers[0].username.startsWith('al***') && paired.sellers[0].label === 'friends', "the pairing is on the newcomer's account");
  // An invite that asks for approval: the request waits for the inviter.
  await goto(base + '/s#' + shareAsk);
  await waitFor(q('#invite-accept'));
  check((await evaluate(`${q('#invite-title')}.textContent`)).includes('共享伙伴') && await evaluate(`!!${q('#invite-approval')}`), 'a share invite says so, and that the inviter approves');
  check((await evaluate(`${q('#invite-who')}.textContent`)).includes(NEWCOMER), 'and which account accepts it');
  await click('#invite-accept');
  await waitFor(q('#invite-result'), 12000);
  check(await evaluate(`${q('#invite-result')}.dataset.pending`) === '1' && (await text()).includes('等待对方确认'), 'accepting files a request that waits for approval');
  await shot('06c-invite-pending');
  // Broken and dead links.
  await goto(base + '/p#AAAAAAAAAAAA'); await waitFor(q('#invite-error'));
  await assertText('邀请已失效', 'a dead link says the invite no longer works');
  await goto(base + '/s#'); await sleep(300); await evaluate('location.reload()'); await sleep(800); await waitFor(q('#invite-error'));
  await assertText('邀请链接不完整', 'a cut link says it is incomplete');
  // Inside a chat app the page warns that downloads may be blocked; on a phone it offers no app to open.
  await cdp('Network.setUserAgentOverride', { userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.50' });
  await goto(base + '/p#' + pairLink); await waitFor(q('#invite-title'));
  check(await evaluate(`!!${q('#invite-inapp')}`) && !(await evaluate(`!!${q('#invite-open')}`)), 'in WeChat on a phone: the browser hint, and no button to open the app');
  await shot('06d-invite-wechat');
  await cdp('Network.setUserAgentOverride', { userAgent: '' });
  // The inviter approves the request from the console.
  await goto(base + '/app/'); await sleep(500);
  await clickText('.topbar button', '退出'); await sleep(800);
  await login(ALICE);
  await waitFor('document.querySelector(".sidenav")');
  await goto(base + '/app/#/pairing');
  await waitFor(q('#sharing-requests'));
  check((await evaluate(`${q('#sharing-requests')}.textContent`)).includes('li***'), 'the request waits for the inviter, the account masked');
  await shot('06e-invite-request');
  await click('#sharing-requests .sharing-approve');
  await waitFor(`!${q('#sharing-requests')} && ${q('#partner-list')}?.textContent.includes('li***')`);
  check(true, 'approved: the newcomer is a share partner');
  await clickText('.topbar button', '退出'); await sleep(800);

  // ---- admin console ----
  console.log('admin console');
  await goto(base + '/admin/');
  await login(ALICE);
  await sleep(600);
  await assertText('不是管理员', 'non-admin is refused by the console');
  await shot('05-admin-refused');
  await clickText('button', '退出'); await sleep(800);
  await goto(base + '/admin/');
  await login(ADMIN);
  await waitFor('document.querySelector(".sidenav")');
  await waitFor('document.querySelector(".dash-chart")');
  await assertText('中继状态', 'the console opens on the dashboard');
  check(await evaluate('document.querySelectorAll(".dash-chart").length') === 3, 'dashboard draws the three daily charts');
  check(await evaluate('document.querySelectorAll("main .card tbody tr").length') >= 1, 'dashboard lists the relays');
  await shot('06a-admin-dashboard');
  await goto(base + '/admin/#/overview'); await sleep(700);
  await shot('06-admin-overview');
  await assertText('平台总览');
  await assertText('账户余额');

  // users: search, detail, credit, disable/enable, admin toggle guard
  await goto(base + '/admin/#/users'); await sleep(700);
  await type('input[type=search]', ALICE); await sleep(600);
  const userRows = await evaluate('document.querySelectorAll("main tbody tr").length');
  check(userRows === 1, 'search narrows to alice');
  await shot('07-admin-users');
  await clickText('main tbody a', ALICE); await sleep(800);
  check(!(await text()).includes('重置密码'), 'no password to reset');
  await assertText('发放 / 扣减 credit');
  await clickText('button', '发放 / 扣减 credit'); await sleep(300);
  await type('.modal input[type=number]', '2.5'); await type('.modal input[type=text]', 'ui test top-up');
  await clickText('.modal button', '提交'); await sleep(800);
  await assertText('ui test top-up', 'credit grant appears in the ledger');
  await shot('08-admin-user-detail');
  await noNull('user detail');
  await clickText('button', '禁用'); await sleep(300); await clickText('.modal button', '禁用'); await sleep(800);
  await assertText('已禁用', 'user disabled');
  await clickText('button', '启用'); await sleep(800);
  check(!(await evaluate('document.querySelector("h1 .badge.bad")')), 'user enabled again');
  // self-protection: root cannot demote or disable itself
  await goto(base + '/admin/#/users/' + encodeURIComponent(ADMIN)); await sleep(800);
  const selfButtons = await evaluate(`[...document.querySelectorAll('main button')].filter(b => b.disabled).map(b => b.textContent)`);
  check(selfButtons.includes('撤销管理员') && selfButtons.includes('禁用'), 'self demote/disable buttons are disabled: ' + selfButtons.join(','));
  const selfDemote = await evaluate(`fetch('/api/v1/admin/users/${encodeURIComponent(ADMIN)}/admin', {method: 'POST', headers: {'X-Sharetoken-Web': '1', 'Content-Type': 'application/json'}, body: '{"is_admin":false}'}).then(r => r.status)`);
  check(selfDemote === 400, 'API also refuses self-demotion');

  // create a user from the console
  await goto(base + '/admin/#/users'); await sleep(700);
  await clickText('button', '新建用户'); await sleep(300);
  const newbie = 'newbie' + Date.now().toString(36) + '@example.test';
  check(!(await evaluate('!!document.querySelector(".modal input[type=password]")')), 'a new user gets no password');
  await type('#new-user-email', newbie);
  await clickText('.modal button', '创建'); await sleep(900);
  await assertText(newbie, 'user created and opened');

  // relays: edit, disable, enable
  await goto(base + '/admin/#/relays'); await sleep(700);
  await clickText('button', '编辑'); await sleep(300);
  await evaluate(`(() => { const i = document.querySelectorAll('.modal input'); i[0].value = 'relay-edited'; i[1].value = 'sg'; return true; })()`);
  await clickText('.modal button', '确定'); await sleep(800);
  await assertText('relay-edited', 'relay renamed');
  await clickText('button', '停用'); await sleep(800);
  await assertText('停用', 'relay disabled');
  await clickText('button', '启用'); await sleep(800);
  await shot('09-admin-relays');
  // bad address is reported, not crashed
  await clickText('button', '登记中继'); await sleep(300);
  await evaluate(`(() => { document.querySelector('.modal textarea').value = '/ip4/1.2.3.4/tcp/1'; return true; })()`);
  await clickText('.modal button', '登记'); await sleep(800);
  check((await evaluate('document.querySelector(".toast.bad")?.textContent || ""')).includes('peer id'), 'invalid relay address rejected with a message');

  // disputes: resolve one from the page
  await goto(base + '/admin/#/disputes'); await sleep(700);
  if ((await text()).includes('退款给买家')) {
    await clickText('button', '退款给买家'); await sleep(300); await clickText('.modal button', '退款'); await sleep(900);
    check(!(await text()).includes('退款给买家'), 'dispute resolved from the page');
    await shot('09b-admin-disputes');
    await goto(base + '/admin/#/jobs'); await sleep(700);
    await assertText('已退款', 'refunded job listed');
    await evaluate(`(() => { const s = document.querySelector('main select'); s.value = 'final'; s.dispatchEvent(new Event('change')); return true; })()`); await sleep(700);
    const rows = await evaluate('[...document.querySelectorAll("main tbody tr")].map(r => r.textContent)');
    check(rows.length >= 1 && rows.every(r => r.includes('已完成')), 'status filter applied: ' + rows.length + ' rows');
    await shot('09c-admin-jobs');
    await noNull('admin jobs');
  } else console.log('  skip no open dispute seeded');

  // feedback: seen, marked handled, reopened
  await goto(base + '/admin/#/feedback');
  await waitFor(`document.querySelector('main .feedback-text')`);
  check(await evaluate(`document.querySelector('main .feedback-text').textContent`) === HOSTILE && !(await evaluate(`!!document.querySelector('main img') || !!window.__fb`)), 'the admin sees feedback as text');
  check(await evaluate(`Number(document.querySelector('#feedback-new').textContent) >= 1`), 'the nav counts waiting feedback');
  await shot('10a-admin-feedback');
  await clickText('main button', '标记已处理'); await sleep(500);
  check(!(await text()).includes(HOSTILE), 'a handled message leaves the waiting list');
  await evaluate(`(() => { const s = document.querySelector('#feedback-status'); s.value = 'done'; s.dispatchEvent(new Event('change')); return true; })()`); await sleep(500);
  await assertText(HOSTILE, 'and is under handled');

  // pairings: full names, the private seller marked, search
  await goto(base + '/admin/#/pairings');
  await waitFor(`document.querySelector('main .list-body table')`);
  check((await evaluate(`document.querySelector('main .list-body').textContent`)).includes(newUser) && (await evaluate(`document.querySelector('main .list-body').textContent`)).includes(ALICE)
    && (await evaluate(`document.querySelector('main .list-body').textContent`)).includes('私有'), 'the admin sees who is paired with whom, and that the seller is private');
  check((await evaluate(`document.querySelector('main .list-body').textContent`)).includes('<b onclick') && !(await evaluate(`!!document.querySelector('main .list-body b') || !!window.__pl`)), 'with the code label as text');
  await shot('10b-admin-pairings');
  await noNull('admin pairings');
  await evaluate(`(() => { const i = document.querySelector('#pairings-user'); i.value = 'no-such-user'; i.dispatchEvent(new Event('input')); return true; })()`); await sleep(900);
  await assertText('没有配对关系', 'searching the pairings by account name');

  // inference audits: the calibration card, token-level scores and the per-question modal
  const now = Date.now() * 1e6;
  sql(`INSERT INTO audit_calibrations (at, model, method, samples, honest_mean, honest_median, honest_p001, fail_rate, threshold, drift, detail)
    VALUES (${now}, 'ui-ref-model', 'tf', 30, 0.951, 0.953, 0.903, 0, 0.883, 0, '30 questions')`);
  const scores = JSON.stringify([{ job: 'j1', via: '12D3KooWRelay', billed: [20, 64], question: 'Explain tides to a teenager.', answer: 'Tides are <b>the</b> rise and fall of the sea.',
    score: { method: 'tf', tokens: 64, top1: 0.68, calls: 65 } }]).replace(/'/g, "''");
  sql(`INSERT INTO audits (at, job_id, node, model, verdict, similarity, detail, method, questions, score, threshold, shadow, scores)
    VALUES (${now}, 'ui-audit-job', '12D3KooWNode', 'ui-ref-model', 'mismatch', 0, 'answers differ from the reference model', 'tf', 5, 0.684, 0.883, 1, '${scores}')`);
  await goto(base + '/admin/#/audits'); await sleep(900);
  await assertText('参考模型校准', 'the audits page shows the reference calibrations');
  await assertText('0.883', 'with the calibrated threshold');
  await assertText('0.684 / 0.883', 'an audit shows its score against the threshold');
  await assertText('影子', 'and that it ran in shadow mode');
  await clickText('main button', '明细'); await sleep(500);
  await assertText('Explain tides to a teenager.', 'the detail modal lists each question');
  check(!(await evaluate(`!!document.querySelector('.audit-modal .audit-val b')`)), 'with the answer as text, not HTML');
  await shot('10c-admin-audit-detail');
  await clickText('.modal button', '关闭'); await sleep(300);

  // read-only pages render
  for (const [page, label] of [['jobs', '作业'], ['nodes', '节点'], ['audits', '审计'], ['disputes', '争议'], ['partners', '共享伙伴'], ['ledger', '账本']]) {
    await goto(base + '/admin/#/' + page); await sleep(700);
    await assertText(label, 'admin page ' + page + ' renders');
    await noNull(page);
  }
  await shot('10-admin-ledger');
  await clickText('.topbar button', '退出'); await sleep(800);
} catch (e) {
  failures++; console.log('  ERROR ' + e.message);
  await shot('error').catch(() => {});
}
if (consoleErrors.length) { failures++; console.log('  console errors:\n    ' + consoleErrors.join('\n    ')); } else console.log('  ok   no console errors');
console.log(failures ? `FAILED (${failures})` : 'ALL OK');
proc.kill();
process.exit(failures ? 1 : 0);
