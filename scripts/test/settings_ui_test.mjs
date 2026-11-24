#!/usr/bin/env node
// Browser test of the system settings page, the audit log, invite codes and
// paged listings: drives a headless Chrome over the DevTools protocol through
// what an administrator and a user do. Works against a local stack
// (scripts/test/gui_stack.sh) and against dev.
//
//   STACK=DIR ST_INSECURE=1 node scripts/test/settings_ui_test.mjs <portal url> <dir for screenshots>   # scripts/test/gui_stack.sh
//   node scripts/test/settings_ui_test.mjs <portal url> <dir for screenshots>                            # elsewhere, with ST_MAIL_CMD
//
// Signing in takes a code mailed to the address.
// Environment:
//   STACK                    a scripts/test/gui_stack.sh directory: codes come from its outbox, the admin is created there
//   ST_ADMIN                 an administrator's email (a stored or new two-factor secret lives in the dir)
//   ST_MAIL_CMD              without STACK: prints the newest queued mail to the address in $ST_MAIL_TO (subject and text)
//   ST_MAIL_HOLD_CMD         stops the mail server, if there is one: a sent code is wiped from the outbox
//   ST_MAIL_RELEASE_CMD      starts it again
//   ST_TEST_MAIL_TO          where the test mail goes; a server that accepts it shows "accepted"
//   ST_INSECURE=1            accept a self-signed certificate
//   ST_KEEP_SETTINGS=1       do not put the settings back at the end
import { spawn, execSync, execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createHmac, randomBytes } from 'node:crypto';
import { setTimeout as sleep } from 'node:timers/promises';

