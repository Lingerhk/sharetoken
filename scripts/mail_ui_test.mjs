#!/usr/bin/env node
// Browser test of email sign-up and the admin console's mail section, against
// real processes: a portal and a mail server on a temporary MySQL database.
// A headless Chrome drives the pages; a plain SMTP dialogue delivers hostile
// mail to the real receiving server. Nothing leaves this machine: delivery goes
// to a closed local port, so outgoing mail stays queued where the test reads it.
//
//   node scripts/mail_ui_test.mjs [workdir]
import { spawn, execFileSync } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const ROOT = resolve(new URL('..', import.meta.url).pathname);
const dir = process.argv[2] ? resolve(process.argv[2]) : mkdtempSync(join(tmpdir(), 'st-mail-ui-'));
mkdirSync(dir, { recursive: true });
const shots = join(dir, 'shots'); mkdirSync(shots, { recursive: true });
const PORTAL = 18193, SMTP = 12525, CDP = 9344;
const base = `http://127.0.0.1:${PORTAL}`;
const procs = [];
process.on('exit', () => procs.forEach(p => p.kill()));

function run(bin, args, name) {
  const p = spawn(bin, args, { cwd: dir, stdio: ['ignore', 'pipe', 'pipe'] });
  let log = ''; p.stdout.on('data', d => { log += d; }); p.stderr.on('data', d => { log += d; });
  p.on('exit', c => { if (c && !p.killed) console.log(`${name} exited ${c}:\n${log}`); });
  procs.push(p); return p;
}

// ---- stack ----
execFileSync('go', ['build', '-o', dir + '/', './cmd/portal', './cmd/mailserver'], { cwd: ROOT, stdio: 'inherit' });
const DSN = execFileSync(join(ROOT, 'scripts/local-mysql.sh'), ['new-db', 'st_mail_ui'], { encoding: 'utf8' }).trim();
process.env.SHARETOKEN_DB_PASSWORD = '';
writeFileSync(join(dir, 'portal.yaml'), `version: 1
database: {dsn: "${DSN}"}
keys: {pass_key_file: pass.key}
http: {listen: "127.0.0.1:${PORTAL}"}
tls: {mode: auto}
accounts: {open_registration: true, signup_credit_micros: 1000000, email_verification: true}
mail: {from: "ShareToken <team@ui.test>", support: "ShareToken Support <support@ui.test>"}
`);
writeFileSync(join(dir, 'mail.yaml'), `version: 1
database: {dsn: "${DSN}"}
hostname: mail.ui.test
domain: ui.test
dkim: {selector: t1, key_file: dkim.key}
delivery: {mode: smarthost, smarthost: {addr: "127.0.0.1:1"}}
smtp: {enabled: true, listen: "127.0.0.1:${SMTP}", mailboxes: ["support@ui.test"]}
`);
const PASSWORD = 'adminadmin';
execFileSync('./portal', ['-config', 'portal.yaml', 'useradd', '-admin', 'root'], { cwd: dir, env: { ...process.env, SHARETOKEN_PASSWORD: PASSWORD }, stdio: 'ignore' });
execFileSync('./mailserver', ['-config', 'mail.yaml', 'dkim-keygen'], { cwd: dir, stdio: 'ignore' });
run('./portal', ['-config', 'portal.yaml', 'serve'], 'portal');
run('./mailserver', ['-config', 'mail.yaml', 'serve'], 'mailserver');
for (let i = 0; i < 100; i++) { try { if ((await fetch(base + '/healthz')).ok) break; } catch { /* starting */ } await sleep(100); }
const sql = q => execFileSync('docker', ['exec', 'st-test-mysql', 'mysql', '-h127.0.0.1', '-uroot', '-N', '--default-character-set=utf8mb4', 'st_mail_ui', '-e', q], { encoding: 'utf8' }).trim();

