// Translation runtime for the landing page, the user portal and the desktop
// client. Keep portal/web/i18n.js and client/ui/i18n.js byte-identical (a test
// checks it).
//
// The Chinese source text is the key: tr('登录') looks up the current language's
// dictionary and falls back to the source. A page that pins data-lang="zh-CN"
// (the admin console) loads no dictionary and never shows a language picker.
//
// A script tag selects the dictionary: <script src="…/i18n.js" data-bundle="site">
// loads …/i18n/site/<lang>.json. Dictionaries hold plain text; only rich() below
// understands markup, and it builds <br>, <em> and <strong> without parsing HTML.
'use strict';

const I18N = (() => {
  const SOURCE = 'zh-CN', FALLBACK = 'en', KEY = 'st_lang';
  const LANGS = {
    en: 'English', 'zh-CN': '简体中文', 'zh-TW': '繁體中文', es: 'Español', fr: 'Français', de: 'Deutsch',
    'pt-BR': 'Português (Brasil)', ru: 'Русский', ja: '日本語', ko: '한국어', ar: 'العربية', hi: 'हिन्दी',
    bn: 'বাংলা', ur: 'اردو', id: 'Bahasa Indonesia', vi: 'Tiếng Việt', tr: 'Türkçe', it: 'Italiano', pl: 'Polski', th: 'ไทย',
  };
  const RTL = new Set(['ar', 'ur']);

  const script = document.currentScript;
  const base = script.src.replace(/[^/]*$/, '') + 'i18n/' + script.dataset.bundle + '/';
  const fixed = script.dataset.lang || '';

  function stored() { try { return localStorage.getItem(KEY) || ''; } catch (e) { return ''; } }
  function store(lang) { try { localStorage.setItem(KEY, lang); } catch (e) { /* this visit only */ } }

  // match maps a browser language tag to a supported language, or ''.
  function match(tag) {
    const lc = String(tag || '').toLowerCase(), primary = lc.split('-')[0];
    if (primary === 'zh') return /hant|-tw|-hk|-mo/.test(lc) ? 'zh-TW' : 'zh-CN';
    if (primary === 'pt') return 'pt-BR';
    return Object.keys(LANGS).find(l => l.toLowerCase() === lc) || (LANGS[primary] ? primary : '');
  }
  function choose() {
    if (fixed) return fixed;
    const asked = match(new URLSearchParams(location.search).get('lang'));
    if (asked) { store(asked); return asked; }
    const saved = match(stored());
    if (saved) return saved;
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

  function picker() {
    if (fixed) return null;
    const sel = document.createElement('select');
    sel.className = 'lang-select';
    sel.id = 'lang-select';
    sel.setAttribute('aria-label', 'Language');
    for (const [code, name] of Object.entries(LANGS)) {
      const o = document.createElement('option');
      o.value = code; o.textContent = name; o.lang = code; o.selected = code === lang;
      sel.appendChild(o);
    }
    sel.addEventListener('change', () => {
      store(sel.value);
      const u = new URL(location.href);
      u.searchParams.delete('lang'); // it would override the choice just made
      location.replace(u.href);
    });
    return sel;
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
  return { lang, ready, t, parts, rich, picker, list, LANGS };
})();