const base = process.argv[2];
const shots = process.argv[3] || './shots-settings';
if (!base) { console.error('usage: settings_ui_test.mjs <portal url> [dir]'); process.exit(2); }
mkdirSync(shots, { recursive: true });
const admin = process.env.ST_ADMIN || 'root@example.test';
const STACK = process.env.STACK;
const ROOT = new URL('../..', import.meta.url).pathname;
const runQuiet = (cmd, ...args) => execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
const stackSQL = q => runQuiet(ROOT + 'scripts/test/local-mysql.sh', 'sql', readFileSync(STACK + '/dsn', 'utf8').trim().replace(/.*\//, ''), q);
if (STACK) { try { runQuiet(STACK + '/bin/portal', '-db', readFileSync(STACK + '/dsn', 'utf8').trim(), 'useradd', '-admin', admin); } catch (e) { /* there already */ } }
const RUN = randomBytes(3).toString('hex');
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9300 + Math.floor(Math.random() * 400);
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${port}`,
  `--user-data-dir=${shots}/profile-${RUN}`, '--window-size=1280,900', ...(process.env.ST_INSECURE ? ['--ignore-certificate-errors'] : []), 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => proc.kill());

let ws, id = 0; const pending = new Map();
const consoleErrors = [];
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
    if (d.id && pending.has(d.id)) { const { res, rej } = pending.get(d.id); pending.delete(d.id); d.error ? rej(new Error(d.error.message)) : res(d.result); return; }
    if (d.method === 'Runtime.exceptionThrown') consoleErrors.push(d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
    if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error' && !/status of 4\d\d/.test(d.params.entry.text)) consoleErrors.push(d.params.entry.text);
  };
}
function cdp(method, params = {}) { return new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); }); }
async function evaluate(expr) {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}
async function shot(name) { const r = await cdp('Page.captureScreenshot', { format: 'png' }); writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64')); }
async function goto(url) { await cdp('Page.navigate', { url }); await sleep(700); }
async function waitFor(expr, ms = 8000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { if (await evaluate(`!!(${expr})`)) return true; await sleep(100); }
  throw new Error('timeout waiting for ' + expr);
}
const text = () => evaluate('document.body.innerText');
let failures = 0;
function check(cond, what) { if (cond) console.log('  ok   ' + what); else { failures++; console.log('  FAIL ' + what); } }
async function assertText(sub, what) { check((await text()).includes(sub), what || `page shows "${sub}"`); }
async function noNull(where) { check(!/\bnull\b|undefined|NaN/.test(await text()), 'no stray null/undefined/NaN on ' + where); }
const J = JSON.stringify;
async function type(sel, value) {
  await evaluate(`(() => { const el = document.querySelector(${J(sel)}); if (!el) throw new Error('no element ' + ${J(sel)}); el.focus(); el.value = ${J(value)}; el.dispatchEvent(new Event('input', {bubbles:true})); el.dispatchEvent(new Event('change', {bubbles:true})); return true; })()`);
}
async function click(sel) { await evaluate(`(() => { const el = document.querySelector(${J(sel)}); if (!el) throw new Error('no element ' + ${J(sel)}); el.click(); return true; })()`); await sleep(400); }
async function clickText(tag, txt) {
  await evaluate(`(() => { const el = [...document.querySelectorAll(${J(tag)})].find(e => e.textContent.trim() === ${J(txt)}); if (!el) throw new Error('no ' + ${J(tag)} + ' with text ' + ${J(txt)}); el.click(); return true; })()`);
  await sleep(500);
}
async function setChecked(sel, on) { await evaluate(`(() => { const el = document.querySelector(${J(sel)}); if (el.checked !== ${on}) el.click(); return true; })()`); }
async function apiCall(method, path, body) {
  return evaluate(`fetch(${J(path)}, { method: ${J(method)}, headers: { 'X-Sharetoken-Web': '1', 'Content-Type': 'application/json' }, credentials: 'same-origin', ${body === undefined ? '' : `body: ${J(J(body))},`} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
}
function totp(secretB32, at = Date.now()) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '', bytes = [];
  for (const ch of secretB32.replace(/[\s=]/g, '').toUpperCase()) bits += alphabet.indexOf(ch).toString(2).padStart(5, '0');
  for (let i = 0; i + 8 <= bits.length; i += 8) bytes.push(parseInt(bits.slice(i, i + 8), 2));
  const msg = Buffer.alloc(8), step = Math.floor(at / 1000 / 30);
  msg.writeUInt32BE(Math.floor(step / 2 ** 32), 0); msg.writeUInt32BE(step >>> 0, 4);
  const mac = createHmac('sha1', Buffer.from(bytes)).update(msg).digest(), off = mac[mac.length - 1] & 0x0f;
  return String(((mac[off] & 0x7f) << 24 | mac[off + 1] << 16 | mac[off + 2] << 8 | mac[off + 3]) % 1_000_000).padStart(6, '0');
}
const secretFile = u => `${shots}/mfa-${u}.secret`;
// sendCode asks for a code to the address in the form and returns the page's
// answer: '' when a code went out, else the error shown.
async function sendCode(email) {
  await waitFor('document.querySelector("#login-email")');
  // An address gets one code a minute and five an hour: on a stack, start clean.
  if (STACK) stackSQL(`DELETE FROM mail_codes WHERE email='${email}'; DELETE FROM mail_outbox WHERE to_addr='${email}'`);
  await type('#login-email', email);
  for (;;) {
    await click('#send-code'); await sleep(600);
    const err = await evaluate('document.querySelector("#login-error").textContent');
    if (!err.includes('刚发送过')) return err;
    await sleep(15000);
  }
}
// readCode returns the code just mailed to the address.
async function readCode(email) {
  if (STACK) return runQuiet(ROOT + 'scripts/test/gui_stack.sh', 'code', STACK, email);
  return (await mailTo(email, /code: (\d{6})/))[1];
}
// loginForm signs in with a mailed code, up to any two-factor step.
async function loginForm(email) {
  hold();
  try {
    const err = await sendCode(email);
    if (err) throw new Error('no code for ' + email + ': ' + err);
    await type('#login-code', await readCode(email));
  } finally { release(); }
  await click('#login-submit');
  await sleep(900);
}
// SHOWN is an expression: whether the element takes up room on the page.
const SHOWN = sel => `(e => !!e && e.getClientRects().length > 0)(document.querySelector(${JSON.stringify(sel)}))`;
async function adminLogin() {
  await goto(base + '/admin/');
  await loginForm(admin);
  await waitFor('document.querySelector("#mfa-code")');
  let secret = await evaluate('(document.querySelector("#mfa-secret") || {}).textContent || ""');
  if (secret) writeFileSync(secretFile(admin), secret);
  else secret = readFileSync(secretFile(admin), 'utf8').trim();
  // A code cannot be used twice: wait for a fresh step if the last login used this one.
  const rest = 30 - (Date.now() / 1000 % 30);
  if (existsSync(`${secretFile(admin)}.used`) && Number(readFileSync(`${secretFile(admin)}.used`, 'utf8')) === Math.floor(Date.now() / 30000)) await sleep((rest + 1) * 1000);
  await evaluate(`(() => { const i = document.querySelector('#mfa-code'); i.value = ${J(totp(secret))}; i.form.requestSubmit(); return true; })()`);
  writeFileSync(`${secretFile(admin)}.used`, String(Math.floor(Date.now() / 30000)));
  await waitFor('document.querySelector(".sidenav")', 10000);
}
// Leaves the page too: a page left signed out would reload itself on its next request, racing the next navigation.
async function logout() { await apiCall('POST', '/api/v1/web/logout'); await goto('about:blank'); }
const route = async r => { await evaluate(`location.hash = ${J('#/' + r)}`); await sleep(900); };
const sh = cmd => execSync(cmd, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
const hold = () => { if (process.env.ST_MAIL_HOLD_CMD) sh(process.env.ST_MAIL_HOLD_CMD); };
const release = () => { if (process.env.ST_MAIL_RELEASE_CMD) sh(process.env.ST_MAIL_RELEASE_CMD); };
async function mailTo(to, re) {
  for (let i = 0; i < 40; i++) {
    const out = execSync(process.env.ST_MAIL_CMD, { encoding: 'utf8', env: { ...process.env, ST_MAIL_TO: to } });
    const m = re.exec(out);
    if (m) return m;
    await sleep(500);
  }
  throw new Error('no mail to ' + to + ' matching ' + re);
}

async function run() {
  await connect();
  await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable');
  await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "try { localStorage.setItem('st_lang', 'zh-CN'); localStorage.setItem('st_theme', 'light'); } catch (e) {}" });

  console.log('== admin sign-in, settings page');
  await adminLogin();
  const original = (await apiCall('GET', '/api/v1/admin/settings')).body;
  check(original && original.settings, 'GET /api/v1/admin/settings');
  try {
    await route('settings');
    await waitFor('document.querySelector("#set-save")');
    for (const t of ['注册管控', '邮件', '安全', 'API 限流', '界面', '邀请码', '发送测试邮件', '邮箱域名白名单', '邮箱黑名单']) await assertText(t, 'settings shows ' + t);
    check(await evaluate('!document.querySelector("#set-site_name, #set-public_url, #set-logo, #set-docs_url")'), 'there is no site section');
    check(await evaluate('![...document.querySelectorAll("h2")].some(e => e.textContent.trim() === "站点")'), 'and no 站点 heading');
    check(!('site_name' in original.settings) && !('public_url' in original.settings) && !('logo' in original.settings), 'the API has no site fields');
    await noNull('settings');
    await shot('settings-top');
    await type('#set-mail_from', 'not an address'); await click('#set-save'); await sleep(400);
    check((await evaluate('document.querySelector("#set-error").textContent')).includes('mail_from'), 'a bad value is refused and named');
    check(await evaluate('document.activeElement && document.activeElement.id === "set-mail_from"'), 'the bad field takes the focus');
    const support = 'ST Test ' + RUN + ' <' + ((original.settings.mail_support || '').match(/<(.+)>/) || [, 'support@example.com'])[1] + '>';
    await type('#set-mail_from', original.settings.mail_from || 'ST Test <team@example.com>');
    await type('#set-mail_support', support);
    await click('#set-save'); await sleep(600);
    await assertText('设置已保存', 'saving says so');
    check((await evaluate('document.querySelector("#set-error").textContent')) === '', 'no error after saving');
    await cdp('Page.reload'); await sleep(900); await waitFor('document.querySelector("#set-save")');
    check((await evaluate('document.querySelector("#set-mail_support").value')) === support, 'the saved value is there after a reload');
    check((await evaluate('document.querySelector(".side-brand b").textContent')) === 'ShareToken', 'the sidebar keeps the built-in name');
    await assertText('上次由 ' + admin + ' 修改于', 'who changed the settings last');

    console.log('== test mail');
    const testTo = process.env.ST_TEST_MAIL_TO || `functest+${RUN}@example.com`;
    await type('#set-test-to', testTo); await click('#set-test-send'); await sleep(800);
    await waitFor('document.querySelector("#set-test-result a")');
    await assertText('在发件箱查看', 'the test mail is queued, with a link to it');
    await shot('settings-test-mail');
    if (process.env.ST_MAIL_RELEASE_CMD) {
      await waitFor(`/对方已接受|失败|重试中/.test(document.querySelector("#set-test-result").innerText)`, 70000);
      const res = await evaluate('document.querySelector("#set-test-result").innerText');
      console.log('       mail server says: ' + res.split('\n')[0]);
      check(!process.env.ST_TEST_MAIL_TO || res.includes('对方已接受'), 'the page follows the mail to its outcome');
      await shot('settings-test-mail-result');
    }

    console.log('== invite codes and registration rules');
    await click('#invite-new'); await sleep(300);
    await type('#invite-note', 'ui test ' + RUN); await type('#invite-uses', '1');
    await clickText('.modal button', '创建'); await sleep(600);
    await assertText('ui test ' + RUN, 'the new code is listed');
    const code = await evaluate('document.querySelector(".invite-code").textContent');
    check(/^[A-Z2-7]{16}$/.test(code), 'it looks like a code');
    const email = `functest+ui${RUN}@example.com`, blocked = `functest+blocked${RUN}@example.com`;
    let r = await apiCall('PUT', '/api/v1/admin/settings', { open_registration: true, invite_required: true, email_block: [blocked] });
    check(r.status === 200, 'invite required, one address blocked');
    await logout(); await goto(base + '/app/'); await sleep(600);
    await waitFor('document.querySelector("#login-email")');
    // Rendered, not just the attribute: a display rule once overrode [hidden].
    check(!(await evaluate(SHOWN('#login-invite'))), 'the form does not ask everybody for an invite code');
    check((await evaluate('document.querySelector(".login h1").textContent')) === 'ShareToken', 'the sign-in page keeps the built-in name');
    check(await evaluate('document.querySelector(".auth-logo").getAttribute("src") === "/static/favicon.svg"'), 'and the built-in logo');
    check((await sendCode(blocked)).includes('这个邮箱不能在此注册'), 'no code goes to a blocked address');
    check((await sendCode(email)).includes('新账户需要邀请码'), 'a new address is asked for an invite code');
    check(await evaluate(SHOWN('#login-invite')), 'and the invite field appears');
    await shot('register-invite');
    await type('#login-invite', 'WRONGCODE');
    check((await sendCode(email)).includes('邀请码错误'), 'no code without a good invite');
    await type('#login-invite', code);
    hold();
    let signupCode;
    try {
      check((await sendCode(email)) === '', 'with the invite, a code is mailed');
      signupCode = await readCode(email);
    } finally { release(); }
    await type('#login-code', signupCode);
    await click('#login-submit');
    await waitFor('document.querySelector(".sidenav")', 10000);
    check(true, 'signing up with the code works');

    console.log('== user pages: pager, dates, CSV');
    await route('ledger');
    await waitFor('document.querySelector(".pager-total")');
    await assertText('共 1 条', 'the bill has its total');
    await assertText('导出 CSV', 'and an export button');
    await noNull('the bill');
    await shot('user-ledger');
    const csv = await evaluate(`fetch('/api/v1/ledger?format=csv', { headers: { 'X-Sharetoken-Web': '1' } }).then(async r => r.status + ' ' + r.headers.get('content-type') + ' ' + await r.text())`);
    check(csv.startsWith('200 text/csv') && csv.includes('amount_credit') && csv.includes('sign-up credit'), 'the bill exports as CSV');
    await click('.export-csv'); await sleep(800);
    check(await evaluate('document.querySelector(".export-csv").textContent === "导出 CSV" && !document.querySelector(".toast.bad")'), 'the export button finishes without an error');
    await evaluate(`(() => { const p = document.querySelector('.date-preset'); p.value = '1'; p.dispatchEvent(new Event('change')); return true; })()`); await sleep(600);
    await assertText('共 1 条', 'today\'s bill still has the entry');
    await evaluate(`(() => { const f = document.querySelector('.date-from'); f.value = '2020-01-01'; f.dispatchEvent(new Event('change')); const t = document.querySelector('.date-to'); t.value = '2020-01-02'; t.dispatchEvent(new Event('change')); return true; })()`); await sleep(600);
    await assertText('共 0 条', 'a range in the past is empty');
    check(await evaluate(`document.querySelector('.date-preset').value === 'custom'`), 'typed dates show as a custom range');
    await route('jobs');
    await waitFor('document.querySelector("#jobs-role")');
    await assertText('共 0 条', 'jobs has a pager too');
    await route('feedback');
    await waitFor('document.querySelector("#feedback-form")');
    await type('#feedback-text', 'ui test ' + RUN); await click('#feedback-send'); await sleep(600);
    await assertText('共 1 条', 'my feedback is paged and refreshes');

    console.log('== no passwords');
    await logout();
    await goto(base + '/app/#reset=AAAAAAAAAAAAAAAAAAAAAAAA'); await sleep(600);
    await waitFor('document.querySelector("#login-email")');
    check(!(await evaluate('!!document.querySelector("#reset-form, #forgot-link, input[type=password]")')), 'an old reset link shows the sign-in form: there is no password to reset');
    for (const [path, body] of [['/api/v1/password/forgot', { email }], ['/api/v1/web/login', { username: email, password: 'x' }]]) {
      const st = (await apiCall('POST', path, body)).status;
      check(st === 404 || st === 405, path + ' is gone (' + st + ')');
    }
    await goto('about:blank');

    console.log('== admin listings');
    await adminLogin();
    await route('users');
    await waitFor('document.querySelector(".pager-total")');
    let total = Number((await evaluate('document.querySelector(".pager-total").textContent')).replace(/\D/g, ''));
    // Enough rows for two pages at the smallest size.
    for (let i = total; i < 13; i++) { const x = await apiCall('POST', '/api/v1/admin/users', { username: `functest-pg-${RUN}-${i}@example.com` }); if (x.status !== 201) throw new Error('seeding users: ' + J(x)); }
    await route('overview'); await route('users');
    await waitFor('document.querySelector(".pager-size")');
    await evaluate(`(() => { const s = document.querySelector('.pager-size'); s.value = '10'; s.dispatchEvent(new Event('change')); return true; })()`); await sleep(800);
    check(await evaluate(`document.querySelectorAll('.list-body tbody tr').length === 10`), 'page size 10 shows ten rows');
    check(await evaluate(`localStorage.getItem('st_page_size') === '10'`), 'the size is remembered');
    const firstRow = await evaluate(`document.querySelector('.list-body tbody tr').innerText`);
    await click('.pager-next'); await sleep(700);
    check(await evaluate(`document.querySelector('.pager-num.active').textContent === '2'`), 'next goes to page 2');
    check(firstRow !== await evaluate(`document.querySelector('.list-body tbody tr').innerText`), 'with other rows');
    await shot('admin-users-page2');
    await click('.pager-prev'); await sleep(700);
    check(firstRow === await evaluate(`document.querySelector('.list-body tbody tr').innerText`), 'previous comes back');
    await type('#users-search', `functest+ui${RUN}`); await sleep(900);
    await assertText('共 1 条', 'search counts what it finds');
    await click('.export-csv'); await sleep(800);
    await cdp('Page.reload'); await sleep(900); await waitFor('document.querySelector(".pager-size")');
    check(await evaluate(`document.querySelector('.pager-size').value === '10'`), 'the size survives a reload');
    for (const [page, sel] of [['jobs', '#jobs-status'], ['ledger', '#ledger-account'], ['audits', '.pager-total'], ['disputes', '.pager-total'], ['mail/inbox', '#mail-search'], ['mail/outbox', '#out-search'], ['feedback', '#feedback-status']]) {
      await route(page);
      await waitFor(`document.querySelector(${J(sel)}) && document.querySelector(".pager-total")`);
      check(await evaluate(`!!document.querySelector('.export-csv') && !!document.querySelector('.date-range')`), page + ': pager, dates, export');
      await noNull(page);
    }
    await route('mail/outbox');
    await type('#out-search', email); await sleep(800);
    check(!(await text()).includes(signupCode) && (await text()).includes('含验证码'), 'the outbox does not show the sign-in code');

    console.log('== blocklist');
    await route('settings'); await waitFor('document.querySelector("#set-block-apply")');
    r = await apiCall('PUT', '/api/v1/admin/settings', { email_block: [email] });
    check(r.status === 200, 'the new account is put on the blocklist');
    await cdp('Page.reload'); await sleep(900); await waitFor('document.querySelector("#set-block-apply")');
    await click('#set-block-apply'); await sleep(700);
    await waitFor('document.querySelector("#set-block-matched")');
    check((await evaluate('document.querySelector("#set-block-matched").textContent')) === email, 'a dry run names who it hits');
    await shot('settings-blocklist');
    await clickText('.modal button', '禁用'); await sleep(600);
    r = await apiCall('GET', '/api/v1/admin/users/' + encodeURIComponent(email));
    check(r.body.user.disabled === true, 'the account is disabled');

    console.log('== audit log');
    await route('oplog');
    await waitFor('document.querySelector("#oplog-action")');
    await shot('oplog');
    for (const t of ['修改系统设置', '创建邀请码', '禁用黑名单用户', '发送测试邮件', '导出用户']) await assertText(t, 'the log has ' + t);
    await noNull('the log');
    await evaluate(`(() => { const s = document.querySelector('#oplog-action'); s.value = 'settings.apply_blocklist'; s.dispatchEvent(new Event('change')); return true; })()`); await sleep(700);
    check(await evaluate(`[...document.querySelectorAll('.list-body tbody tr')].every(r => r.innerText.includes('禁用黑名单用户')) && document.querySelectorAll('.list-body tbody tr').length >= 2`), 'it filters by action');
    await evaluate(`(() => { const s = document.querySelector('#oplog-action'); s.value = ''; s.dispatchEvent(new Event('change')); return true; })()`);
    await type('#oplog-actor', admin); await sleep(800);
    const log = await evaluate(`fetch('/api/v1/admin/oplog?format=csv&actor=' + encodeURIComponent(${J(admin)}), { headers: { 'X-Sharetoken-Web': '1' } }).then(r => r.text())`);
    check(log.includes('settings.update') && !log.includes(signupCode), 'its export has the entries and no sign-in code');
    r = await apiCall('GET', '/api/v1/admin/oplog?action=' + encodeURIComponent('auth.admin_login') + '&actor=' + encodeURIComponent(admin));
    check(r.body.total >= 1, 'admin sign-ins are on record');
    await route('versions'); await sleep(300);
    await assertText('/ portal 4', 'the version page lists the new schema');

    console.log('== phone width, dark theme');
    await cdp('Emulation.setDeviceMetricsOverride', { width: 390, height: 800, deviceScaleFactor: 2, mobile: true });
    await route('oplog'); await sleep(500);
    check(await evaluate('document.documentElement.scrollWidth <= window.innerWidth + 1'), 'the log does not scroll sideways on a phone');
    await shot('oplog-phone');
    await route('settings'); await sleep(600);
    check(await evaluate('document.documentElement.scrollWidth <= window.innerWidth + 1'), 'nor do the settings');
    await shot('settings-phone');
    await cdp('Emulation.clearDeviceMetricsOverride');
    await evaluate(`THEME.toggle()`); await sleep(200);
    await route('oplog'); await sleep(300); await shot('oplog-dark');
    await route('settings'); await sleep(400); await shot('settings-dark');
    await evaluate(`THEME.toggle()`);
  } finally {
    release();
    if (!process.env.ST_KEEP_SETTINGS && original && original.settings) {
      // The test may have stopped anywhere, signed in as anyone.
      let back = { status: 'not tried' };
      try {
        await goto(base + '/admin/');
        back = await apiCall('PUT', '/api/v1/admin/settings', original.settings);
        if (back.status !== 200) {
          await logout();
          await adminLogin();
          back = await apiCall('PUT', '/api/v1/admin/settings', original.settings);
        }
      } catch (e) { back = { status: String(e) }; }
      console.log('settings put back: ' + back.status);
      check(back.status === 200, 'the settings are as they were');
      if (back.status === 200 && !original.settings.invite_required) {
        await logout(); await goto(base + '/app/'); await waitFor('document.querySelector("#login-email")');
        check(!(await evaluate(SHOWN('#login-invite'))), 'with invites off, signing in has no invite field');
      }
    }
  }
  check(consoleErrors.length === 0, 'no console errors or CSP violations: ' + consoleErrors.join(' | ').slice(0, 400));
  console.log(failures ? `FAILED: ${failures}` : 'all passed');
  process.exit(failures ? 1 : 0);
}
run().catch(e => { console.error('ERROR', e); process.exit(1); });