// ---- SMTP ----
function smtp(from, to, message) {
  return new Promise((res, rej) => {
    const s = net.connect(SMTP, '127.0.0.1');
    s.setTimeout(30000, () => rej(new Error('SMTP dialogue stalled after ' + i + ' steps, replies ' + replies.join(','))));
    const steps = [`EHLO hostile.example`, `MAIL FROM:<${from}>`, `RCPT TO:<${to}>`, 'DATA', message.replace(/\r?\n/g, '\r\n') + '\r\n.', 'QUIT'];
    let buf = '', i = 0; const replies = [];
    s.on('data', d => {
      buf += d;
      for (;;) {
        const m = buf.match(/^(\d{3}) .*\r\n/m);
        if (!m) break;
        replies.push(m[1]); buf = buf.slice(m.index + m[0].length);
        if (i < steps.length) s.write(steps[i++] + '\r\n');
      }
    });
    s.on('close', () => res(replies)); s.on('error', rej);
  });
}

// ---- Chrome ----
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const cp = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${CDP}`, `--user-data-dir=${dir}/profile`, '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
procs.push(cp);
let ws, id = 0; const pending = new Map(); const problems = [];
for (let i = 0; i < 80 && !ws; i++) {
  try { const page = (await (await fetch(`http://127.0.0.1:${CDP}/json`)).json()).find(t => t.type === 'page'); if (page) { ws = new WebSocket(page.webSocketDebuggerUrl); break; } } catch { /* starting */ }
  await sleep(200);
}
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
ws.onmessage = m => {
  const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { const { res, rej } = pending.get(d.id); pending.delete(d.id); d.error ? rej(new Error(d.error.message)) : res(d.result); }
  else if (d.method === 'Runtime.exceptionThrown') problems.push('exception: ' + d.params.exceptionDetails.text);
  else if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error' && !/status of 4\d\d/.test(d.params.entry.text)) problems.push(d.params.entry.text);
};
const cdp = (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable');
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: "try { localStorage.setItem('st_lang', 'zh-CN'); } catch (e) {}" }); // these checks read the Chinese source text
const ev = async expr => {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error in ' + expr + ': ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
};
const q = s => `document.querySelector(${JSON.stringify(s)})`;
const waitFor = async (expr, ms = 8000) => { const t = Date.now(); while (Date.now() - t < ms) { if (await ev(`!!(${expr})`)) return; await sleep(80); } throw new Error('timeout: ' + expr); };
const goto = async u => { await cdp('Page.navigate', { url: u }); await sleep(500); };
const fill = (s, v) => ev(`(() => { const e = ${q(s)}; e.focus(); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', {bubbles:true})); })()`);
const click = async s => { await waitFor(q(s)); await ev(`${q(s)}.click()`); await sleep(350); };
const clickText = async (tag, t) => { await ev(`[...document.querySelectorAll(${JSON.stringify(tag)})].find(e => e.textContent.trim() === ${JSON.stringify(t)}).click()`); await sleep(400); };
const body = () => ev('document.body.innerText');
const shot = async n => writeFileSync(join(shots, n + '.png'), Buffer.from((await cdp('Page.captureScreenshot', { format: 'png' })).data, 'base64'));
let failures = 0;
const ok = (c, what) => { console.log((c ? '  ok   ' : '  FAIL ') + what); if (!c) failures++; };

function totp(secret) {
  const A = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = '';
  for (const ch of secret.replace(/\s|=/g, '').toUpperCase()) bits += A.indexOf(ch).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g).map(b => parseInt(b, 2)));
  const c = Buffer.alloc(8); c.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const h = createHmac('sha1', key).update(c).digest(); const o = h[19] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, '0');
}

