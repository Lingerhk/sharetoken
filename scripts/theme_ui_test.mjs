#!/usr/bin/env node
// Browser test of the portal's frame in headless Chrome: light and dark mode, the
// sidebar, the language menu, the icons, and every page at desktop and phone width.
//
//   scripts/gui_stack.sh start DIR && . DIR/env
//   STACK=DIR node scripts/theme_ui_test.mjs $PORTAL [shots-dir]
//
// Signs up a throwaway user and creates a throwaway admin in the stack, reading
// their sign-in codes from its outbox; it needs open registration. Never point
// it at a shared portal.
import { execFileSync, spawn } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const portal = process.argv[2];
const STACK = process.env.STACK;
if (!portal || !STACK) { console.error('usage: STACK=<gui_stack.sh directory> theme_ui_test.mjs <portal url> [shots-dir]'); process.exit(2); }
const ROOT = new URL('..', import.meta.url).pathname;
const quiet = (cmd, ...args) => execFileSync(cmd, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
const RUN = Date.now().toString(36);
const ADMIN = `theme-admin-${RUN}@example.test`;
quiet(STACK + '/bin/portal', '-db', readFileSync(STACK + '/dsn', 'utf8').trim(), 'useradd', '-admin', ADMIN);
const shots = process.argv[3] || mkdtempSync(join(tmpdir(), 'st-theme-'));
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9347;
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--ignore-certificate-errors',
  `--remote-debugging-port=${port}`, `--user-data-dir=${mkdtempSync(join(tmpdir(), 'st-theme-profile-'))}`, '--window-size=1440,900', 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => proc.kill());

let ws, id = 0;
const pending = new Map(), problems = [];
for (let i = 0; i < 50 && !ws; i++) {
  try {
    const page = (await (await fetch(`http://127.0.0.1:${port}/json`)).json()).find(t => t.type === 'page');
    if (page) ws = new WebSocket(page.webSocketDebuggerUrl);
  } catch (e) { await sleep(200); }
}
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
ws.onmessage = m => {
  const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { const { res, rej } = pending.get(d.id); pending.delete(d.id); d.error ? rej(new Error(d.error.message)) : res(d.result); }
  else if (d.method === 'Runtime.exceptionThrown') problems.push(d.params.exceptionDetails.exception?.description || d.params.exceptionDetails.text);
  else if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error' && !/status of 40[13]/.test(d.params.entry.text)) problems.push(d.params.entry.text);
};
const cdp = (method, params = {}) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
async function ev(expr) {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}
async function waitFor(expr, ms = 8000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { if (await ev(`!!(${expr})`)) return; await sleep(80); }
  await shot('timeout');
  throw new Error('timeout waiting for ' + expr + '\npage: ' + (await ev('document.body.innerText')).slice(0, 300) + '\nconsole: ' + problems.join('\n'));
}
const go = async url => { await cdp('Page.navigate', { url }); await sleep(250); await waitFor(`document.readyState === 'complete'`); };
const size = (width, height, mobile = false) => cdp('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile });
const scheme = v => cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: v }] });
const shot = async name => { const r = await cdp('Page.captureScreenshot', { format: 'png' }); writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64')); };
const theme = () => ev('document.documentElement.dataset.theme');
const bg = sel => ev(`getComputedStyle(document.querySelector(${JSON.stringify(sel)})).backgroundColor`);
let failures = 0;
function ok(cond, what) { if (cond) console.log('  ok   ' + what); else { failures++; console.log('  FAIL ' + what); } }
const overflow = () => ev(`document.documentElement.scrollWidth - document.documentElement.clientWidth`);
function totp(secret) {
  const A = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'; let bits = ''; const bytes = [];
  for (const c of secret.replace(/[\s=]/g, '').toUpperCase()) bits += A.indexOf(c).toString(2).padStart(5, '0');
  for (let i = 0; i + 8 <= bits.length; i += 8) bytes.push(parseInt(bits.slice(i, i + 8), 2));
  const msg = Buffer.alloc(8); msg.writeUInt32BE(Math.floor(Date.now() / 30000), 4);
  const mac = createHmac('sha1', Buffer.from(bytes)).update(msg).digest(), off = mac[19] & 15;
  return String((mac.readUInt32BE(off) & 0x7fffffff) % 1e6).padStart(6, '0');
}
const fill = (sel, v) => ev(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); el.value = ${JSON.stringify(v)}; el.dispatchEvent(new Event('input', {bubbles: true})); })()`);
// signIn signs in with a code mailed to the address, read from the stack's outbox.
async function signIn(email) {
  await fill('#login-email', email);
  await ev(`document.getElementById('send-code').click()`);
  await fill('#login-code', quiet(ROOT + 'scripts/gui_stack.sh', 'code', STACK, email));
  await ev(`document.querySelector('.login form').requestSubmit()`);
}

await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable');
await size(1440, 900);

// ---- sign-in, in the system's theme ----
console.log('sign-in page');
await scheme('dark');
await go(`${portal}/app/?lang=zh-CN#register`);
await waitFor(`document.querySelector('.login')`);
ok(await theme() === 'dark', 'with no saved choice the page follows a dark system');
await shot('login-dark');
await scheme('light');
await sleep(100);
ok(await theme() === 'light', 'and changes with the system while no choice is saved');
await shot('login-light');
ok(await ev(`!!document.querySelector('.auth-tools #lang-select') && !!document.querySelector('.auth-tools #theme-toggle')`), 'language and theme controls sit in the corner');
ok(await ev(`!!document.querySelector('.auth .input-icon svg')`), 'the fields carry icons');

