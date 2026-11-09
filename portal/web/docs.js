// The user guide at /docs/. Pages are Markdown under /static/docs/<language>/,
// drawn into the DOM with textContent only, never as HTML (see the portal's CSP).
// Supported Markdown: headings with {#id}, paragraphs, lists, tables, code fences,
// `code`, **bold**, [links](url), and:
//   > [!NOTE] / [!TIP] / [!WARNING]     a callout
//   ::: macos linux                     content for these systems; closes with :::
//   ::: downloads                       the portal's packages for the chosen system
//   ::: cards ... [title](url) text     a grid of links
// {{exe}}, {{home}}, {{homewin}}, {{port}}, {{version}}, {{origin}} and {{env}} fill
// in the network this portal serves, so dev.sharetoken.app shows dev commands.
'use strict';

I18N.ready.then(async () => {
  const tr = I18N.t;
  const $ = id => document.getElementById(id);
  const OSES = [['macos', 'macOS'], ['linux', 'Linux'], ['windows', 'Windows']];
  // A reader of Traditional Chinese is better served by the Simplified guide than the English one.
  const docLang = I18N.lang === 'zh-CN' || I18N.lang === 'zh-TW' ? 'zh-CN' : 'en';
  const OS_KEY = 'st_docs_os';

  const store = {
    get(k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* this visit only */ } },
  };
  function mk(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === undefined || v === null) continue;
      if (k === 'class') e.className = v; else e.setAttribute(k, v);
    }
    for (const c of kids.flat()) if (c !== null && c !== undefined) e.append(c);
    return e;
  }

  // ---- what this portal serves ----
  let downloads = { packages: [] };
  try {
    const r = await fetch('/api/v1/downloads', { headers: { Accept: 'application/json' } });
    if (r.ok) downloads = await r.json();
  } catch (e) { /* the guide still reads without the package list */ }
  const dev = downloads.env ? downloads.env === 'dev' : /^dev\./.test(location.hostname);
  const VARS = {
    exe: dev ? 'sharetoken-dev' : 'sharetoken-prod',
    home: dev ? '~/.sharetoken-dev' : '~/.sharetoken',
    homewin: dev ? '$HOME\\.sharetoken-dev' : '$HOME\\.sharetoken',
    port: dev ? '9887' : '9787',
    env: dev ? 'dev' : 'prod',
    version: downloads.version || '<version>',
    origin: location.origin,
  };
  const fill = s => s.replace(/\{\{(\w+)\}\}/g, (m, k) => (k in VARS ? VARS[k] : m));

  // ---- Markdown ----
  function inline(text) {
    const out = [];
    const re = /`([^`]+)`|\*\*(.+?)\*\*|\[([^\]]+)\]\(([^)\s]+)\)/g;
    let at = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > at) out.push(text.slice(at, m.index));
      if (m[1] !== undefined) out.push(mk('code', null, m[1]));
      else if (m[2] !== undefined) out.push(mk('strong', null, inline(m[2])));
      else out.push(link(m[3], m[4]));
      at = re.lastIndex;
    }
    if (at < text.length) out.push(text.slice(at));
    return out;
  }
  function link(text, url) {
    const external = /^https?:\/\//.test(url);
    if (!external && !/^[/#]/.test(url)) return text; // nothing else may become a link
    return mk('a', external ? { href: url, target: '_blank', rel: 'noopener noreferrer' } : { href: url }, inline(text));
  }
  const plain = text => text.replace(/`([^`]+)`/g, '$1').replace(/\*\*(.+?)\*\*/g, '$1').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1');

  function codeBlock(lang, body) {
    const pre = mk('pre', null);
    const code = mk('code', null);
    const commentMark = lang === 'json' || lang === 'text' || lang === 'xml' ? null : '#';
    const lines = body.split('\n');
    lines.forEach((line, i) => {
      const text = line + (i < lines.length - 1 ? '\n' : '');
      if (commentMark && line.trimStart().startsWith(commentMark)) code.append(mk('span', { class: 'comment' }, text));
      else code.append(text);
    });
    pre.append(code);
    const copy = mk('button', { class: 'copy', type: 'button' }, tr('复制'));
    copy.addEventListener('click', async () => {
      const ok = await copyText(body);
      copy.textContent = ok ? tr('已复制') : tr('复制失败');
      copy.classList.toggle('done', ok);
      setTimeout(() => { copy.textContent = tr('复制'); copy.classList.remove('done'); }, 1600);
    });
    return mk('div', { class: 'code' }, mk('div', { class: 'code-head' }, mk('span', null, lang || ''), copy), pre);
  }
  async function copyText(s) {
    try { await navigator.clipboard.writeText(s); return true; } catch (e) { /* not a secure context, or refused */ }
    const t = mk('textarea', { readonly: '', 'aria-hidden': 'true', class: 'offscreen' });
    t.value = s;
    document.body.append(t);
    t.select();
    let ok = false;
    try { ok = document.execCommand('copy'); } catch (e) { /* unsupported */ }
    t.remove();
    return ok;
  }

  function splitRow(line) {
    return line.trim().replace(/^\|/, '').replace(/\|$/, '').split(/(?<!\\)\|/).map(c => c.trim().replace(/\\\|/g, '|'));
  }
  function heading(level, raw, ctx) {
    const m = raw.match(/^(.*?)\s*\{#([a-z0-9-]+)\}$/);
    const title = m ? m[1] : raw, id = m ? m[2] : '';
    const h = mk('h' + level, id ? { id } : null, inline(title));
    if (level === 1) h.tabIndex = -1;
    if (id) {
      h.append(mk('a', { class: 'anchor', href: '#' + id, 'aria-label': tr('链接到本节') }, '#'));
      ctx.sections.push({ level, id, title: plain(title) });
    }
    return h;
  }

  // parse turns Markdown lines into DOM nodes, appended to parent.
  function parse(lines, parent, ctx) {
    let i = 0;
    const isBlockStart = l => /^(#{1,3} |```|::: ?|> |[-*] |\d+\. |\|)/.test(l);
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) { i++; continue; }
      let m;
      if ((m = line.match(/^(#{1,3}) (.+)$/))) { parent.append(heading(m[1].length, m[2], ctx)); i++; continue; }
      if ((m = line.match(/^```(\w*)\s*$/))) {
        let j = i + 1;
        while (j < lines.length && !/^```\s*$/.test(lines[j])) j++;
        parent.append(codeBlock(m[1], lines.slice(i + 1, j).join('\n')));
        i = j + 1;
        continue;
      }
      if ((m = line.match(/^::: ?(.+)$/))) {
        let depth = 1, j = i + 1;
        for (; j < lines.length; j++) {
          if (/^:::\s*$/.test(lines[j])) { if (--depth === 0) break; } else if (/^::: ?\S/.test(lines[j])) depth++;
        }
        const kind = m[1].trim(), body = lines.slice(i + 1, j);
        if (kind === 'downloads') parent.append(mk('div', { class: 'dl-slot', 'data-downloads': '' }));
        else if (kind === 'cards') parent.append(cards(body));
        else {
          const box = mk('div', { class: 'os-block', 'data-os': kind });
          const inner = { sections: [] };
          parse(body, box, inner);
          for (const s of inner.sections) ctx.sections.push({ ...s, os: kind });
          parent.append(box);
        }
        i = j + 1;
        continue;
      }
      if (line.startsWith('> ')) {
        let j = i;
        while (j < lines.length && lines[j].startsWith('>')) j++;
        const body = lines.slice(i, j).map(l => l.replace(/^> ?/, ''));
        const kindMatch = body[0].match(/^\[!(NOTE|TIP|WARNING)\]$/);
        const kind = kindMatch ? kindMatch[1].toLowerCase() : 'note';
        const label = { note: tr('说明'), tip: tr('提示'), warning: tr('注意') }[kind];
        const box = mk('div', { class: 'callout ' + kind }, mk('b', null, label));
        parse(kindMatch ? body.slice(1) : body, box, ctx);
        parent.append(box);
        i = j;
        continue;
      }
      if (line.startsWith('|')) {
        let j = i;
        while (j < lines.length && lines[j].startsWith('|')) j++;
        const head = splitRow(lines[i]), rows = lines.slice(i + 2, j).map(splitRow);
        const wrap = head.length > 2 ? 'table wrap' : 'table';
        parent.append(mk('div', { class: wrap }, mk('table', null,
          mk('thead', null, mk('tr', null, head.map(c => mk('th', { scope: 'col' }, inline(c))))),
          mk('tbody', null, rows.map(r => mk('tr', null, r.map(c => mk('td', null, inline(c))))))),
        ));
        i = j;
        continue;
      }
      if ((m = line.match(/^([-*]|\d+\.) /))) {
        const ordered = /\d/.test(m[1]);
        const list = mk(ordered ? 'ol' : 'ul');
        const itemRe = ordered ? /^\d+\. (.*)$/ : /^[-*] (.*)$/;
        while (i < lines.length) {
          const im = lines[i].match(itemRe);
          if (!im) break;
          // An item runs on over its indented lines, which may hold a code block.
          const body = [im[1]];
          i++;
          while (i < lines.length && (lines[i].startsWith('   ') || (!lines[i].trim() && i + 1 < lines.length && lines[i + 1].startsWith('   ')))) {
            body.push(lines[i].replace(/^ {1,3}/, ''));
            i++;
          }
          const li = mk('li');
          parse(body, li, ctx);
          // A one-paragraph item needs no <p> around it.
          if (li.childElementCount === 1 && li.firstElementChild.tagName === 'P') li.replaceChildren(...li.firstElementChild.childNodes);
          list.append(li);
          if (i < lines.length && !lines[i].trim() && i + 1 < lines.length && itemRe.test(lines[i + 1])) i++;
        }
        parent.append(list);
        continue;
      }
      let j = i + 1;
      while (j < lines.length && lines[j].trim() && !isBlockStart(lines[j])) j++;
      parent.append(mk('p', null, inline(lines.slice(i, j).join(' '))));
      i = j;
    }
  }
  function cards(body) {
    const box = mk('div', { class: 'cards' });
    for (const line of body) {
      const m = line.match(/^\[([^\]]+)\]\(([^)]+)\)\s*(.*)$/);
      if (m && /^\//.test(m[2])) box.append(mk('a', { class: 'card', href: m[2] }, mk('b', null, m[1]), m[3]));
    }
    return box;
  }

  // ---- platform ----
  function detectOS() {
    const asked = new URLSearchParams(location.search).get('os');
    if (OSES.some(o => o[0] === asked)) { store.set(OS_KEY, asked); return asked; }
    const saved = store.get(OS_KEY);
    if (OSES.some(o => o[0] === saved)) return saved;
    const ua = navigator.userAgent;
    if (/Windows/.test(ua)) return 'windows';
    if (/Macintosh|Mac OS X/.test(ua)) return 'macos';
    return 'linux';
  }
  let os = detectOS();
  const osTabs = OSES.map(([id, name]) => {
    const b = mk('button', { type: 'button', role: 'radio', 'data-os': id }, name);
    b.addEventListener('click', () => setOS(id, true));
    b.addEventListener('keydown', e => {
      const d = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
      if (!d) return;
      e.preventDefault();
      const next = OSES[(OSES.findIndex(o => o[0] === id) + d + OSES.length) % OSES.length][0];
      setOS(next, true);
      $('os-tabs').querySelector(`[data-os=${next}]`).focus();
    });
    return b;
  });
  $('os-tabs').append(...osTabs);

  function setOS(next, byUser) {
    // Switching should not throw the reader to another place on the page.
    const anchor = byUser ? firstVisibleHeading() : null;
    const before = anchor ? anchor.getBoundingClientRect().top : 0;
    os = next;
    if (byUser) {
      store.set(OS_KEY, next);
      const u = new URL(location.href);
      u.searchParams.set('os', next);
      history.replaceState(history.state, '', u.pathname + u.search + u.hash);
    }
    applyOS();
    if (anchor && anchor.isConnected && anchor.offsetParent) scrollBy(0, anchor.getBoundingClientRect().top - before);
  }
  function firstVisibleHeading() {
    for (const h of $('doc').querySelectorAll('h1, h2, h3')) {
      if (!h.closest('.os-block') && h.getBoundingClientRect().bottom > 0) return h;
    }
    return null;
  }
  function applyOS() {
    for (const b of osTabs) {
      const on = b.dataset.os === os;
      b.setAttribute('aria-checked', String(on));
      b.tabIndex = on ? 0 : -1;
    }
    $('doc').querySelectorAll('.os-block').forEach(el => { el.hidden = !el.dataset.os.split(/\s+/).includes(os); });
    drawDownloads();
    drawToc();
  }

  function drawDownloads() {
    $('doc').querySelectorAll('[data-downloads]').forEach(slot => {
      const mine = downloads.packages.filter(p => p.kind === 'cli' && p.os === os);
      if (!mine.length) {
        slot.replaceChildren(mk('div', { class: 'callout note' }, mk('b', null, tr('说明')), mk('p', null, tr('这个门户暂时没有可下载的命令行安装包。'))));
        return;
      }
      const arch = { macos: { arm64: 'Apple Silicon (arm64)', amd64: 'Intel (amd64)' }, linux: { amd64: 'x86-64 (amd64)', arm64: 'ARM64 (arm64)' }, windows: { amd64: 'x64 (amd64)', arm64: 'ARM64 (arm64)' } }[os];
      slot.replaceChildren(mk('div', { class: 'table dl-table' }, mk('table', null,
        mk('thead', null, mk('tr', null, mk('th', { scope: 'col' }, tr('架构')), mk('th', { scope: 'col' }, tr('安装包')), mk('th', { scope: 'col' }, tr('大小')))),
        mk('tbody', null, mine.map(p => mk('tr', { 'data-package': p.file },
          mk('td', null, arch[p.arch] || p.arch),
          mk('td', null, mk('a', { href: p.url, download: p.file }, p.file), p.sha256 ? mk('span', { class: 'sum' }, 'SHA-256 ' + p.sha256) : null),
          mk('td', null, (p.size / 1048576).toLocaleString(I18N.lang, { maximumFractionDigits: 1 }) + ' MB')))))));
    });
  }

  // ---- contents ----
  let observer = null;
  function drawToc() {
    const list = $('toc'), box = $('toc-box');
    list.textContent = '';
    if (observer) observer.disconnect();
    const heads = [...$('doc').querySelectorAll('h2[id], h3[id]')].filter(h => !h.closest('[hidden]'));
    box.hidden = heads.length < 2;
    const items = new Map();
    for (const h of heads) {
      const label = [...h.childNodes].filter(n => !(n.classList && n.classList.contains('anchor'))).map(n => n.textContent).join('');
      const a = mk('a', { href: '#' + h.id }, label);
      list.append(mk('li', h.tagName === 'H3' ? { class: 'sub' } : null, a));
      items.set(h, a);
    }
    if (!heads.length || !('IntersectionObserver' in window)) return;
    const seen = new Set();
    observer = new IntersectionObserver(es => {
      for (const e of es) { if (e.isIntersecting) seen.add(e.target); else seen.delete(e.target); }
      const current = heads.find(h => seen.has(h));
      if (!current) return;
      items.forEach((a, h) => a.classList.toggle('on', h === current));
    }, { rootMargin: '-120px 0px -65% 0px' });
    heads.forEach(h => observer.observe(h));
  }

  // ---- pages ----
  let nav = [];
  const flat = () => nav.flatMap(g => g.pages);
  const title = (t) => t[docLang] || t.en;
  const pageFromPath = () => {
    const m = location.pathname.match(/^\/docs\/([^/]*)$/);
    return (m && m[1]) || 'overview';
  };
  const urlOf = slug => (slug === 'overview' ? '/docs/' : '/docs/' + slug);

  const cache = new Map();
  async function source(slug) {
    if (!cache.has(slug)) {
      const p = fetch(`/static/docs/${docLang}/${slug}.md`, { signal: AbortSignal.timeout(15000) }).then(r => {
        if (!r.ok) throw new Error(String(r.status));
        return r.text();
      });
      cache.set(slug, p);
      p.catch(() => cache.delete(slug));
    }
    return fill(await cache.get(slug));
  }

  function drawNav() {
    const box = $('sidebar');
    box.textContent = '';
    for (const g of nav) {
      box.append(mk('div', { class: 'nav-group' }, mk('h2', null, title(g.title)), mk('ul', null, g.pages.map(p => mk('li', null, mk('a', { href: urlOf(p.slug), 'data-page': p.slug }, title(p.title)))))));
    }
  }
  function markNav(slug) {
    $('sidebar').querySelectorAll('a[data-page]').forEach(a => {
      if (a.dataset.page === slug) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
  }
  function drawPager(slug) {
    const pages = flat(), i = pages.findIndex(p => p.slug === slug), box = $('pager');
    box.textContent = '';
    if (i < 0) return;
    if (i > 0) box.append(mk('a', { class: 'prev', href: urlOf(pages[i - 1].slug) }, mk('small', null, tr('上一页')), title(pages[i - 1].title)));
    if (i < pages.length - 1) box.append(mk('a', { class: 'next', href: urlOf(pages[i + 1].slug) }, mk('small', null, tr('下一页')), title(pages[i + 1].title)));
  }

  let drawing = 0;
  async function show(focusTitle) {
    const slug = pageFromPath(), serial = ++drawing, doc = $('doc');
    const info = flat().find(p => p.slug === slug);
    closeMenu();
    closeSearch();
    markNav(slug);
    drawPager(slug);
    if (!info) {
      $('os-bar').hidden = true;
      doc.replaceChildren(mk('h1', { tabindex: '-1' }, tr('找不到这个页面')), mk('p', { class: 'state' }, tr('链接可能已经过期。请从左侧目录选择一个页面。')));
      document.title = tr('找不到这个页面') + ' · ' + tr('ShareToken 文档');
      drawToc();
      return;
    }
    let text;
    try {
      text = await source(slug);
    } catch (e) {
      if (serial !== drawing) return;
      const retry = mk('button', { type: 'button' }, tr('重试'));
      retry.addEventListener('click', () => show(true));
      $('os-bar').hidden = true;
      doc.replaceChildren(mk('div', { class: 'state' }, tr('页面加载失败，请检查网络后重试。'), retry));
      drawToc();
      return;
    }
    if (serial !== drawing) return;
    const frag = document.createDocumentFragment();
    const ctx = { sections: [] };
    parse(text.split('\n'), frag, ctx);
    doc.replaceChildren(frag);
    doc.lang = docLang;
    doc.dir = 'ltr';
    const firstP = doc.querySelector('h1 + p');
    if (firstP) firstP.classList.add('lede');
    $('os-bar').hidden = !doc.querySelector('.os-block, [data-downloads]');
    document.title = title(info.title) + ' · ' + tr('ShareToken 文档');
    applyOS();
    const anchor = location.hash.slice(1);
    const target = anchor && document.getElementById(anchor);
    if (target) {
      reveal(target);
      target.scrollIntoView();
    } else {
      scrollTo(0, 0);
    }
    if (focusTitle && !target) { const h1 = doc.querySelector('h1'); if (h1) h1.focus({ preventScroll: true }); }
  }

  // A link into another system's section brings its system along.
  function reveal(target) {
    const block = target.closest('.os-block');
    if (!block) return;
    const list = block.dataset.os.split(/\s+/);
    if (!list.includes(os)) os = list[0];
    applyOS();
  }

  function navigate(url, replace) {
    const before = location.pathname;
    history[replace ? 'replaceState' : 'pushState'](null, '', url);
    const asked = new URLSearchParams(location.search).get('os');
    if (OSES.some(o => o[0] === asked)) { os = asked; store.set(OS_KEY, asked); }
    if (location.pathname === before && location.hash) {
      const t = document.getElementById(location.hash.slice(1));
      closeMenu();
      closeSearch();
      applyOS();
      if (t) { reveal(t); t.scrollIntoView(); }
      return;
    }
    show(true);
  }
  document.addEventListener('click', e => {
    if (e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest('a[href]');
    if (!a || a.target || a.hasAttribute('download')) return;
    const u = new URL(a.href);
    if (u.origin !== location.origin || !/^\/docs\/[^/]*$/.test(u.pathname)) return;
    e.preventDefault();
    navigate(u.pathname + u.search + u.hash);
  });
  addEventListener('popstate', () => {
    const asked = new URLSearchParams(location.search).get('os');
    if (OSES.some(o => o[0] === asked)) os = asked;
    show(false);
  });

  // ---- phone menu ----
  const menu = $('menu'), sidebar = $('sidebar'), backdrop = $('backdrop');
  function openMenu(on) {
    sidebar.classList.toggle('open', on);
    menu.setAttribute('aria-expanded', String(on));
    backdrop.hidden = !on;
  }
  function closeMenu() { openMenu(false); }
  menu.addEventListener('click', () => openMenu(!sidebar.classList.contains('open')));
  backdrop.addEventListener('click', closeMenu);

  // ---- search: over all pages of this language, section by section ----
  const input = $('search-input'), results = $('search-results');
  let index = null, active = -1, found = [];
  function loadIndex() {
    if (!index) {
      index = Promise.all(flat().map(async p => {
        const lines = (await source(p.slug)).split('\n');
        const sections = [];
        let cur = { page: p, id: '', title: title(p.title), os: '', parts: [] };
        let osStack = '', fence = false;
        for (const raw of lines) {
          if (/^```/.test(raw)) { fence = !fence; continue; }
          let m;
          if (!fence && (m = raw.match(/^::: ?(.+)$/))) { const k = m[1].trim(); if (k !== 'downloads' && k !== 'cards') osStack = k; continue; }
          if (!fence && /^:::\s*$/.test(raw)) { osStack = ''; continue; }
          if (!fence && (m = raw.match(/^(#{2,3}) (.*?)\s*\{#([a-z0-9-]+)\}$/))) {
            sections.push(cur);
            cur = { page: p, id: m[3], title: plain(m[2]), os: osStack, parts: [] };
            continue;
          }
          if (!fence && /^# /.test(raw)) continue;
          const line = plain(raw).replace(/^[>|\-*\s]+/, '').replace(/\[![A-Z]+\]/, '').replace(/\|/g, ' ').trim();
          if (line && !/^-+$/.test(line.replace(/\s/g, ''))) cur.parts.push({ os: osStack, text: line });
        }
        sections.push(cur);
        return sections;
      })).then(all => all.flat());
      index.catch(() => { index = null; });
    }
    return index;
  }
  function closeSearch() {
    results.hidden = true;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    active = -1;
  }
  function marked(text, terms) {
    const lower = text.toLowerCase(), out = [];
    let at = 0;
    for (;;) {
      let best = -1, len = 0;
      for (const t of terms) {
        const i = lower.indexOf(t, at);
        if (i >= 0 && (best < 0 || i < best)) { best = i; len = t.length; }
      }
      if (best < 0) break;
      if (best > at) out.push(text.slice(at, best));
      out.push(mk('mark', null, text.slice(best, best + len)));
      at = best + len;
    }
    if (at < text.length) out.push(text.slice(at));
    return out;
  }
  function snippet(text, terms) {
    const lower = text.toLowerCase();
    const hit = Math.min(...terms.map(t => { const i = lower.indexOf(t); return i < 0 ? Infinity : i; }));
    if (hit === Infinity) return text.slice(0, 110);
    const from = Math.max(0, hit - 36);
    return (from ? '…' : '') + text.slice(from, from + 120) + (from + 120 < text.length ? '…' : '');
  }
  async function runSearch() {
    const q = input.value.trim().toLowerCase();
    if (!q) { closeSearch(); return; }
    let sections;
    try { sections = await loadIndex(); } catch (e) { return; }
    if (input.value.trim().toLowerCase() !== q) return;
    const terms = q.split(/\s+/);
    found = sections.map(s => {
      const head = s.title.toLowerCase();
      const hits = s.parts.filter(p => terms.some(t => p.text.toLowerCase().includes(t)));
      const all = hits.map(p => p.text.toLowerCase()).join(' ');
      if (!terms.every(t => head.includes(t) || all.includes(t))) return null;
      // The systems the match is visible on. A heading shown to everyone can hold a
      // command for just one system, and the result has to open on that system.
      const inHead = terms.some(t => head.includes(t));
      const systems = s.os ? s.os.split(/\s+/)
        : inHead || hits.some(p => !p.os) ? []
        : [...new Set(hits.flatMap(p => p.os.split(/\s+/)))];
      let score = terms.filter(t => head.includes(t)).length * 10;
      if (systems.includes(os)) score += 2;
      else if (systems.length) score -= 3;
      // Prefer showing the line for the reader's own system.
      const line = hits.find(p => p.os.split(/\s+/).includes(os)) || hits[0];
      return { s: { ...s, systems, snippetText: line ? line.text : s.parts.map(p => p.text).join(' ') }, score };
    }).filter(Boolean).sort((a, b) => b.score - a.score).slice(0, 12).map(x => x.s);
    active = found.length ? 0 : -1;
    results.textContent = '';
    if (!found.length) {
      results.append(mk('div', { class: 'result-none' }, tr('没有找到相关内容')));
    } else {
      found.forEach((s, i) => {
        const osName = s.systems.map(o => (OSES.find(x => x[0] === o) || [o, o])[1]).join(' / ');
        const row = mk('a', { class: 'result', id: 'result-' + i, role: 'option', href: resultUrl(s), 'aria-selected': 'false' },
          mk('b', null, marked(s.title, terms), s.id ? mk('small', null, title(s.page.title)) : null, osName ? mk('small', null, osName) : null),
          marked(snippet(s.snippetText, terms), terms));
        results.append(row);
      });
      markActive();
    }
    results.hidden = false;
    input.setAttribute('aria-expanded', 'true');
  }
  function resultUrl(s) {
    const params = s.systems.length ? '?os=' + (s.systems.includes(os) ? os : s.systems[0]) : '';
    return urlOf(s.page.slug) + params + (s.id ? '#' + s.id : '');
  }
  function markActive() {
    [...results.querySelectorAll('.result')].forEach((r, i) => {
      r.classList.toggle('on', i === active);
      r.setAttribute('aria-selected', String(i === active));
      if (i === active) { input.setAttribute('aria-activedescendant', r.id); r.scrollIntoView({ block: 'nearest' }); }
    });
  }
  function openResult(url) {
    const u = new URL(url, location.href);
    closeSearch();
    input.value = '';
    navigate(u.pathname + u.search + u.hash);
  }
  let searchTimer;
  input.addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(runSearch, 90); });
  input.addEventListener('focus', () => { loadIndex().catch(() => {}); if (input.value.trim()) runSearch(); });
  input.addEventListener('keydown', e => {
    if (e.key === 'Escape') { closeSearch(); input.blur(); return; }
    if (results.hidden || !found.length) return;
    if (e.key === 'ArrowDown') { e.preventDefault(); active = (active + 1) % found.length; markActive(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); active = (active - 1 + found.length) % found.length; markActive(); }
    else if (e.key === 'Enter') { e.preventDefault(); if (active >= 0) openResult(resultUrl(found[active])); }
  });
  results.addEventListener('click', e => {
    const a = e.target.closest('a.result');
    if (!a) return;
    e.preventDefault();
    e.stopPropagation();
    openResult(a.getAttribute('href'));
  });
  document.addEventListener('click', e => { if (!e.target.closest('#search')) closeSearch(); });
  document.addEventListener('keydown', e => {
    if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return;
    if (/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)) return;
    e.preventDefault();
    input.focus();
  });

  // ---- start ----
  $('lang-note').hidden = docLang === I18N.lang;
  try {
    const r = await fetch('/static/docs-nav.json', { signal: AbortSignal.timeout(15000) });
    if (!r.ok) throw new Error(String(r.status));
    nav = await r.json();
  } catch (e) {
    const retry = mk('button', { type: 'button' }, tr('重试'));
    retry.addEventListener('click', () => location.reload());
    $('doc').replaceChildren(mk('div', { class: 'state' }, tr('页面加载失败，请检查网络后重试。'), retry));
    return;
  }
  drawNav();
  show(false);
});
