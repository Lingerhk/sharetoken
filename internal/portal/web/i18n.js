// Translation runtime for the landing page, the user portal and the desktop
// client. Keep internal/portal/web/i18n.js and internal/client/ui/i18n.js byte-identical (a test
// checks it).
//
// The Chinese source text is the key: tr('登录') looks up the current language's
// dictionary and falls back to the source. A page that pins data-lang="zh-CN"
// (the admin console) loads no dictionary and never shows a language picker.
//
// A script tag selects the dictionary: <script src="…/i18n.js" data-bundle="site">
// loads …/i18n/site/<lang>.json. Dictionaries hold plain text; only rich() below
// understands markup, and it builds <br>, <em> and <strong> without parsing HTML.
//
// data-langs="en,zh-CN" limits a page to those languages, and data-detect="system"
// lets the browser's (for the desktop client: the system's) language choose when
// the user has not; the desktop client sets both.
'use strict';

const I18N = (() => {
  const SOURCE = 'zh-CN', FALLBACK = 'en', KEY = 'st_lang';
  const ALL_LANGS = {
    en: 'English', 'zh-CN': '简体中文', 'zh-TW': '繁體中文', es: 'Español', fr: 'Français', de: 'Deutsch',
    'pt-BR': 'Português', ru: 'Русский', ja: '日本語', ko: '한국어',
  };
  const RTL = new Set(['ar', 'ur']);

  const script = document.currentScript;
  const base = script.src.replace(/[^/]*$/, '') + 'i18n/' + script.dataset.bundle + '/';
  const fixed = script.dataset.lang || '';
  const only = (script.dataset.langs || '').split(',').map(x => x.trim()).filter(x => ALL_LANGS[x]);
  const LANGS = only.length ? Object.fromEntries(only.map(x => [x, ALL_LANGS[x]])) : ALL_LANGS;
  const detect = script.dataset.detect === 'system';

  function stored() { try { return localStorage.getItem(KEY) || ''; } catch (e) { return ''; } }
  function store(lang) { try { localStorage.setItem(KEY, lang); } catch (e) { /* this visit only */ } }

  // match maps a browser language tag to a supported language, or ''.
  function match(tag) {
    const lc = String(tag || '').toLowerCase(), primary = lc.split('-')[0];
    if (primary === 'zh') {
      const zh = /hant|-tw|-hk|-mo/.test(lc) ? 'zh-TW' : 'zh-CN';
      return LANGS[zh] ? zh : LANGS['zh-CN'] ? 'zh-CN' : LANGS['zh-TW'] ? 'zh-TW' : '';
    }
    if (primary === 'pt') return LANGS['pt-BR'] ? 'pt-BR' : '';
    return Object.keys(LANGS).find(l => l.toLowerCase() === lc) || (LANGS[primary] ? primary : '');
  }
  function choose() {
    if (fixed) return fixed;
    const asked = match(new URLSearchParams(location.search).get('lang'));
    if (asked) { store(asked); return asked; }
    const saved = match(stored());
    if (saved) return saved;
    if (detect) {
      // The first preferred language decides: a Chinese system gets Chinese, any other English.
      const first = (navigator.languages && navigator.languages[0]) || navigator.language;
      return match(first) || FALLBACK;
    }
    return FALLBACK;
  }

  const lang = choose();
  let dict = {};
  const root = document.documentElement;
  root.lang = lang;
  root.dir = RTL.has(lang) ? 'rtl' : 'ltr';

  function t(s, vars) {
    const text = Object.prototype.hasOwnProperty.call(dict, s) ? dict[s] : s;
    return vars ? text.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m)) : text;
  }
  // parts is t() for a sentence with nodes in it: an array of strings and nodes.
  function parts(s, vars) {
    return t(s).split(/(\{\w+\})/).filter(Boolean).map(p => {
      const m = p.match(/^\{(\w+)\}$/);
      return m && m[1] in vars ? vars[m[1]] : p;
    });
  }
  // rich replaces el's content with text holding only <br>, <em>, <strong>.
  function rich(el, text) {
    const stack = [el];
    while (el.firstChild) el.removeChild(el.firstChild);
    for (const p of text.split(/(<br>|<\/?(?:em|strong)>)/)) {
      const top = stack[stack.length - 1];
      if (p === '<br>') top.appendChild(document.createElement('br'));
      else if (p === '<em>' || p === '<strong>') stack.push(top.appendChild(document.createElement(p.slice(1, -1))));
      else if (p === '</em>' || p === '</strong>') stack.pop();
      else if (p) top.appendChild(document.createTextNode(p));
    }
  }

  // translate rewrites the page's static text: elements marked data-i18n-rich as
  // a whole, other text nodes and attributes when their exact text is a key.
  function translate() {
    document.querySelectorAll('[data-i18n-rich]').forEach(el => {
      const key = el.innerHTML.trim();
      if (dict[key]) rich(el, dict[key]);
      el.setAttribute('data-i18n-done', '');
    });
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const parent = n.parentElement;
      if (!parent || parent.closest('script,style,[data-i18n-done]')) continue;
      const key = n.nodeValue.trim();
      if (key && dict[key]) n.nodeValue = n.nodeValue.replace(key, dict[key]);
    }
    for (const attr of ['aria-label', 'title', 'alt', 'placeholder']) {
      document.querySelectorAll('[' + attr + ']').forEach(el => {
        const v = dict[el.getAttribute(attr)];
        if (v) el.setAttribute(attr, v);
      });
    }
    document.querySelectorAll('meta[name=description], meta[property^="og:"]').forEach(el => {
      const v = dict[el.getAttribute('content')];
      if (v) el.setAttribute('content', v);
    });
  }

  // svg draws an outline icon from one path on a 24×24 grid.
  function svg(d, size) {
    const NS = 'http://www.w3.org/2000/svg';
    const el = document.createElementNS(NS, 'svg');
    const set = (node, attrs) => { for (const k in attrs) node.setAttribute(k, attrs[k]); };
    set(el, { viewBox: '0 0 24 24', width: size || 18, height: size || 18, fill: 'none', stroke: 'currentColor', 'stroke-width': 1.5, 'aria-hidden': 'true' });
    const path = document.createElementNS(NS, 'path');
    set(path, { d, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' });
    el.appendChild(path);
    return el;
  }
  const GLOBE = 'M12 21a9.004 9.004 0 008.716-6.747M12 21a9.004 9.004 0 01-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 017.843 4.582M12 3a8.997 8.997 0 00-7.843 4.582m15.686 0A11.953 11.953 0 0112 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0121 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0112 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 013 12c0-1.605.42-3.113 1.157-4.418';
  const CHEVRON = 'M19.5 8.25l-7.5 7.5-7.5-7.5', CHECK = 'M4.5 12.75l6 6 9-13.5';

  function choosePicked(code) {
    store(code);
    const u = new URL(location.href);
    u.searchParams.delete('lang'); // it would override the choice just made
    // An address that differs by nothing, or only by its #fragment, would not reload.
    if (u.href === location.href) location.reload(); else location.replace(u.href);
  }

  // picker builds the language menu: a globe, the current language, and a list of all.
  function picker() {
    if (fixed) return null;
    const wrap = document.createElement('div');
    wrap.className = 'lang-picker';
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'lang-btn';
    btn.id = 'lang-select';
    btn.title = 'Language';
    btn.setAttribute('aria-label', 'Language: ' + LANGS[lang]);
    btn.setAttribute('aria-haspopup', 'menu');
    btn.setAttribute('aria-expanded', 'false');
    const name = document.createElement('span');
    name.className = 'lang-name';
    name.textContent = LANGS[lang];
    const chevron = svg(CHEVRON, 12);
    chevron.classList.add('lang-chevron');
    btn.append(svg(GLOBE, 18), name, chevron);

    const menu = document.createElement('div');
    menu.className = 'lang-menu';
    menu.setAttribute('role', 'menu');
    menu.hidden = true;
    for (const [code, label] of Object.entries(LANGS)) {
      const item = document.createElement('button');
      item.type = 'button';
      item.className = 'lang-item';
      item.lang = code;
      item.setAttribute('role', 'menuitemradio');
      item.setAttribute('aria-checked', String(code === lang));
      const text = document.createElement('span');
      text.textContent = label;
      item.appendChild(text);
      if (code === lang) item.appendChild(svg(CHECK, 15));
      item.addEventListener('click', () => { close(); choosePicked(code); });
      menu.appendChild(item);
    }

    const onOutside = e => { if (!wrap.contains(e.target)) close(); };
    function close() {
      menu.hidden = true;
      btn.setAttribute('aria-expanded', 'false');
      document.removeEventListener('click', onOutside);
    }
    function open() {
      menu.hidden = false;
      btn.setAttribute('aria-expanded', 'true');
      document.addEventListener('click', onOutside);
      (menu.querySelector('[aria-checked=true]') || menu.firstChild).focus();
    }
    btn.addEventListener('click', () => { if (menu.hidden) open(); else close(); });
    wrap.addEventListener('keydown', e => {
      if (e.key === 'Escape' && !menu.hidden) { e.stopPropagation(); close(); btn.focus(); return; }
      if (e.key === 'Tab') { close(); return; }
      if (menu.hidden) return;
      const items = [...menu.children], at = items.indexOf(document.activeElement);
      const to = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: items.length - 1 }[e.key];
      if (to === undefined) return;
      e.preventDefault();
      items[(to + items.length) % items.length].focus();
    });
    wrap.append(btn, menu);
    return wrap;
  }

  const domReady = new Promise(r => {
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', r, { once: true }); else r();
  });
  // Hide the source text until the translation is in; the stylesheets hide the
  // body on this class, and a script that never runs leaves the page readable.
  if (lang !== SOURCE) root.classList.add('i18n-pending');
  const ready = (async () => {
    try {
      if (lang !== SOURCE) {
        const resp = await fetch(base + lang + '.json', { signal: AbortSignal.timeout(4000) });
        if (resp.ok) dict = await resp.json();
      }
    } catch (e) { /* the source text stays readable */ }
    await domReady;
    if (lang !== SOURCE) translate();
    document.querySelectorAll('[data-lang-picker]').forEach(el => { const p = picker(); if (p) el.appendChild(p); });
    root.classList.remove('i18n-pending');
  })();

  const list = items => new Intl.ListFormat(lang, { style: 'narrow' }).format(items);
  return { lang, ready, t, parts, rich, picker, list, svg, LANGS };
})();