// ---- sign-up ----
console.log('email sign-up');
await goto(base + '/app/');
await clickText('button', '注册');
await waitFor(q('#send-code'));
ok(await ev(`!${q('.code-field')}.hidden`), 'the code field appears for registration');
await fill('#login-user', 'ui@example.com'); await fill('#login-pass', 'correct horse');
await ev(`document.querySelector('form').requestSubmit()`); await sleep(300);
ok((await body()).includes('请填写邮箱收到的验证码'), 'sign-up without a code is stopped in the page');
await fill('#login-code', '000000');
await ev(`document.querySelector('form').requestSubmit()`); await sleep(400);
ok((await body()).includes('验证码错误或已过期'), 'a wrong code is explained');
await click('#send-code');
ok((await ev(`${q('#send-code')}.disabled`)) && (await ev(`${q('#send-code')}.textContent`)).includes('秒后可重发'), 'the send button counts down');
await shot('01-register');
const code = sql(`SELECT subject FROM mail_outbox WHERE to_addr='ui@example.com' ORDER BY id DESC LIMIT 1`).split('：')[1];
ok(/^\d{6}$/.test(code), 'a code was mailed');
await fill('#login-code', code);
await ev(`document.querySelector('form').requestSubmit()`);
await waitFor(`document.body.innerText.includes('ui@example.com')`);
ok(true, 'the right code signs in');
await goto(base + '/app/#/account'); await waitFor(q('#support-email'));
ok((await ev(`${q('#support-email')}.innerText`)).includes('support@ui.test'), 'users are told where to write');
await clickText('button', '退出'); await sleep(500);

// ---- hostile mail through real SMTP ----
console.log('receiving');
const XSS = `<img src=x onerror="window.__pwned=1"><script>window.__pwned=1</script>`;
const evil = `From: =?UTF-8?B?${Buffer.from('</b><img src=x onerror=window.__pwned=1>').toString('base64')}?= <attacker@evil.example>
Reply-To: attacker@evil.example
To: support@ui.test
Subject: =?UTF-8?B?${Buffer.from('反馈 <script>window.__pwned=1</script>').toString('base64')}?=
Message-ID: <evil-1@evil.example>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary=B

--B
Content-Type: text/html; charset=utf-8

<p>你好，我要反馈一个问题</p>${XSS}
--B
Content-Type: text/html
Content-Disposition: attachment; filename="report<img src=x onerror=window.__pwned=1>.html"

<script>window.__pwned=1</script>
--B--`;
let replies = await smtp('attacker@evil.example', 'support@ui.test', evil);
ok(replies.includes('354') && replies.filter(r => r === '250').length >= 3, 'the server takes mail for support@');
replies = await smtp('attacker@evil.example', 'root@ui.test', 'Subject: x\n\nx');
ok(replies.includes('550') && !replies.includes('354'), 'and refuses other addresses');
replies = await smtp('', 'support@ui.test', `From: MAILER-DAEMON@elsewhere.example\nSubject: Undelivered Mail Returned to Sender\n\nbounce`);
ok(replies.includes('354'), 'bounces arrive with a null sender');

// ---- admin console ----
console.log('admin console');
await goto(base + '/admin/');
await waitFor(q('#login-user'));
await fill('#login-user', 'root'); await fill('#login-pass', PASSWORD);
await ev(`document.querySelector('form').requestSubmit()`);
await waitFor(q('#mfa-secret'));
const secret = await ev(`${q('#mfa-secret')}.textContent`);
await fill('#mfa-code', totp(secret));
await ev(`document.querySelector('form').requestSubmit()`);
await waitFor(q('.sidenav'));
await waitFor(`${q('#mail-unread')}.textContent === '1'`);
ok(true, 'the menu shows one unread message (the bounce is filed apart)');

