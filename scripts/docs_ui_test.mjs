#!/usr/bin/env node
// Browser test of the download section and the user guide (/docs/), in a headless
// Chrome with real mouse and keyboard input: a click lands wherever the coordinates
// are, so a covered or off-screen control fails the test.
//
//   GUI_STACK_OFFSET=300 scripts/gui_stack.sh start DIR && . DIR/env
//   NODE_TLS_REJECT_UNAUTHORIZED=0 node scripts/docs_ui_test.mjs "$PORTAL" DIR/downloads [shots-dir]
//
// With a downloads directory the test lays out package sets of its own. Without
// one it only reads, so it can run against a live portal:
//   node scripts/docs_ui_test.mjs https://dev.sharetoken.app "" [shots-dir]
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';

const base = process.argv[2].replace(/\/$/, '');
const downloadsDir = process.argv[3] || '';
const shots = process.argv[4] || mkdtempSync(join(tmpdir(), 'st-docs-'));
mkdirSync(shots, { recursive: true });
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const port = 9000 + (process.pid % 700);
const profile = mkdtempSync(join(tmpdir(), 'st-docs-chrome-'));
const proc = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--ignore-certificate-errors',
  `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, '--window-size=1320,900', 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => { proc.kill(); try { rmSync(profile, { recursive: true, force: true }); } catch (e) { /* still closing */ } });

let ws, seq = 0;
const pending = new Map();
let problems = [];
let lastDocStatus = 0;
for (let i = 0; i < 80 && !ws; i++) {
  try {
    const page = (await (await fetch(`http://127.0.0.1:${port}/json`)).json()).find(t => t.type === 'page');
    if (page) ws = new WebSocket(page.webSocketDebuggerUrl);
  } catch (e) { /* not up yet */ }
  if (!ws) await sleep(150);
}
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
ws.onmessage = m => {
  const d = JSON.parse(m.data);
  if (d.id && pending.has(d.id)) { const { res, rej } = pending.get(d.id); pending.delete(d.id); d.error ? rej(new Error(d.error.message)) : res(d.result); return; }
  if (d.method === 'Runtime.exceptionThrown') problems.push('exception: ' + JSON.stringify(d.params.exceptionDetails).slice(0, 400));
  else if (d.method === 'Log.entryAdded' && d.params.entry.level === 'error' && d.params.entry.source !== 'network') problems.push('console: ' + d.params.entry.text);
  else if (d.method === 'Network.responseReceived' && d.params.type === 'Document') lastDocStatus = d.params.response.status;
};
const cdp = (method, params = {}) => new Promise((res, rej) => { const i = ++seq; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params })); });
async function ev(expr) {
  const r = await cdp('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page error in ' + expr + ': ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
}
const q = s => JSON.stringify(s);
const el = s => `document.querySelector(${q(s)})`;
async function waitFor(expr, ms = 10000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) { if (await ev(`!!(${expr})`)) return; await sleep(40); }
  throw new Error('timeout waiting for ' + expr + ' at ' + await ev('location.href') + '\n--- page text:\n' + (await ev('document.body.innerText')).slice(0, 1500));
}
let passed = 0;
function ok(cond, what) { if (!cond) throw new Error('FAILED: ' + what); passed++; console.log('  ok  ' + what); }
const section = name => console.log('\n' + name);
async function shot(name) { const r = await cdp('Page.captureScreenshot', { format: 'png' }); writeFileSync(join(shots, name + '.png'), Buffer.from(r.data, 'base64')); }

