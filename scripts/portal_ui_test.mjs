#!/usr/bin/env node
// Browser test of the portal's web pages: drives a headless Chrome over the
// DevTools protocol (no dependencies beyond Node 22) through the user portal
// and the admin console, checking the DOM after each step and saving
// screenshots.
//
//   node scripts/portal_ui_test.mjs http://127.0.0.1:8099 ./shots
//   ST_INSECURE=1 node scripts/portal_ui_test.mjs https://127.0.0.1:18092 ./shots   # self-signed (scripts/gui_stack.sh)
//
// Expects: open registration, an admin "root" with password "adminadmin",
// a user "alice" with password "alicealice", one relay registered.
import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createHmac } from 'node:crypto';
import { setTimeout as sleep } from 'node:timers/promises';

const base = process.argv[2] || 'http://127.0.0.1:8099';
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

// login signs in; an admin is taken through the two-factor step: enrollment
// the first time (the secret is kept beside the screenshots for later runs),
// a code afterwards.
async function login(user, pass) {
  await waitFor('document.querySelector("#login-user")');
  await type('#login-user', user); await type('#login-pass', pass);
  await click('button[type=submit]');
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
  await evaluate(`(() => { const i = document.querySelector('#mfa-code'); i.value = ${JSON.stringify(totp(secret))}; i.form.requestSubmit(); return true; })()`);
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
  await waitFor('document.querySelector("#login-user")');
  await shot('01-login');
  check((await text()).includes('注册'), 'registration tab shown because registration is open');
  await login('alice', 'wrongwrong');
  await assertText('用户名或密码错误', 'wrong password is reported');
  await login('alice', 'alicealice');
  await waitFor('document.querySelector(".sidenav")');
  await shot('02-home');
  await noNull('home');
  await assertText('可用余额');
  await assertText('alice');
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
  await assertText('203.0.113.7', 'relay listed on the network page');
  await assertText('延迟', 'latency column on the network page');
  await goto(base + '/app/#/account'); await sleep(600);
  await shot('04-account');
  await type('main form input[autocomplete=current-password]', 'alicealice');
  const newInputs = await evaluate('document.querySelectorAll("main form input[autocomplete=new-password]").length');
  check(newInputs === 2, 'two new-password fields');
  await evaluate(`[...document.querySelectorAll('main form input[autocomplete=new-password]')].forEach(i => { i.value = 'alicealice2'; })`);
  await click('main form button[type=submit]'); await sleep(600);
  await assertText('密码已修改', 'password changed via the page');
  await evaluate(`(() => { const f = document.querySelector('main form'); f.querySelector('input[autocomplete=current-password]').value = 'alicealice2'; [...f.querySelectorAll('input[autocomplete=new-password]')].forEach(i => i.value = 'alicealice'); f.querySelector('button[type=submit]').click(); return true; })()`);
  await sleep(600);

  // logout
  await clickText('.topbar button', '退出'); await sleep(900);
  await waitFor('document.querySelector("#login-user")');
  check(true, 'logout returns to the login page');
  const after = await evaluate(`fetch('/api/v1/me', {headers: {'X-Sharetoken-Web': '1'}}).then(r => r.status)`);
  check(after === 401, 'session is gone after logout');

  // registration from the page
  await clickText('.tabs button', '注册');
  const newUser = 'web' + Date.now().toString(36);
  await type('#login-user', newUser); await type('#login-pass', 'webuserpass');
  await click('button[type=submit]'); await sleep(900);
  await waitFor('document.querySelector(".sidenav")');
  await assertText(newUser, 'registered and signed in from the page');
  await clickText('.topbar button', '退出'); await sleep(800);

  // ---- feedback ----
  console.log('feedback');
  await login('alice', 'alicealice');
  await waitFor('document.querySelector(".sidenav")');
  await goto(base + '/app/#/feedback');
  await waitFor('document.querySelector("#feedback-form")');
  await evaluate(`document.querySelector('#feedback-send').click()`);
  await assertText('请填写反馈内容', 'empty feedback is stopped in the page');
  const HOSTILE = '<img src=x onerror="window.__fb=1">反馈内容';
  await evaluate(`document.querySelector('#feedback-text').value = ${JSON.stringify(HOSTILE)}; document.querySelector('#feedback-send').click()`);
  await waitFor(`document.querySelector('#feedback-list td .feedback-text')`);
  check(await evaluate(`document.querySelector('.feedback-text').textContent`) === HOSTILE && !(await evaluate(`!!document.querySelector('#feedback-list img') || !!window.__fb`)), 'feedback is listed as text, not markup');
  await assertText('已收到', 'and shows that it was received');
  await shot('05b-feedback');
  await clickText('.topbar button', '退出'); await sleep(800);

  // ---- admin console ----
  console.log('admin console');
  await goto(base + '/admin/');
  await login('alice', 'alicealice');
  await sleep(600);
  await assertText('不是管理员', 'non-admin is refused by the console');
  await shot('05-admin-refused');
  await clickText('button', '退出'); await sleep(800);
  await goto(base + '/admin/');
  await login('root', 'adminadmin');
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
  await type('input[type=search]', 'ali'); await sleep(600);
  const userRows = await evaluate('document.querySelectorAll("main tbody tr").length');
  check(userRows === 1, 'search narrows to alice');
  await shot('07-admin-users');
  await clickText('main tbody a', 'alice'); await sleep(800);
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
  await goto(base + '/admin/#/users/root'); await sleep(800);
  const selfButtons = await evaluate(`[...document.querySelectorAll('main button')].filter(b => b.disabled).map(b => b.textContent)`);
  check(selfButtons.includes('撤销管理员') && selfButtons.includes('禁用'), 'self demote/disable buttons are disabled: ' + selfButtons.join(','));
  const selfDemote = await evaluate(`fetch('/api/v1/admin/users/root/admin', {method: 'POST', headers: {'X-Sharetoken-Web': '1', 'Content-Type': 'application/json'}, body: '{"is_admin":false}'}).then(r => r.status)`);
  check(selfDemote === 400, 'API also refuses self-demotion');

  // create a user from the console
  await goto(base + '/admin/#/users'); await sleep(700);
  await clickText('button', '新建用户'); await sleep(300);
  const newbie = 'newbie' + Date.now().toString(36);
  await type('.modal input[type=text]', newbie); await type('.modal input[type=password]', 'newbiepass1');
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
  check(!(await text()).includes('反馈内容'), 'a handled message leaves the waiting list');
  await evaluate(`(() => { const s = document.querySelector('#feedback-status'); s.value = 'done'; s.dispatchEvent(new Event('change')); return true; })()`); await sleep(500);
  await assertText('反馈内容', 'and is under handled');

  // read-only pages render
  for (const [page, label] of [['jobs', '作业'], ['nodes', '节点'], ['audits', '审计'], ['disputes', '争议'], ['ledger', '账本']]) {
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