await goto(base + '/admin/#/mail/inbox'); await waitFor(`document.querySelector('main table')`);
let t = await body();
ok(t.includes('反馈 <script>') && t.includes('未验证'), 'the list shows the subject as text, marked unverified');
ok(!t.includes('Undelivered'), 'bounces stay out of the inbox');
await shot('02-inbox');
await click('main table a');
await waitFor(q('#mail-body'));
t = await ev(`${q('#mail-body')}.innerText`);
ok(t.includes('我要反馈一个问题'), 'the text of an HTML-only message is shown');
ok(!t.includes('onerror') && !t.includes('script'), 'and none of its markup');
ok(await ev(`!!${q('#mail-unverified')}`), 'unverified senders get a warning');
ok(await ev(`document.querySelectorAll('main img, main script').length === 0`), 'no element from the message reached the page');
ok(await ev(`window.__pwned === undefined`), 'nothing from the message ran');
ok((await body()).includes('report<img'), 'a hostile attachment name is plain text');
await shot('03-message');

await click('#mail-reply');
await waitFor(q('#reply-text'));
ok((await ev(`${q('#reply-text')}.value`)).includes('> 你好'), 'the reply quotes the message');
await fill('#reply-text', '谢谢反馈，我们会处理。');
await clickText('.modal button', '发送');
await waitFor(`location.hash.startsWith('#/mail/outbox/')`);
await waitFor(q('#out-retry'));
t = await body();
ok(t.includes('attacker@evil.example') && t.includes('谢谢反馈') && t.includes('support@ui.test'), 'the reply is queued, from the support address');
await sleep(2500);
await goto(base + '/admin/#/mail/outbox'); await waitFor(`document.querySelector('main table')`);
t = await body();
ok(!t.includes(code), 'the outbox does not show verification codes');
ok(t.includes('含验证码'), 'it says why');
await shot('04-outbox');

await click('#mail-compose');
await waitFor(q('#mail-to'));
await fill('#mail-to', 'user@example.com'); await fill('#mail-subject', '维护通知'); await fill('#mail-text', '今晚维护。');
await clickText('.modal button', '发送');
await waitFor(`location.hash.startsWith('#/mail/outbox/')`); await waitFor(q('#out-cancel'));
await sleep(300);
const canCancel = await ev(`!${q('#out-cancel')}.disabled`);
if (canCancel) { await click('#out-cancel'); await waitFor(`document.body.innerText.includes('已取消')`); await click('#out-retry'); await waitFor(`document.body.innerText.includes('排队中') || document.body.innerText.includes('发送中')`); }
ok(canCancel, 'mail can be cancelled and queued again');

await goto(base + '/admin/#/mail/bounces'); await waitFor(`document.querySelector('main table')`);
ok((await body()).includes('Undelivered'), 'bounces have their own list');
await goto(base + '/admin/#/mail/status'); await waitFor(`document.body.innerText.includes('邮件服务')`);
await waitFor(`document.body.innerText.includes('运行中')`, 40000);
t = await body();
ok(t.includes('运行中') && t.includes('t1') && t.includes('smarthost'), 'the status page shows the running server');
ok(t.includes('DNS 检查'), 'and its DNS checks');
await shot('05-status');

await goto(base + '/admin/#/mail/inbox'); await waitFor(`document.querySelector('main table')`);
await click('main table a'); await waitFor(q('#mail-archive'));
await click('#mail-archive'); await waitFor(`location.hash === '#/mail/inbox'`); await sleep(400);
ok((await body()).includes('没有邮件'), 'an archived message leaves the inbox');
await goto(base + '/admin/#/mail/archived'); await waitFor(`document.querySelector('main table')`);
await click('main table a'); await waitFor(q('#mail-archive'));
await clickText('button', '删除'); await waitFor(q('.modal'));
await clickText('.modal button', '删除'); await sleep(600);
await goto(base + '/admin/#/mail/archived'); await sleep(500);
ok((await body()).includes('没有邮件'), 'and can be deleted');
ok(problems.length === 0, 'no page errors or CSP violations' + (problems.length ? ': ' + problems.join(' | ') : ''));

console.log(failures ? `\n${failures} FAILED (see ${shots})` : `\nall passed (screenshots in ${shots})`);
process.exit(failures ? 1 : 0);