const DOC_READY = `document.querySelector('#doc h1') && document.querySelector('#sidebar a')`;
async function go(path, ready = DOC_READY) {
  await cdp('Page.navigate', { url: path.startsWith('http') ? path : base + path });
  await sleep(120);
  await waitFor(`document.readyState === 'complete' && !document.documentElement.classList.contains('i18n-pending')`);
  await ev(`document.documentElement.style.scrollBehavior = 'auto'`); // the site scrolls smoothly: a click needs its target to stay put
  if (ready) await waitFor(ready);
}
// The centre of an element that is on screen, after scrolling it there.
async function point(selector) {
  await ev(`${el(selector)}.scrollIntoView({ block: 'center', inline: 'nearest' })`);
  await sleep(60);
  const box = await ev(`(() => { const r = ${el(selector)}.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height }; })()`);
  if (!box.w || !box.h) throw new Error('not visible: ' + selector);
  return box;
}
async function mouseAt(x, y) {
  await cdp('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
  await cdp('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
  await cdp('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
}
async function click(selector) { const p = await point(selector); await mouseAt(p.x, p.y); await sleep(80); }
const KEYS = { Enter: 13, Escape: 27, ArrowDown: 40, ArrowUp: 38, ArrowLeft: 37, ArrowRight: 39, Tab: 9, '/': 191 };
async function key(k) {
  const common = { key: k, code: k === '/' ? 'Slash' : k, windowsVirtualKeyCode: KEYS[k], nativeVirtualKeyCode: KEYS[k] };
  await cdp('Input.dispatchKeyEvent', { type: 'keyDown', ...common, ...(k === 'Enter' ? { text: '\r' } : k === '/' ? { text: '/' } : {}) });
  await cdp('Input.dispatchKeyEvent', { type: 'keyUp', ...common });
  await sleep(50);
}
async function type(text) { await cdp('Input.insertText', { text }); await sleep(200); }
const text = s => ev(`${el(s)}?.textContent ?? ''`);
const count = s => ev(`document.querySelectorAll(${q(s)}).length`);
const visible = s => ev(`(() => { const e = ${el(s)}; return !!e && !e.closest('[hidden]') && e.getBoundingClientRect().width > 0; })()`);
const overflow = () => ev(`document.documentElement.scrollWidth - document.documentElement.clientWidth`);
function assertClean(where) { if (problems.length) { const p = problems; problems = []; throw new Error(`page problems at ${where}:\n` + p.join('\n')); } }

await cdp('Page.enable'); await cdp('Runtime.enable'); await cdp('Log.enable'); await cdp('Network.enable');
await cdp('Browser.grantPermissions', { permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'], origin: base }).catch(() => {});
let lang = 'zh-CN';
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: `try { if (!localStorage.getItem('st_lang')) localStorage.setItem('st_lang', ${q(lang)}); } catch (e) {}` });
const setLang = async l => { await ev(`localStorage.setItem('st_lang', ${q(l)})`); };

// ---- package sets ----
const SET_ARCH = ['amd64', 'arm64'], SET_OS = { macos: 'tar.gz', linux: 'tar.gz', windows: 'zip' };
function layout(version, withDesktop) {
  for (const f of readdirSync(downloadsDir)) rmSync(join(downloadsDir, f), { recursive: true });
  let sums = '';
  const put = name => {
    const body = `archive ${name}\n`.repeat(40000);
    writeFileSync(join(downloadsDir, name), body);
    sums += `${createHash('sha256').update(body).digest('hex')}  ${name}\n`;
  };
  for (const [os, ext] of Object.entries(SET_OS)) for (const a of SET_ARCH) put(`sharetoken-cli-${version}-${os}-${a}.${ext}`);
  // Dev packages are for testing: served by name, never listed.
  for (const a of SET_ARCH) put(`sharetoken-cli-dev-${version}-linux-${a}.tar.gz`);
  if (withDesktop) put(`sharetoken-${version}-macos-arm64.dmg`);
  if (withDesktop === 'all') { put(`sharetoken-${version}-windows-amd64.exe`); put(`sharetoken-${version}-linux-amd64.deb`); }
  writeFileSync(join(downloadsDir, 'SHA256SUMS-' + version), sums);
}
function emptyDownloads() { for (const f of readdirSync(downloadsDir)) rmSync(join(downloadsDir, f), { recursive: true }); }

const manifest = await (await fetch(base + '/api/v1/downloads')).json().catch(() => ({ packages: [] }));
const live = !downloadsDir;
const VERSION = live ? manifest.version : '9.9.9';
if (!live) layout(VERSION, false);
const EXE = 'sharetoken';
const PORT = '9787';

// ---- the download section ----
section('download section');
const FRONT = `document.querySelector('#tab-cli') && document.querySelector('#panel-cli .platform.real')`;
await go('/', FRONT);
const hasDesktop = manifest.packages.some(p => p.kind === 'desktop'); // a live portal has them; the test's own package set starts without
if (hasDesktop) {
  ok(await ev(`${el('#tab-desktop')}.getAttribute('aria-selected') === 'true'`) && await count('#panel-desktop .arch-link') === 6, 'opens on the desktop installers');
  ok(await ev(`[...document.querySelectorAll('#panel-desktop .arch-link')].every(a => /\\.(dmg|exe|deb)$/.test(a.getAttribute('download')))`), 'which are real installers');
  await click('#tab-cli');
} else {
  ok(await ev(`${el('#tab-cli')}.getAttribute('aria-selected') === 'true'`), 'opens on the command line while no desktop package exists');
}
ok(await visible('#panel-cli') && !(await visible('#panel-desktop .platform')), 'only the command-line panel shows');
ok(await count('#panel-cli .platform.real') === 3, 'three systems are offered');
ok(await count('#panel-cli .arch-link') === 6, 'with two architectures each');
const names = await ev(`[...document.querySelectorAll('#panel-cli .platform.real > b')].map(b => b.textContent).join()`);
ok(names === 'macOS,Windows,Linux', 'macOS, Windows and Linux: ' + names);
await shot('download-cli');
await click('#tab-desktop');
ok(await visible('#panel-desktop .platform') && !(await visible('#panel-cli .platform')), 'the desktop tab switches panels');
if (!hasDesktop) {
  await click('#panel-desktop .platform');
  ok((await text('#soon-msg')).includes('macOS'), 'a desktop placeholder says it is coming');
}
await ev(`${el('#tab-desktop')}.focus()`);
await key('ArrowRight');
ok(await ev(`document.activeElement.id === 'tab-cli'`) && await visible('#panel-cli .platform'), 'the arrow keys move between tabs');
const here = await ev(`/Mac/.test(navigator.userAgent) ? 'macos' : /Windows/.test(navigator.userAgent) ? 'windows' : 'linux'`);
ok(await ev(`${el(`.platform.here[data-os=${here}]`)} !== null`), 'the visitor\'s own system is marked');
const links = await ev(`[...document.querySelectorAll('#panel-cli .arch-link')].map(a => ({ href: a.href, file: a.getAttribute('download') }))`);
for (const l of live ? links.slice(0, 1) : links) {
  const r = await ev(`fetch(${q(l.href)}, { headers: { Range: 'bytes=0-15' } }).then(async r => ({ status: r.status, type: r.headers.get('content-type'), cd: r.headers.get('content-disposition') }))`);
  ok(r.status === 206 && r.type === 'application/octet-stream' && r.cd.includes(l.file), 'downloadable: ' + l.file);
}
ok(await ev(`[...document.querySelectorAll('#panel-cli .arch-link .arch-name')].some(e => e.textContent === 'Apple Silicon')`), 'macOS architectures have their everyday names');
const sumsHref = await ev(`${el('#dl-meta a')}.href`);
ok((await ev(`fetch(${q(sumsHref)}).then(r => r.status)`)) === 200 && (await text('#dl-meta')).includes(VERSION), 'the version and checksum file are shown');
await click(`#panel-cli .platform[data-os=linux] .os-guide`);
await waitFor(DOC_READY);
await sleep(200);
ok((await ev('location.pathname')) === '/docs/install' && (await ev(`${el('.os-tabs [aria-checked=true]')}.dataset.os`)) === 'linux', 'a system\'s install guide opens with that system chosen');
assertClean('download section');

if (!live) {
  section('download section without packages, and with a desktop package');
  emptyDownloads();
  await go('/', `document.querySelector('#tab-desktop')`);
  await sleep(300);
  ok(await ev(`${el('#tab-desktop')}.getAttribute('aria-selected') === 'true'`), 'no packages: opens on the desktop tab');
  await click('#tab-cli');
  ok(await count('#panel-cli [data-soon]') === 3 && await ev(`${el('#dl-meta')}.hidden`), 'no packages: the command-line tab shows placeholders, no version');
  await click('#panel-cli .platform');
  ok((await text('#soon-msg')).includes('macOS'), 'no packages: a placeholder says it is coming');
  layout(VERSION, true);
  await go('/', `document.querySelector('#panel-desktop .platform.real')`);
  ok(await ev(`${el('#tab-desktop')}.getAttribute('aria-selected') === 'true'`), 'a desktop package: opens on the desktop tab');
  ok(await count('#panel-desktop .platform.real') === 1 && await count('#panel-desktop .arch-link') === 1, 'only the system that has a package becomes a link');
  layout(VERSION, false);
  assertClean('package sets');
}

// ---- nav from the landing page ----
section('landing page to the guide');
await go('/', FRONT);
ok(await visible('.nav nav a[href="/docs/"]'), 'the navigation has a docs link');
await click('.foot a[href="/docs/"]');
await waitFor(DOC_READY);
ok((await ev('location.pathname')) === '/docs/', 'the footer links to the guide');

// ---- every page ----
const nav = await (await fetch(base + '/static/docs-nav.json')).json();
const pages = nav.flatMap(g => g.pages);
const urlOf = slug => (slug === 'overview' ? '/docs/' : '/docs/' + slug);
const BROKEN = `(() => {
  const t = document.getElementById('doc').innerText;
  const bad = [/\\{\\{/, /^:::/m, /\\{#/, /\\[!(NOTE|TIP|WARNING)\\]/, /\\]\\(\\//, /\\bundefined\\b/, /\\bNaN\\b/].find(r => r.test(t));
  return bad ? String(bad) + ': ' + (t.match(bad) || [''])[0] : '';
})()`;
for (const l of process.env.QUICK ? [] : ['zh-CN', 'en']) {
  section('every page in ' + l);
  await setLang(l);
  for (const p of pages) {
    for (const os of ['macos', 'linux', 'windows']) {
      await go(urlOf(p.slug) + '?os=' + os);
      const h1 = (await ev(`[...${el('#doc h1')}.childNodes].filter(n => !n.classList || !n.classList.contains('anchor')).map(n => n.textContent).join('')`)).trim();
      if (h1 !== p.title[l]) throw new Error(`${p.slug}: h1 ${q(h1)} is not ${q(p.title[l])}`);
      const broken = await ev(BROKEN);
      if (broken) throw new Error(`${l}/${p.slug}/${os}: raw markup on the page: ${broken}`);
      if (await overflow() > 0) throw new Error(`${l}/${p.slug}/${os}: the page scrolls sideways by ${await overflow()}px`);
      const others = ['macos', 'linux', 'windows'].filter(x => x !== os);
      const wrong = await ev(`[...document.querySelectorAll('.os-block')].filter(b => !b.hidden && !b.dataset.os.split(' ').includes(${q(os)})).length`);
      const hiddenWrong = await ev(`[...document.querySelectorAll('.os-block')].filter(b => b.hidden && b.dataset.os.split(' ').includes(${q(os)})).length`);
      if (wrong || hiddenWrong) throw new Error(`${l}/${p.slug}/${os}: blocks of ${others} are showing, or its own are hidden`);
      if (!(await ev(`document.title`)).includes(p.title[l])) throw new Error(`${p.slug}: title is ${await ev('document.title')}`);
      // Every link on the page points at a place that exists.
      const targets = await ev(`[...document.querySelectorAll('#doc a[href], #pager a[href]')].map(a => a.href)`);
      for (const t of new Set(targets)) {
        const u = new URL(t);
        if (u.origin !== new URL(base).origin) continue;
        if (u.pathname.startsWith('/docs/') && u.hash) {
          const slug = u.pathname.slice(6) || 'overview';
          const src = await (await fetch(`${base}/static/docs/${l}/${slug}.md`)).text();
          if (!src.includes('{' + u.hash + '}')) throw new Error(`${p.slug}: link ${t} has no target`);
        }
      }
    }
    assertClean(p.slug);
    passed++;
    console.log(`  ok  ${p.slug}: three systems`);
  }
}

// ---- walking the guide with the mouse, without a full reload ----
section('moving around the guide');
await setLang('zh-CN');
await go('/docs/?os=macos');
await ev(`window.__sameDocument = true`);
await click('#sidebar a[data-page=quickstart]');
await waitFor(`location.pathname === '/docs/quickstart' && document.querySelector('#doc h1').textContent.startsWith('快速开始')`);
ok(await ev(`window.__sameDocument === true`), 'the sidebar changes page without reloading');
ok(await ev(`${el('#sidebar a[data-page=quickstart]')}.getAttribute('aria-current') === 'page'`), 'the sidebar marks the current page');
ok(await ev(`${el('#sidebar a[data-page=overview]')}.getAttribute('aria-current') === null`), 'and unmarks the previous one');
ok((await ev('document.title')).startsWith('快速开始'), 'the window title follows');
ok(await ev(`document.activeElement === ${el('#doc h1')}`), 'focus moves to the new page title');
await ev(`scrollTo(0, 800)`);
await click('#sidebar a[data-page=install]');
await waitFor(`location.pathname === '/docs/install'`);
await sleep(150);
ok(await ev(`scrollY === 0`), 'a new page starts at the top');
await cdp('Page.navigateToHistoryEntry', { entryId: (await cdp('Page.getNavigationHistory')).entries.at(-2).id });
await waitFor(`location.pathname === '/docs/quickstart' && document.querySelector('#doc h1').textContent.startsWith('快速开始')`);
ok(await ev(`window.__sameDocument === true`), 'the back button returns without reloading');
await cdp('Page.navigateToHistoryEntry', { entryId: (await cdp('Page.getNavigationHistory')).entries.at(-1).id });
await waitFor(`location.pathname === '/docs/install'`);
ok(true, 'and forward goes on again');

// prev / next walk through the whole guide
await go('/docs/');
for (let i = 1; i < pages.length; i++) {
  await click('#pager a.next');
  await waitFor(`location.pathname === ${q(urlOf(pages[i].slug))} && document.querySelector('#doc h1').textContent.startsWith(${q(pages[i].title['zh-CN'])})`);
}
ok(await count('#pager a.next') === 0, 'next walks the whole guide, the last page has none');
for (let i = pages.length - 2; i >= 0; i--) {
  await click('#pager a.prev');
  await waitFor(`location.pathname === ${q(urlOf(pages[i].slug))}`);
}
ok(await count('#pager a.prev') === 0, 'previous walks back, the first page has none');
await click('.cards .card[href="/docs/share"]');
await waitFor(`location.pathname === '/docs/share'`);
ok(true, 'cards on the overview open their pages');
assertClean('moving around');

// ---- platforms ----
section('platform switch');
await go('/docs/install?os=macos');
const bar = async () => ev(`[...document.querySelectorAll('#os-tabs button')].map(b => b.textContent + '=' + b.getAttribute('aria-checked')).join()`);
ok((await bar()) === 'macOS=true,Linux=false,Windows=false', 'the address chooses the system');
const shown = () => ev(`document.getElementById('doc').innerText`);
ok((await shown()).includes('shasum -a 256') && !(await shown()).includes('Get-FileHash'), 'macOS shows macOS commands');
await click('#os-tabs [data-os=windows]');
ok((await shown()).includes('Get-FileHash') && !(await shown()).includes('shasum -a 256'), 'Windows swaps them for PowerShell');
ok((await ev('location.search')) === '?os=windows', 'the address records the choice');
await click('#os-tabs [data-os=linux]');
ok((await shown()).includes('sha256sum') && !(await shown()).includes('Get-FileHash'), 'Linux shows its own');
await key('ArrowRight');
ok((await bar()).includes('Windows=true') && await ev(`document.activeElement.dataset.os === 'windows'`), 'arrow keys change the system');
await click('#os-tabs [data-os=linux]');
await go('/docs/quickstart');
ok((await bar()).includes('Linux=true'), 'the choice carries over to the next visit');
await go('/docs/');
ok(await visible('#os-bar') === false, 'a page with nothing system-specific hides the switch');
await go('/docs/install');
// Switching keeps the reader in place, and the bar stays on screen while scrolled.
const y0 = await ev(`document.getElementById('path').scrollIntoView(); document.getElementById('path').getBoundingClientRect().top`);
const tabBox = await ev(`(() => { const r = ${el('#os-tabs [data-os=macos]')}.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
await mouseAt(tabBox.x, tabBox.y); // without scrolling first: the switch must be reachable from here
await sleep(120);
const y1 = await ev(`document.getElementById('path').getBoundingClientRect().top`);
ok(Math.abs(y1 - y0) < 40, `switching keeps the place on the page (${Math.round(y0)} to ${Math.round(y1)})`);
ok(await ev(`(() => { const r = document.getElementById('os-bar').getBoundingClientRect(); return r.top >= 0 && r.top < 80; })()`), 'the switch stays on screen while scrolling');
ok(await ev(`[...document.querySelectorAll('#toc a')].every(a => document.getElementById(a.getAttribute('href').slice(1)))`), 'the contents match the page');
assertClean('platforms');

// system-specific headings in the contents
await go('/docs/troubleshooting?os=macos');
ok(await ev(`!!${el('#toc a[href="#gatekeeper"]')} && !${el('#toc a[href="#smartscreen"]')}`), 'contents list macOS\' heading only on macOS');
await click('#os-tabs [data-os=windows]');
ok(await ev(`!!${el('#toc a[href="#smartscreen"]')} && !${el('#toc a[href="#gatekeeper"]')}`), 'and Windows\' on Windows');

// a deep link into another system's section
await go('/docs/quickstart?os=macos');
await go('/docs/troubleshooting#smartscreen');
await sleep(150);
ok(await visible('#smartscreen') && await ev(`(() => { const r = document.getElementById('smartscreen').getBoundingClientRect(); return r.top >= 0 && r.top < innerHeight; })()`), 'a link to another system\'s section shows and scrolls to it');
assertClean('deep links');

// ---- contents ----
section('contents and anchors');
await go('/docs/config?os=linux');
const before = await ev('scrollY');
await click('#toc a[href="#buy"]');
await sleep(250);
ok((await ev('location.hash')) === '#buy' && await ev('scrollY') > before, 'a contents link scrolls to its section');
ok(await ev(`(() => { const t = document.getElementById('buy').getBoundingClientRect().top; const b = document.getElementById('os-bar').getBoundingClientRect().bottom; return t >= b - 2 && t < innerHeight / 2; })()`), 'the heading is not hidden under the sticky bars');
await sleep(250);
ok(await ev(`${el('#toc a[href="#buy"]')}.classList.contains('on')`), 'the contents highlight the section being read');
ok(await ev(`[...document.querySelectorAll('#doc h2 .anchor')].length > 0`), 'headings have a link to themselves');

// ---- code ----
section('code blocks');
await go('/docs/quickstart?os=linux');
const blockBtn = `#doc .os-block:not([hidden]) .code .copy`;
const code = await ev(`${el('#doc .os-block:not([hidden]) .code code')}.textContent`);
ok(code.includes(`sharetoken-cli-${VERSION}-linux-$ARCH.tar.gz`), 'commands name this portal\'s real package');
ok(code.includes(new URL(base).origin), 'and this portal\'s address');
ok(code.includes(EXE) && !code.includes('{{'), 'and this network\'s program name');
ok((await ev(`document.getElementById('doc').innerText`)).includes(`127.0.0.1:${PORT}/v1`), 'and its local port');
await click(blockBtn);
ok((await text(blockBtn)) === '已复制', 'the copy button confirms');
const pasted = await ev(`navigator.clipboard.readText()`);
ok(pasted === code, 'the clipboard holds exactly the command');
await sleep(1800);
ok((await text(blockBtn)) === '复制', 'and the button goes back');
assertClean('code');

// ---- download table in the guide ----
section('download table');
await go('/docs/install?os=windows');
ok(await count('#doc [data-package]') === 2 && await ev(`[...document.querySelectorAll('#doc [data-package]')].every(r => r.dataset.package.includes('windows') && r.dataset.package.endsWith('.zip'))`), 'Windows lists its two zip packages');
await click('#os-tabs [data-os=macos]');
ok(await count('#doc [data-package]') === 2 && await ev(`[...document.querySelectorAll('#doc [data-package]')].every(r => r.dataset.package.includes('macos'))`), 'the table follows the system');
ok((await text('#doc [data-package]')).includes('SHA-256'), 'each package shows its checksum');
const first = await ev(`${el('#doc [data-package] a')}.href`);
ok((await ev(`fetch(${q(first)}, { headers: { Range: 'bytes=0-1' } }).then(r => r.status)`)) === 206, 'its link downloads');

if (!live) {
  emptyDownloads();
  await go('/docs/install?os=macos');
  ok((await text('#doc [data-downloads]')).includes('暂时没有'), 'no packages: the table says so');
  ok((await ev(`document.getElementById('doc').innerText`)).includes('<version>'), 'and commands show a placeholder version');
  layout(VERSION, 'all');
  await go('/docs/quickstart?os=macos');
  const prod = await ev(`document.getElementById('doc').innerText`);
  ok(prod.includes('sharetoken version') && prod.includes('127.0.0.1:9787') && !prod.includes('sharetoken-dev') && !prod.includes('sharetoken-prod'), 'commands name the released program');
  await go('/docs/config?os=macos');
  ok((await ev(`document.getElementById('doc').innerText`)).includes('~/.sharetoken') && !(await ev(`document.getElementById('doc').innerText`)).includes('sharetoken-dev'), 'and its data directory');
  for (const [o, ext] of [['macos', 'dmg'], ['windows', 'exe'], ['linux', 'deb']]) {
    await go('/docs/desktop?os=' + o);
    ok((await text('#doc [data-downloads="desktop"]')).includes(`sharetoken-${VERSION}-${o}-`) && (await text('#doc [data-downloads="desktop"]')).includes('.' + ext), `the desktop guide offers the ${o} installer`);
  }
  await go('/', `document.querySelector('#panel-desktop .platform.real')`);
  ok(await count('#panel-desktop .platform.real') === 3 && await ev(`!document.getElementById('panel-desktop').hidden`), 'the landing page opens on the desktop installers');
  ok(await ev(`[...document.querySelectorAll('#panel-desktop .os-guide')].every(a => a.getAttribute('href').startsWith('/docs/desktop'))`), 'and links to the desktop guide');
  ok(!(await ev(`fetch('/api/v1/downloads').then(r => r.text())`)).includes('-dev-'), 'dev packages are not listed');
  layout(VERSION, false);
}
assertClean('downloads');

// ---- search ----
section('search');
await go('/docs/quickstart?os=macos');
await key('/');
ok(await ev(`document.activeElement.id === 'search-input'`), 'the / key focuses search');
await type('Unblock-File');
await waitFor(`!document.getElementById('search-results').hidden`);
ok(await count('#search-results .result') > 0, 'results appear while typing');
ok((await text('#search-results .result mark')).toLowerCase() === 'unblock-file', 'the match is highlighted');
ok((await text('#search-results')).includes('Windows'), 'results say which system they belong to');
await shot('search');
await key('ArrowDown');
ok(await count('#search-results .result.on') === 1 && (await ev(`${el('#search-results .result.on')}.id`)) === 'result-1' || await count('#search-results .result') === 1, 'arrow keys move through results');
await key('ArrowUp');
const target = await ev(`${el('#search-results .result.on')}.getAttribute('href')`);
await key('Enter');
await waitFor(`location.pathname === ${q(new URL(target, base).pathname)}`);
await sleep(200);
ok(await ev(`document.getElementById('search-results').hidden`) && (await ev(`document.getElementById('search-input').value`)) === '', 'Enter opens the result and closes search');
ok((await ev(`${el('#os-tabs [aria-checked=true]')}.dataset.os`)) === 'windows', 'a result for another system brings that system along');
const anchor = new URL(target, base).hash.slice(1);
if (anchor) ok(await visible('#' + anchor), 'and shows its section');
await click('#search-input');
await type('zzqqxx-no-such-thing');
await waitFor(`document.querySelector('.result-none')`);
ok((await text('.result-none')).includes('没有'), 'nothing found says so');
await key('Escape');
ok(await ev(`document.getElementById('search-results').hidden`), 'Escape closes search');
await ev(`${el('#search-input')}.value = ''`);
await click('#search-input');
await type('share add');
await waitFor(`document.querySelector('#search-results .result')`);
await click('#search-results .result');
await waitFor(`document.getElementById('search-results').hidden && document.getElementById('doc').innerText.includes('share add')`);
ok(true, 'clicking a result opens it');
await click('#search-input');
await type('relays');
await waitFor(`document.querySelector('#search-results .result')`);
await mouseAt(600, 500);
await sleep(100);
ok(await ev(`document.getElementById('search-results').hidden`), 'clicking elsewhere closes search');
await ev(`${el('#search-input')}.value = ''`);
assertClean('search');

// ---- errors ----
section('errors');
await go('/docs/no-such-page', `document.querySelector('#doc h1')`);
await sleep(150);
ok(lastDocStatus === 404, 'an unknown page answers 404');
ok((await text('#doc h1')).includes('找不到'), 'and says so');
ok(await count('#sidebar a') === pages.length, 'with the navigation still there');
await click('#sidebar a[data-page=account]');
await waitFor(`location.pathname === '/docs/account' && document.querySelector('#doc h1').textContent.startsWith('账户')`);
ok(true, 'and the way out works');
assertClean('404');
if (!live) {
  await go('/docs/');
  await cdp('Network.setBlockedURLs', { urls: ['*/static/docs/*'] });
  await click('#sidebar a[data-page=cli]');
  await waitFor(`document.querySelector('#doc .state button')`);
  ok((await text('#doc .state')).includes('加载失败'), 'a page that fails to load says so');
  await cdp('Network.setBlockedURLs', { urls: [] });
  await click('#doc .state button');
  await waitFor(`document.querySelector('#doc h1') && document.querySelector('#doc h1').textContent.startsWith('命令参考')`);
  ok(true, 'and retry recovers');
  problems = [];
}

// ---- links ----
section('links');
await go('/docs/share?os=macos');
const ext = await ev(`(() => { const a = ${el('#doc .os-block:not([hidden]) a[href^="https://"]')}; return { target: a.target, rel: a.rel }; })()`);
ok(ext.target === '_blank' && ext.rel.includes('noopener'), 'outside links open safely in a new tab');
await go('/docs/account');
ok(await ev(`!!${el('#doc a[href="/app/#/devices"]')}`), 'links reach the portal pages');
await click('.top .logo');
await waitFor(`location.pathname === '/'`);
ok(true, 'the logo goes home');

// ---- phone ----
section('phone');
await cdp('Emulation.setDeviceMetricsOverride', { width: 390, height: 780, deviceScaleFactor: 2, mobile: true });
await cdp('Emulation.setTouchEmulationEnabled', { enabled: false });
await go('/docs/install?os=linux');
await sleep(200);
ok(await overflow() <= 0, 'the page fits a phone');
ok(await ev(`document.getElementById('sidebar').getBoundingClientRect().right <= 0`), 'the sidebar starts closed');
await shot('phone-page');
await click('#menu');
await sleep(280);
ok(await ev(`document.getElementById('sidebar').getBoundingClientRect().left >= 0`) && await ev(`${el('#menu')}.getAttribute('aria-expanded') === 'true'`), 'the menu button opens it');
await shot('phone-menu');
await mouseAt(370, 500);
await sleep(280);
ok(await ev(`document.getElementById('sidebar').getBoundingClientRect().right <= 0`), 'tapping beside it closes it');
await click('#menu');
await sleep(280);
await click('#sidebar a[data-page=running]');
await waitFor(`location.pathname === '/docs/running'`);
await sleep(280);
ok(await ev(`document.getElementById('sidebar').getBoundingClientRect().right <= 0`), 'choosing a page closes it');
for (const p of pages) {
  await go(urlOf(p.slug) + '?os=windows');
  if (await overflow() > 0) throw new Error(`phone: ${p.slug} scrolls sideways by ${await overflow()}px`);
}
ok(true, 'no page scrolls sideways on a phone');
await go('/docs/quickstart?os=windows');
await click('#os-tabs [data-os=linux]');
ok((await bar()).includes('Linux=true'), 'the system switch works on a phone');
await click('#search-input');
await type('relays');
await waitFor(`document.querySelector('#search-results .result')`);
ok(await ev(`(() => { const r = document.getElementById('search-results').getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; })()`), 'search results fit the screen');
await shot('phone-search');
await key('Escape');
await ev(`${el('#search-input')}.value = ''`);
await go('/', FRONT);
ok(await overflow() <= 0, 'the landing page fits a phone');
await ev(`document.getElementById('download').scrollIntoView()`);
await sleep(300);
await shot('phone-download');
await click('#tab-desktop');
ok(await visible('#panel-desktop .platform'), 'the download tabs work on a phone');
assertClean('phone');
await cdp('Emulation.clearDeviceMetricsOverride');

// ---- languages ----
section('languages');
const CHINESE = /[一-龥]/;
await setLang('ja');
await go('/docs/quickstart?os=macos');
ok((await ev('document.documentElement.lang')) === 'ja', 'a Japanese visitor gets Japanese navigation text');
ok(!CHINESE.test(await text('#doc')) && (await text('#doc h1')).startsWith('Quick start'), 'and the English guide');
ok(await visible('#lang-note'), 'with a note about the languages of the guide');
await setLang('zh-TW');
await go('/docs/quickstart?os=macos');
ok((await text('#doc h1')).startsWith('快速开始'), 'Traditional Chinese readers get the Simplified guide');
await setLang('en');
await go('/docs/quickstart?os=macos');
const english = await ev(`[...document.querySelectorAll('#main, #sidebar, #toc-box, .search, .section-link')].map(e => e.innerText + (e.placeholder || '')).join(' ') + document.getElementById('search-input').placeholder + document.title`);
ok(await ev(`${el('#lang-note')}.hidden`) && !CHINESE.test(english), 'English: no Chinese on the page and no note');
await go('/', FRONT);
ok(!CHINESE.test(await text('#download')), 'English: the download section is translated');
await ev(`document.getElementById('download').scrollIntoView()`);
await sleep(300);
await shot('download-en');
for (const l of ['ja', 'de', 'ko', 'ru']) {
  await setLang(l);
  await go('/', FRONT);
  const bad = await ev(`document.getElementById('download').innerText`);
  if (l !== 'ja' && CHINESE.test(bad)) throw new Error(`${l}: untranslated: ${bad}`);
  if (await overflow() > 0) throw new Error(`${l}: landing page scrolls sideways`);
}
ok(true, 'the download section is translated in sampled languages');
await setLang('en');
await go('/docs/');
await ev(`(() => { document.getElementById('lang-select').click(); document.querySelector('.lang-item[lang="zh-CN"]').click(); })()`);
await sleep(300);
await waitFor(DOC_READY);
await waitFor(`document.querySelector('#doc h1').textContent.startsWith('概览')`);
ok(true, 'the language picker changes the guide');
assertClean('languages');

// ---- pictures for a person to look at ----
await setLang('zh-CN');
await cdp('Emulation.setDeviceMetricsOverride', { width: 1320, height: 900, deviceScaleFactor: 1, mobile: false });
for (const [name, path] of [['desktop-overview', '/docs/'], ['desktop-install', '/docs/install?os=windows'], ['desktop-running', '/docs/running?os=linux'], ['desktop-use', '/docs/use-models?os=macos']]) {
  await go(path);
  await sleep(200);
  await shot(name);
}
await setLang('en');
await go('/docs/share?os=macos');
await sleep(200);
await shot('desktop-en');
await go('/', FRONT);
await ev(`document.getElementById('download').scrollIntoView()`);
await sleep(300);
await shot('desktop-download');
assertClean('final');
console.log(`\n${passed} checks passed. Screenshots in ${shots}`);
process.exit(0);