const name = `theme-${RUN}@example.test`;
await signIn(name);
await waitFor(`document.querySelector('.sidenav a.active') && document.querySelector('main h1')`);

// ---- the frame ----
console.log('frame');
ok(await ev(`[...document.querySelectorAll('.sidenav a')].every(a => a.querySelector('svg') && a.textContent.trim())`), 'every module has an icon and a name');
ok(await ev(`document.querySelectorAll('.sidenav a').length`) === 9, 'the user portal lists its nine modules');
ok(await ev(`!!document.querySelector('main h1 .chip svg')`), 'the page title carries the icon of its module');
ok((await ev(`document.querySelector('.crumb').textContent`)).includes('总览'), 'the top bar names the current module');
ok(await ev(`document.querySelectorAll('.tile .chip svg').length`) === await ev(`document.querySelectorAll('.tile').length`), 'every tile has an icon');
ok((await ev(`document.getElementById('top-balance').textContent`)).includes('credit'), 'the top bar shows the balance');
ok(await ev(`document.querySelector('.drop').hidden`), 'the user menu starts closed');
await ev(`document.getElementById('user-menu').click()`);
ok(await ev(`!document.querySelector('.drop').hidden && document.querySelector('.drop').textContent.includes('退出')`), 'the user menu opens with sign-out');
await ev(`document.body.click()`);
await waitFor(`document.querySelector('.drop').hidden`);
ok(true, 'and closes on a click elsewhere');

// ---- light and dark ----
console.log('theme');
const lightBg = await bg('body');
await shot('home-light');
await ev(`document.getElementById('theme-toggle').click()`);
ok(await theme() === 'dark', 'the toggle switches to dark');
ok(await bg('body') !== lightBg, 'the colours follow');
ok(await ev(`getComputedStyle(document.documentElement).colorScheme`) === 'dark', 'native controls follow too');
await shot('home-dark');
await scheme('light');
await go(`${portal}/app/#/jobs`);
await waitFor(`document.querySelector('main h1')`);
ok(await theme() === 'dark', 'the saved choice beats the system setting after a reload');

// ---- the sidebar ----
console.log('sidebar');
const wide = await ev(`document.querySelector('.sidebar').getBoundingClientRect().width`);
await ev(`document.getElementById('side-collapse').click()`);
await sleep(300);
ok(await ev(`document.querySelector('.sidebar').getBoundingClientRect().width`) < wide / 2, 'the sidebar collapses to icons');
await shot('collapsed-dark');
await go(`${portal}/app/#/ledger`);
await waitFor(`document.querySelector('main h1')`);
ok(await ev(`document.querySelector('.shell').classList.contains('collapsed')`), 'and stays collapsed after a reload');
await ev(`document.getElementById('side-collapse').click()`);

// ---- the language menu ----
console.log('language menu');
ok((await ev(`document.getElementById('lang-select').textContent`)).includes('简体中文'), 'the button shows the current language');
ok(await ev(`!!document.querySelector('#lang-select svg')`), 'with an icon');
await ev(`document.getElementById('lang-select').click()`);
ok(await ev(`document.querySelectorAll('.lang-item').length`) === 10, 'the menu lists 10 languages');
ok(await ev(`document.activeElement.getAttribute('lang')`) === 'zh-CN', 'focus starts on the current language');
await shot('lang-dark');
await cdp('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
ok(await ev(`document.querySelector('.lang-menu').hidden`), 'Escape closes it');
await ev(`location.hash = '#/network'`);
await waitFor(`document.querySelector('main h1')?.textContent.includes('网络')`);
await ev(`(() => { document.getElementById('lang-select').click(); document.querySelector('.lang-item[lang="ja"]').click(); })()`);
await waitFor(`document.documentElement.lang === 'ja'`);
await waitFor(`document.querySelector('.sidenav a.active')`);
ok(await ev(`location.hash`) === '#/network', 'choosing a language reloads the page it is on');
ok((await ev(`document.getElementById('theme-toggle').textContent`)).includes('ライトモード'), 'the theme control is translated');
await go(`${portal}/?lang=zh-CN`);
await waitFor(`document.querySelector('#lang-select')`);
await ev(`document.getElementById('lang-select').click()`);
await shot('site-lang');
ok(await ev(`!document.querySelector('.lang-menu').hidden`), 'the landing page has the same menu');

// ---- the landing page ----
console.log('landing page');
// Scrolls through the page so everything has revealed itself, then photographs all of it.
async function wholePage(name) {
  const height = await ev('document.documentElement.scrollHeight');
  for (let y = 0; y < height; y += 600) { await ev(`scrollTo({ top: ${y}, behavior: 'instant' })`); await sleep(120); }
  await ev(`scrollTo({ top: 0, behavior: 'instant' })`); await sleep(900);
  const r = await cdp('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
  writeFileSync(`${shots}/${name}.png`, Buffer.from(r.data, 'base64'));
}
const fillOf = sel => bg(sel);
await ev(`document.getElementById('lang-select').click()`); // closes the menu opened above
ok(await ev(`!!document.getElementById('theme-toggle')`), 'it has the theme control');
ok(await theme() === 'dark', 'it takes the choice made in the portal');
const darkPage = await bg('body'), darkButton = await fillOf('.btn.primary');
await wholePage('site-dark');
await ev(`document.getElementById('theme-toggle').click()`);
ok(await theme() === 'light', 'the control switches it to light');
ok(await bg('body') !== darkPage, 'the page changes colour');
ok(await fillOf('.btn.primary') === darkButton, 'the main button keeps its colour, with dark text on it');
ok(await ev(`document.querySelector('meta[name=theme-color]').content`) === '#fafaf7', "the browser's bar follows");
await sleep(400);
const inked = await ev(`(() => { const c = document.getElementById('mesh'), d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data; let dark = 0, light = 0; for (let i = 0; i < d.length; i += 4) if (d[i + 3] > 40) { const l = d[i] + d[i + 1] + d[i + 2]; if (l < 380) dark++; else if (l > 650) light++; } return { dark, light }; })()`);
ok(inked.dark > inked.light, 'the hero animation redraws in dark ink');
await wholePage('site-light');
// the vendors are tabs over one set of commands
await ev(`document.getElementById('models').scrollIntoView({ behavior: 'instant' })`); await sleep(900);
const vendorCode = () => ev(`document.getElementById('vendor-code').textContent`);
ok(await ev(`document.querySelectorAll('.vendor svg').length`) === 7 && (await vendorCode()).includes('provider login anthropic'), 'seven model sources with logos, the first one open');
await ev(`document.getElementById('vendor-deepseek').click()`);
ok((await vendorCode()).includes('provider add deepseek') && !(await vendorCode()).includes('login'), 'a vendor without account login shows only the API key');
ok(await ev(`document.getElementById('vendor-deepseek').getAttribute('aria-selected')`) === 'true' && await ev(`document.querySelectorAll('.vendor[aria-selected=true]').length`) === 1, 'and is the one selected tab');
await ev(`document.getElementById('vendor-ollama').click()`);
ok((await vendorCode()).includes('share add gemma4:e4b') && !(await vendorCode()).includes('provider'), 'the local source shows the local commands');
await ev(`document.getElementById('vendor-openai').click()`);
await sleep(300); // the border is still changing colour
await shot('site-models-light');
await ev(`document.getElementById('security').scrollIntoView({ behavior: 'instant' })`); await sleep(1500);
await shot('site-security-light');
for (const mode of ['light', 'dark']) {
  await ev(`THEME.current() === ${JSON.stringify(mode)} || THEME.toggle()`);
  await size(390, 780, true);
  await sleep(300);
  ok(await overflow() <= 0, `nothing runs off a phone screen in ${mode} mode`);
  ok(await ev(`(() => { const r = document.getElementById('theme-toggle').getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth; })()`), `the control is on screen on a phone (${mode})`);
  await shot('site-phone-' + mode);
}
await size(1440, 900);
await ev(`THEME.current() === 'light' || THEME.toggle()`);
await go(`${portal}/app/?lang=zh-CN`);
await waitFor(`document.querySelector('.sidenav')`);
ok(await theme() === 'light', 'the choice carries back to the portal');

// ---- the guide ----
console.log('guide');
await go(`${portal}/docs/?lang=zh-CN`);
await waitFor(`document.querySelector('.doc h1')`);
ok(await theme() === 'light', 'it takes the choice made elsewhere');
const lightDoc = await bg('body');
await wholePage('docs-light');
await ev(`document.getElementById('theme-toggle').click()`);
ok(await theme() === 'dark', 'the control switches it to dark');
ok(await bg('body') !== lightDoc, 'the page changes colour');
await wholePage('docs-dark');
await go(`${portal}/docs/quickstart`);
await waitFor(`document.querySelector('.doc h1')`);
ok(await theme() === 'dark', 'the choice survives a reload');
await ev(`document.getElementById('theme-toggle').click()`);
await sleep(200);
await shot('docs-page-light');
await size(390, 780, true);
await sleep(300);
ok(await overflow() <= 0, 'nothing runs off a phone screen');
ok(await ev(`(() => { const r = document.getElementById('theme-toggle').getBoundingClientRect(); return r.width > 0 && r.right <= innerWidth; })()`), 'the control is on screen on a phone');
await shot('docs-phone-light');
await size(1440, 900);
await go(`${portal}/app/?lang=zh-CN`);
await waitFor(`document.querySelector('.sidenav')`);
ok(await theme() === 'light', 'the choice carries back to the portal');

// ---- every user page, both themes, phone and desktop ----
const USER = ['home', 'ledger', 'jobs', 'tokens', 'devices', 'pairing', 'network', 'account', 'feedback'];
async function pages(path, routes, label) {
  for (const [w, h, phone] of [[1440, 900, false], [390, 780, true]]) {
    await size(w, h, phone);
    for (const mode of ['light', 'dark']) {
      await ev(`THEME.current() === ${JSON.stringify(mode)} || THEME.toggle()`);
      for (const r of routes) {
        await ev(`location.hash = '#/${r}'`);
        await waitFor(`document.querySelector('.sidenav a.active')?.getAttribute('href') === '#/${r}' && document.querySelector('main h1')`);
        await sleep(120);
        const off = await overflow();
        if (off > 0) ok(false, `${label} ${r} at ${w}px runs ${off}px off the screen`);
        await shot(`${label}-${r}-${mode}-${w}`);
      }
    }
  }
  ok(true, `${label}: ${routes.length} pages drawn in both themes at both widths`);
}
console.log('pages');
await go(`${portal}/app/?lang=zh-CN`);
await waitFor(`document.querySelector('.sidenav')`);
await pages('/app/', USER, 'user');

// ---- the drawer ----
console.log('phone');
await size(390, 780, true);
await ev(`THEME.current() === 'light' || THEME.toggle()`);
await ev(`location.hash = '#/home'`);
await waitFor(`document.querySelector('.tile')`);
ok(await ev(`getComputedStyle(document.querySelector('.sidebar')).visibility`) === 'hidden', 'the sidebar is out of the way on a phone');
await ev(`document.getElementById('menu-btn').click()`);
await sleep(350);
ok(await ev(`getComputedStyle(document.querySelector('.sidebar')).visibility`) === 'visible', 'the menu button opens it');
await shot('drawer-light');
await ev(`document.querySelector('.sidenav a[href="#/jobs"]').click()`);
await sleep(350);
ok(await ev(`!document.querySelector('.shell').classList.contains('nav-open')`), 'choosing a module closes it');

await size(1440, 900);
await ev(`localStorage.setItem('st_lang', 'zh-CN')`);

// ---- the admin console ----
{
  console.log('admin console');
  await ev(`ST.logout()`);
  await sleep(500);
  await go(`${portal}/admin/`);
  await waitFor(`document.getElementById('login-email')`);
  ok(!(await ev(`!!document.getElementById('lang-select')`)), 'the sign-in has no language menu');
  ok(await ev(`!!document.getElementById('theme-toggle')`), 'but has the theme control');
  await signIn(ADMIN);
  await waitFor(`document.getElementById('mfa-code')`);
  const secret = await ev(`document.getElementById('mfa-secret')?.textContent || ''`);
  await shot('admin-mfa');
  await fill('#mfa-code', totp(secret));
  await ev(`document.querySelector('.login form').requestSubmit()`);
  await waitFor(`document.querySelector('.sidenav a.active') && document.querySelector('main h1')`);
  ok(await ev(`document.querySelectorAll('.sidenav a svg').length`) === 15, 'all fifteen admin modules have icons');
  await pages('/admin/', ['overview', 'users', 'relays', 'versions', 'jobs', 'nodes', 'audits', 'disputes', 'pairings', 'ledger', 'mail', 'feedback'], 'admin');
  await size(1440, 900);
  await ev(`location.hash = '#/users/' + encodeURIComponent(${JSON.stringify(name)})`);
  await waitFor(`document.querySelector('main h1 .chip') && document.querySelector('main .tile')`);
  await shot('admin-user-detail');
  ok(true, 'the user detail page draws');
}

ok(problems.length === 0, 'no console errors or exceptions' + (problems.length ? ':\n' + problems.join('\n') : ''));
console.log(failures ? `\n${failures} FAILED; shots in ${shots}` : `\nall passed; shots in ${shots}`);
proc.kill();
process.exit(failures ? 1 : 0);
