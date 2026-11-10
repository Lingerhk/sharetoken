// Shared helpers for the portal pages. No frameworks, no inline code (CSP);
// every piece of data goes into the DOM through textContent.
'use strict';

const ST = (() => {
  const tr = I18N.t;
  const mark = s => s; // a source string looked up later, when it is shown

  // ---- API ----
  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }
  async function api(method, path, body) {
    const headers = { 'X-Sharetoken-Web': '1', 'Accept': 'application/json' };
    const init = { method, headers, credentials: 'same-origin', cache: 'no-store' };
    if (body !== undefined) { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body); }
    let resp;
    try { resp = await fetch(path, init); } catch (e) { throw new ApiError(0, 'network', tr('无法连接门户')); }
    let data = null;
    try { data = await resp.json(); } catch (e) { /* no body */ }
    if (!resp.ok) {
      const err = (data && data.error) || {};
      const e = new ApiError(resp.status, err.code || 'error', err.message || ('HTTP ' + resp.status));
      e.data = data || {};
      throw e;
    }
    return data;
  }

  // ---- DOM ----
  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    if (attrs) for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'checked' || k === 'disabled' || k === 'selected') el[k] = !!v;
      else if (k === 'value') el.value = v;
      else el.setAttribute(k, String(v));
    }
    append(el, children);
    return el;
  }
  function append(el, children) {
    for (const c of children) {
      if (c === null || c === undefined || c === false) continue;
      if (Array.isArray(c)) append(el, c);
      else if (c instanceof Node) el.appendChild(c);
      else el.appendChild(document.createTextNode(String(c)));
    }
  }
  function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); return el; }
  // add appends children with the same rules as h() (null/false skipped, strings as text); never use Element.append with data.
  function add(el, ...children) { append(el, children); return el; }
  function $(id) { return document.getElementById(id); }

  // ---- formatting ----
  const MICRO = 1_000_000;
  function credit(micros, digits) {
    const n = Number(micros || 0) / MICRO;
    const d = digits === undefined ? (Math.abs(n) >= 100 ? 2 : 6) : digits; // small amounts are real: a short request costs a few micros
    return n.toLocaleString(I18N.lang, { minimumFractionDigits: 0, maximumFractionDigits: d });
  }
  function signed(micros) { const n = Number(micros || 0); return (n > 0 ? '+' : '') + credit(n); }
  function num(n) { return Number(n || 0).toLocaleString(I18N.lang); }
  function time(nanos) {
    if (!nanos) return '—';
    const d = new Date(Number(nanos) / 1e6);
    return d.toLocaleString(I18N.lang, { hour12: false });
  }
  function ago(nanos) {
    if (!nanos) return tr('从未');
    const s = Math.max(0, (Date.now() - Number(nanos) / 1e6) / 1000);
    if (s < 60) return tr('刚刚');
    const rel = new Intl.RelativeTimeFormat(I18N.lang);
    if (s < 3600) return rel.format(-Math.floor(s / 60), 'minute');
    if (s < 86400) return rel.format(-Math.floor(s / 3600), 'hour');
    return rel.format(-Math.floor(s / 86400), 'day');
  }
  function short(id, n) { n = n || 10; if (!id) return ''; return id.length <= 2 * n + 1 ? id : id.slice(0, n) + '…' + id.slice(-n); }
  function peer(id) { return h('code', { class: 'mono', title: id }, short(id, 8)); }
  const STATUS = { held: [mark('冻结中'), 'warn'], pending: [mark('待最终确认'), 'warn'], disputed: [mark('争议中'), 'bad'], final: [mark('已完成'), 'ok'], released: [mark('已释放'), ''], refunded: [mark('已退款'), 'bad'] };
  function status(st) { const [t, c] = STATUS[st] || [st, '']; return h('span', { class: 'badge ' + c }, tr(t)); }
  function badge(text, kind) { return h('span', { class: 'badge ' + (kind || '') }, text); }
  // rtt renders a round-trip time in ms, coloured by how good it is; 0/undefined = not measured.
  function rtt(ms) {
    ms = Number(ms || 0);
    if (!ms) return h('span', { class: 'muted' }, '—');
    const kind = ms < 80 ? 'ok' : ms < 250 ? 'warn' : 'bad';
    return h('span', { class: 'mono ' + kind }, num(ms) + ' ms');
  }

  // ---- feedback ----
  function toast(msg, kind) {
    let box = document.querySelector('.toasts');
    if (!box) { box = h('div', { class: 'toasts' }); document.body.appendChild(box); }
    const t = h('div', { class: 'toast ' + (kind || '') }, msg);
    box.appendChild(t);
    setTimeout(() => t.remove(), kind === 'bad' ? 6000 : 3500);
  }
  function fail(e) { toast(e && e.message ? e.message : String(e), 'bad'); }

  // modal({title, body, okText, danger, cancel}) → Promise<boolean>
  function modal(opts) {
    return new Promise(resolve => {
      const back = h('div', { class: 'modal-back' });
      const close = v => { back.remove(); document.removeEventListener('keydown', onKey); resolve(v); };
      const onKey = e => { if (e.key === 'Escape') close(false); };
      const ok = h('button', { class: 'primary' + (opts.danger ? ' danger' : ''), onclick: () => close(true) }, opts.okText || tr('确定'));
      const actions = h('div', { class: 'actions' },
        opts.cancel === false ? null : h('button', { onclick: () => close(false) }, tr('取消')), ok);
      const box = h('div', { class: 'modal', role: 'dialog' }, h('h3', null, opts.title || ''), opts.body, actions);
      back.appendChild(box);
      back.addEventListener('click', e => { if (e.target === back) close(false); });
      document.addEventListener('keydown', onKey);
      document.body.appendChild(back);
      const first = box.querySelector('input, select, textarea');
      (first || ok).focus();
      box.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target.tagName === 'INPUT') { e.preventDefault(); close(true); } });
    });
  }
  function confirm(title, text, okText, danger) {
    return modal({ title, body: h('p', null, text), okText, danger: danger !== false });
  }
  async function copy(text) {
    try { await navigator.clipboard.writeText(text); toast(tr('已复制')); }
    catch (e) { toast(tr('复制失败，请手动选择复制'), 'bad'); }
  }
  function secret(text) {
    return h('div', { class: 'secret' }, h('code', null, text), h('button', { class: 'small', onclick: () => copy(text) }, tr('复制')));
  }

  // ---- tables ----
  // table(columns, rows): columns = [{title, cell(row) → node|string, num?}]
  function table(columns, rows, empty) {
    if (!rows || rows.length === 0) return h('div', { class: 'empty' }, icon('inbox', 30), h('span', null, empty || tr('暂无数据')));
    const thead = h('thead', null, h('tr', null, columns.map(c => h('th', { class: c.num ? 'num' : null }, c.title))));
    const tbody = h('tbody', null, rows.map(r => h('tr', null, columns.map(c => h('td', { class: c.num ? 'num' : null }, c.cell(r))))));
    return h('div', { class: 'table-wrap' }, h('table', null, thead, tbody));
  }
  function kv(pairs) {
    return h('dl', { class: 'kv' }, pairs.map(([k, v]) => [h('dt', null, k), h('dd', null, v)]));
  }
  function field(label, input) { return h('div', { class: 'field' }, h('label', null, label), input); }
  function input(attrs) { return h('input', Object.assign({ autocomplete: 'off' }, attrs)); }

  // ---- listings ----
  const PAGE_SIZE_KEY = 'st_page_size';
  const PAGE_SIZES = [10, 20, 50, 100];
  function storedPageSize() {
    let n = 0;
    try { n = Number(localStorage.getItem(PAGE_SIZE_KEY)); } catch (e) { /* the default, then */ }
    return PAGE_SIZES.includes(n) ? n : (state.info && state.info.default_page_size) || 20;
  }
  // download saves what a signed-in endpoint answers as a file.
  async function download(path, name) {
    const r = await fetch(path, { headers: { 'X-Sharetoken-Web': '1' }, credentials: 'same-origin', cache: 'no-store' });
    if (!r.ok) {
      let msg = 'HTTP ' + r.status;
      try { msg = (await r.json()).error.message || msg; } catch (e) { /* no JSON body */ }
      throw new Error(msg);
    }
    const disposition = r.headers.get('Content-Disposition') || '';
    const named = /filename="([^"]+)"/.exec(disposition);
    const url = URL.createObjectURL(await r.blob());
    const a = h('a', { href: url, download: name || (named ? named[1] : 'download') });
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  }
  // dateRange is two date inputs and presets. range() gives what listings take:
  // unix milliseconds of the first day's start and of the day after the last,
  // in the viewer's time zone.
  function dateRange(onChange) {
    const day = v => { const [y, m, d] = v.split('-').map(Number); return new Date(y, m - 1, d); };
    const iso = d => d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
    const from = input({ type: 'date', class: 'date-from', 'aria-label': tr('开始日期'), title: tr('开始日期') });
    const to = input({ type: 'date', class: 'date-to', 'aria-label': tr('结束日期'), title: tr('结束日期') });
    const preset = h('select', { class: 'date-preset', 'aria-label': tr('时间范围') },
      h('option', { value: '' }, tr('全部时间')), h('option', { value: '1' }, tr('今天')), h('option', { value: '7' }, tr('最近 7 天')), h('option', { value: '30' }, tr('最近 30 天')), h('option', { value: 'custom', hidden: true }, tr('自定义')));
    preset.addEventListener('change', () => {
      if (preset.value === '') { from.value = to.value = ''; }
      else {
        const today = new Date(), first = new Date(today);
        first.setDate(today.getDate() - Number(preset.value) + 1);
        from.value = iso(first); to.value = iso(today);
      }
      onChange();
    });
    for (const el of [from, to]) el.addEventListener('change', () => { preset.value = from.value || to.value ? 'custom' : ''; onChange(); });
    return {
      el: h('span', { class: 'date-range' }, preset, from, h('span', { class: 'muted' }, '–'), to),
      range: () => {
        const r = {};
        if (from.value) r.from = day(from.value).getTime();
        if (to.value) { const end = day(to.value); end.setDate(end.getDate() + 1); r.to = end.getTime(); }
        return r;
      },
    };
  }
  // pageNumbers: the first, the last and the neighbours of the current page.
  function pageNumbers(page, pages) {
    const out = [];
    for (let p = 1; p <= pages; p++) {
      if (p === 1 || p === pages || Math.abs(p - page) <= 2) out.push(p);
      else if (out[out.length - 1] !== 0) out.push(0);
    }
    return out;
  }
  // listView draws a paged table with its date filter and CSV export.
  // {path, key, columns, empty, params: () => ({...}), dates, csv, tools, actions, onData, onError, size}
  // → {el, reload, tools}. reload() goes back to the first page; refresh() stays.
  function listView(o) {
    let page = 1, seq = 0, size = o.size || storedPageSize();
    const body = h('div', { class: 'list-body' }), pager = h('div', { class: 'pager' });
    const dates = o.dates === false ? null : dateRange(() => reload());
    const query = extra => {
      const q = new URLSearchParams();
      const all = Object.assign({}, o.params ? o.params() : null, dates ? dates.range() : null, extra);
      for (const [k, v] of Object.entries(all)) if (v !== '' && v !== null && v !== undefined && v !== false) q.set(k, v);
      return q.toString();
    };
    const exportBtn = o.csv === false ? null : h('button', { type: 'button', class: 'small export-csv', onclick: async () => {
      exportBtn.disabled = true;
      const label = exportBtn.textContent;
      exportBtn.textContent = tr('正在导出…');
      try { await download(o.path + '?' + query({ format: 'csv' })); }
      catch (ex) { fail(ex); }
      finally { exportBtn.disabled = false; exportBtn.textContent = label; }
    } }, tr('导出 CSV'));
    function drawPager(total) {
      clear(pager);
      const pages = Math.max(1, Math.ceil(total / size));
      const go = p => { page = Math.min(Math.max(1, p), pages); load(); };
      const jump = input({ type: 'number', min: 1, max: pages, class: 'pager-jump', 'aria-label': tr('跳转到页码') });
      jump.addEventListener('keydown', e => {
        if (e.key !== 'Enter') return;
        e.preventDefault();
        const n = Math.trunc(Number(jump.value));
        if (n >= 1) go(n);
      });
      const sizes = h('select', { class: 'pager-size', 'aria-label': tr('每页条数'), onchange: () => {
        size = Number(sizes.value); page = 1;
        try { localStorage.setItem(PAGE_SIZE_KEY, String(size)); } catch (e) { /* this visit only */ }
        load();
      } }, PAGE_SIZES.map(n => h('option', { value: n, selected: n === size }, tr('{n} 条/页', { n }))));
      add(pager, h('span', { class: 'muted pager-total' }, tr('共 {n} 条', { n: num(total) })),
        h('span', { class: 'spacer' }), sizes,
        h('button', { type: 'button', class: 'small pager-prev', disabled: page <= 1, 'aria-label': tr('上一页'), onclick: () => go(page - 1) }, '‹'),
        pageNumbers(page, pages).map(p => p === 0 ? h('span', { class: 'muted pager-gap' }, '…')
          : h('button', { type: 'button', class: 'small pager-num' + (p === page ? ' active' : ''), 'aria-current': p === page ? 'page' : null, onclick: () => go(p) }, String(p))),
        h('button', { type: 'button', class: 'small pager-next', disabled: page >= pages, 'aria-label': tr('下一页'), onclick: () => go(page + 1) }, '›'),
        pages > 7 ? h('label', { class: 'muted pager-go' }, tr('跳至'), jump) : null);
    }
    async function load() {
      const mine = ++seq;
      let r;
      try { r = await api('GET', o.path + '?' + query({ page, page_size: size })); }
      catch (ex) {
        if (mine !== seq) return;
        if (ex.status === 401) { location.reload(); return; }
        add(clear(body), h('div', { class: 'empty' }, o.onError ? o.onError(ex) : ex.message)); clear(pager);
        return;
      }
      if (mine !== seq) return; // a newer filter's answer is on its way
      if (o.onData) o.onData(r);
      const rows = r[o.key] || [], total = r.total || 0;
      if (!rows.length && total > 0 && page > 1) { page = Math.ceil(total / size) || 1; return load(); } // the list shrank
      add(clear(body), table(typeof o.columns === 'function' ? o.columns() : o.columns, rows, o.empty));
      drawPager(total);
    }
    function reload() { page = 1; return load(); }
    const tools = h('div', { class: 'toolbar list-tools' }, o.tools, dates ? dates.el : null, exportBtn || o.actions ? h('span', { class: 'spacer' }) : null, exportBtn, o.actions);
    return { el: h('div', { class: 'list-view' }, tools, h('div', { class: 'card' }, body, pager)), reload, refresh: load };
  }

  // ---- icons and page parts ----
  function icon(name, size) { return I18N.svg(ICONS[name], size); }
  // chip is an icon on a tinted square; tone: ok, warn, bad, blue, violet, or none for the accent.
  function chip(name, tone, size) { return h('span', { class: 'chip ' + (tone || '') }, icon(name, size || 22)); }
  // pageTitle is a page's h1, carrying the icon of the module it belongs to.
  function pageChip() { return chip(state.routeIcon || 'grid'); }
  function pageTitle(...children) { return h('h1', { class: 'page-title' }, pageChip(), h('span', { class: 'page-title-text' }, children)); }
  function section(name, ...children) { return h('h2', { class: 'section-title' }, icon(name, 18), children); }
  // tile is a number on a card. cls colours the value (ok, warn, bad) and the icon with it; small shrinks a long value.
  function tile(label, value, sub, name, tone, cls) {
    return h('div', { class: 'tile' }, chip(name, cls && cls !== 'small' ? cls : tone),
      h('div', { class: 'tile-body' }, h('div', { class: 'label' }, label), h('div', { class: 'value ' + (cls || '') }, value), sub ? h('div', { class: 'sub' }, sub) : null));
  }
  // themeButton offers the other theme; the stylesheet shows the half that applies.
  function themeButton(cls) {
    return h('button', { type: 'button', class: cls + ' theme-toggle', id: 'theme-toggle', onclick: THEME.toggle },
      h('span', { class: 'for-dark' }, icon('sun', 20), h('span', { class: 'nav-label' }, tr('浅色模式'))),
      h('span', { class: 'for-light' }, icon('moon', 20), h('span', { class: 'nav-label' }, tr('深色模式'))));
  }
  // popover wires a button to a menu that closes on Escape and on any click elsewhere.
  function popover(wrap, btn, menu) {
    const close = () => { menu.hidden = true; btn.setAttribute('aria-expanded', 'false'); document.removeEventListener('click', outside); };
    const outside = e => { if (!wrap.contains(e.target)) close(); };
    btn.setAttribute('aria-expanded', 'false');
    btn.setAttribute('aria-haspopup', 'menu');
    menu.hidden = true;
    btn.addEventListener('click', () => {
      if (!menu.hidden) { close(); return; }
      menu.hidden = false; btn.setAttribute('aria-expanded', 'true'); document.addEventListener('click', outside);
    });
    wrap.addEventListener('keydown', e => { if (e.key === 'Escape' && !menu.hidden) { close(); btn.focus(); } });
    menu.addEventListener('click', close);
  }

  // shell draws the frame of a signed-in page and returns its navigation and main area:
  // {title, items: [{route, icon, label, badge}], links: [{href, icon, label}], extra: node, role, menu: [{href, icon, label}]}
  let onRoute = null;
  const SIDEBAR_KEY = 'st_sidebar';
  function shell(root, o) {
    const box = h('div', { class: 'shell' });
    const stored = () => { try { return localStorage.getItem(SIDEBAR_KEY) === 'collapsed'; } catch (e) { return false; } };
    box.classList.toggle('collapsed', stored());
    const closeDrawer = () => box.classList.remove('nav-open');
    const nav = h('nav', { class: 'sidenav' }, o.items.map(it => h('a', { href: '#/' + it.route, 'data-icon': it.icon, title: it.label },
      icon(it.icon, 20), h('span', { class: 'nav-label' }, it.label), it.badge)));
    nav.addEventListener('click', e => { if (e.target.closest('a')) closeDrawer(); });
    const collapse = h('button', { type: 'button', class: 'side-btn side-collapse', id: 'side-collapse', onclick: () => {
      const on = box.classList.toggle('collapsed');
      try { localStorage.setItem(SIDEBAR_KEY, on ? 'collapsed' : 'open'); } catch (e) { /* this visit only */ }
    } }, h('span', { class: 'when-open' }, icon('collapse', 20)), h('span', { class: 'when-collapsed' }, icon('expand', 20)), h('span', { class: 'nav-label' }, tr('收起侧边栏')));
    const sidebar = h('aside', { class: 'sidebar' },
      h('div', { class: 'side-head' }, h('img', { class: 'side-logo', src: '/static/favicon.svg', alt: '' }),
        h('div', { class: 'side-brand' }, h('b', null, 'ShareToken'), h('small', null, o.title))),
      nav, h('div', { class: 'side-foot' }, themeButton('side-btn'), collapse));

    const place = h('b');
    const crumb = h('div', { class: 'crumb' }, h('span', null, o.title), h('i', null, '/'), place);
    onRoute = (link, name) => { state.routeIcon = link ? link.dataset.icon : null; place.textContent = link ? link.textContent : name; closeDrawer(); };

    const username = state.me.user.username;
    const userBtn = h('button', { type: 'button', class: 'user-btn', id: 'user-menu', title: username },
      h('span', { class: 'avatar', 'aria-hidden': 'true' }, Array.from(username)[0].toUpperCase()),
      h('span', { class: 'user-text' }, h('span', { class: 'who' }, username), h('span', { class: 'user-role' }, o.role)), icon('chevronDown', 14));
    const drop = h('div', { class: 'drop', role: 'menu' },
      (o.menu || []).map(m => h('a', { href: m.href, role: 'menuitem' }, icon(m.icon, 18), m.label)),
      h('button', { type: 'button', role: 'menuitem', class: 'drop-danger', onclick: logout }, icon('logout', 18), h('span', null, tr('退出'))));
    const userMenu = h('div', { class: 'user-menu' }, userBtn, drop);
    popover(userMenu, userBtn, drop);

    const topbar = h('header', { class: 'topbar' },
      h('button', { type: 'button', class: 'icon-btn menu-btn', id: 'menu-btn', 'aria-label': tr('打开菜单'), onclick: () => box.classList.toggle('nav-open') }, icon('menu', 22)),
      crumb, h('span', { class: 'spacer' }),
      (o.links || []).map(l => h('a', { class: 'top-link', href: l.href, title: l.label }, icon(l.icon, 18), h('span', null, l.label))),
      I18N.picker(), o.extra, userMenu);
    const main = h('main');
    add(clear(root), add(box, sidebar, h('div', { class: 'backdrop', onclick: closeDrawer }), h('div', { class: 'frame' }, topbar, main)));
    return { nav, main };
  }

  // ---- session / routing ----
  const state = { me: null, info: null };
  async function loadInfo() { if (!state.info) state.info = await api('GET', '/api/v1/info'); return state.info; }
  async function loadMe() {
    try { state.me = await api('GET', '/api/v1/me'); return state.me; }
    catch (e) { if (e.status === 401 || e.status === 403) { state.me = null; return null; } throw e; }
  }
  async function logout() {
    try { await api('POST', '/api/v1/web/logout'); } catch (e) { /* already out */ }
    state.me = null; location.hash = ''; location.reload();
  }

  // authFrame centres a card on the sign-in background, with the logo above it.
  function authFrame(card) {
    return h('div', { class: 'auth' }, h('div', { class: 'auth-tools' }, I18N.picker(), themeButton('icon-btn')),
      h('img', { class: 'auth-logo', src: '/static/favicon.svg', alt: '' }), card);
  }
  const withIcon = (name, el) => h('span', { class: 'input-icon' }, icon(name, 18), el);

  // renderLogin(root, {title, allowRegister}) → resolves when signed in
  function renderLogin(root, opts) {
    return new Promise(resolve => {
      let mode = 'login';
      const err = h('div', { class: 'error' });
      const user = input({ type: 'text', id: 'login-user', autocomplete: 'username', placeholder: tr('请输入你的邮箱'), required: true });
      const pass = input({ type: 'password', id: 'login-pass', autocomplete: 'current-password', placeholder: tr('至少 8 位'), required: true });
      const submit = h('button', { class: 'primary', type: 'submit' }, tr('登录'));
      const verify = () => state.info && state.info.email_verification;
      const mailCode = input({ type: 'text', id: 'login-code', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: 7, placeholder: tr('6 位验证码') });
      const sendCode = h('button', { type: 'button', id: 'send-code', class: 'small', onclick: requestCode }, tr('发送验证码'));
      const codeField = h('div', { class: 'field code-field', hidden: true }, h('label', null, tr('邮箱验证码')), h('div', { class: 'row' }, mailCode, sendCode));
      const invite = input({ type: 'text', id: 'login-invite', autocomplete: 'off', maxlength: 64, placeholder: tr('管理员发给你的邀请码') });
      const inviteField = h('div', { class: 'field', hidden: true }, h('label', null, tr('邀请码')), invite);
      const inviting = () => state.info && state.info.invite_required;
      let cooldown = null;
      async function requestCode() {
        err.textContent = '';
        const email = user.value.trim();
        if (!email.includes('@')) { err.textContent = tr('请先填写邮箱'); user.focus(); return; }
        sendCode.disabled = true;
        try {
          const r = await api('POST', '/api/v1/register/code', { email, invite_code: invite.value.trim() });
          toast(tr('验证码已发送，请查收邮件（也看看垃圾邮件）'));
          startCooldown(r.cooldown_seconds || 60);
          mailCode.focus();
        } catch (ex) {
          sendCode.disabled = false;
          err.textContent = ex.code === 'code_cooldown' ? tr('刚发送过，请稍后再试') : ex.code === 'throttled' ? tr('请求太频繁，请稍后再试') : ex.code === 'bad_request' ? tr('邮箱地址格式不正确') : signupError(ex) || ex.message;
        }
      }
      function startCooldown(seconds) {
        clearInterval(cooldown);
        let left = seconds;
        const show = () => { sendCode.textContent = tr('{n} 秒后可重发', { n: left }); };
        show();
        cooldown = setInterval(() => {
          if (--left > 0) { show(); return; }
          clearInterval(cooldown); sendCode.disabled = false; sendCode.textContent = tr('重新发送');
        }, 1000);
      }
      const signupError = ex => ex.code === 'exists' ? tr('这个邮箱已经注册过了，请直接登录') : ex.code === 'email_not_allowed' ? tr('这个邮箱不能在此注册') : ex.code === 'invite_required' ? tr('注册需要邀请码') : ex.code === 'bad_invite' ? tr('邀请码错误、已用完或已过期') : '';
      const forgot = h('p', { class: 'muted', hidden: true }, h('a', { href: '#', id: 'forgot-link', onclick: e => { e.preventDefault(); showForgot(); } }, tr('忘记密码？')));
      const tabLogin = h('button', { type: 'button', class: 'active', onclick: () => setMode('login') }, tr('登录'));
      const tabReg = h('button', { type: 'button', onclick: () => setMode('register') }, tr('注册'));
      const tabs = h('div', { class: 'tabs' }, tabLogin, opts.allowRegister ? tabReg : null);
      const hint = h('p', { class: 'muted' });
      const userLabel = h('label', null, tr('用户名'));
      function setMode(m) {
        mode = m; err.textContent = '';
        tabLogin.classList.toggle('active', m === 'login'); tabReg.classList.toggle('active', m === 'register');
        submit.textContent = m === 'login' ? tr('登录') : tr('注册');
        codeField.hidden = !(m === 'register' && verify());
        inviteField.hidden = !(m === 'register' && inviting());
        forgot.hidden = !(m === 'login' && state.info && state.info.password_reset);
        userLabel.textContent = m === 'register' && verify() ? tr('邮箱') : tr('用户名');
        pass.autocomplete = m === 'login' ? 'current-password' : 'new-password';
        hint.textContent = m === 'register' && state.info && state.info.signup_credit_micros > 0
          ? tr('注册即赠送 {n} credit', { n: credit(state.info.signup_credit_micros) }) : '';
      }
      const codeError = ex => ex.code === 'bad_code' ? tr('验证码错误') : ex.code === 'throttled' ? tr('尝试次数过多，请几分钟后再试') : ex.message;
      const restart = () => renderLogin(root, opts).then(resolve);
      const codeInput = () => input({ type: 'text', id: 'mfa-code', inputmode: 'numeric', autocomplete: 'one-time-code', pattern: '[0-9 ]*', maxlength: 7, placeholder: tr('6 位验证码'), required: true });
      // A forgotten password: the answer is the same whether the address has an account.
      function showForgot() {
        const email = input({ type: 'email', id: 'forgot-email', autocomplete: 'email', required: true, value: user.value.includes('@') ? user.value.trim() : '' });
        const e2 = h('div', { class: 'error' });
        const send = h('button', { class: 'primary', type: 'submit', id: 'forgot-send' }, tr('发送重置链接'));
        const form2 = h('form', {
          class: 'form', onsubmit: async e => {
            e.preventDefault(); e2.textContent = ''; send.disabled = true;
            try {
              await api('POST', '/api/v1/password/forgot', { email: email.value.trim() });
              clear(card2); add(card2, h('h1', null, tr('查看你的邮箱')), h('p', { id: 'forgot-sent' }, tr('如果 {email} 有账户，重置链接已经发出，30 分钟内有效。也看看垃圾邮件。', { email: email.value.trim() })),
                h('p', { class: 'muted' }, tr('用户名不是邮箱的账户收不到链接，请联系管理员重置。')), h('div', { class: 'row' }, h('button', { type: 'button', onclick: restart }, tr('返回登录'))));
            } catch (ex) { send.disabled = false; e2.textContent = ex.code === 'throttled' ? tr('请求太频繁，请稍后再试') : ex.message; }
          }
        }, h('p', { class: 'muted' }, tr('填写注册时用的邮箱，我们把重置链接发给你。')), field(tr('邮箱'), email), e2,
          h('div', { class: 'row' }, send, h('button', { type: 'button', onclick: restart }, tr('返回'))));
        const card2 = h('div', { class: 'card login' }, h('h1', null, tr('找回密码')), form2);
        clear(root).appendChild(authFrame(card2));
        email.focus();
      }
      // Second step of an admin login: a code from the authenticator app.
      function showCode(challenge) {
        const code = codeInput(), e2 = h('div', { class: 'error' });
        const form2 = h('form', {
          class: 'form', onsubmit: async e => {
            e.preventDefault(); e2.textContent = '';
            try { await api('POST', '/api/v1/web/mfa', { challenge, code: code.value }); resolve(); }
            catch (ex) { if (ex.code === 'challenge_expired') { toast(tr('验证已超时，请重新登录'), 'bad'); restart(); return; } e2.textContent = codeError(ex); }
          }
        }, h('p', { class: 'muted' }, tr('管理员账号需要两步验证，请输入验证器 App 显示的 6 位验证码。')), field(tr('验证码'), code), e2,
          h('div', { class: 'row' }, h('button', { class: 'primary', type: 'submit' }, tr('验证')), h('button', { type: 'button', onclick: restart }, tr('返回'))));
        clear(root).appendChild(authFrame(h('div', { class: 'card login' }, h('h1', null, tr('两步验证')), form2)));
        code.focus();
      }
      // First admin login: enroll an authenticator before any session exists.
      async function showSetup(challenge) {
        let setup;
        try { setup = await api('POST', '/api/v1/web/mfa/setup', { challenge }); }
        catch (ex) { fail(ex); restart(); return; }
        const code = codeInput(), e2 = h('div', { class: 'error' });
        const form2 = h('form', {
          class: 'form', onsubmit: async e => {
            e.preventDefault(); e2.textContent = '';
            try { await api('POST', '/api/v1/web/mfa/confirm', { challenge, code: code.value }); toast(tr('两步验证已启用'), 'ok'); resolve(); }
            catch (ex) { if (ex.code === 'challenge_expired') { toast(tr('设置已超时，请重新登录'), 'bad'); restart(); return; } e2.textContent = codeError(ex); }
          }
        }, h('p', { class: 'muted' }, tr('管理员必须启用两步验证。用 Google Authenticator、Microsoft Authenticator、1Password 等扫描二维码，或手动输入密钥，然后填写 App 显示的 6 位验证码完成绑定。')),
          h('div', { class: 'qr' }, h('img', { src: setup.qr, alt: tr('两步验证二维码'), width: 220, height: 220 })),
          h('label', null, tr('密钥（手动输入时使用）')),
          h('div', { class: 'secret' }, h('code', { id: 'mfa-secret' }, setup.secret), h('button', { class: 'small', type: 'button', onclick: () => copy(setup.secret) }, tr('复制'))),
          field(tr('验证码'), code), e2,
          h('div', { class: 'row' }, h('button', { class: 'primary', type: 'submit' }, tr('完成绑定')), h('button', { type: 'button', onclick: restart }, tr('取消'))));
        clear(root).appendChild(authFrame(h('div', { class: 'card login' }, h('h1', null, tr('启用两步验证')), form2)));
        code.focus();
      }
      const form = h('form', {
        class: 'form', onsubmit: async e => {
          e.preventDefault(); err.textContent = ''; submit.disabled = true;
          try {
            const body = { username: user.value.trim(), password: pass.value };
            if (mode === 'register' && inviting()) {
              if (!invite.value.trim()) { err.textContent = tr('注册需要邀请码'); invite.focus(); return; }
              body.invite_code = invite.value.trim();
            }
            if (mode === 'register' && verify()) {
              if (!mailCode.value.trim()) { err.textContent = tr('请填写邮箱收到的验证码'); mailCode.focus(); return; }
              body.code = mailCode.value.trim();
            }
            await api('POST', mode === 'login' ? '/api/v1/web/login' : '/api/v1/web/register', body);
            pass.value = '';
            resolve();
          } catch (ex) {
            if (ex.code === 'mfa_required' && ex.data.challenge) { pass.value = ''; showCode(ex.data.challenge); return; }
            if (ex.code === 'mfa_setup_required' && ex.data.challenge) { pass.value = ''; showSetup(ex.data.challenge); return; }
            err.textContent = ex.code === 'bad_credentials' ? tr('用户名或密码错误') : ex.code === 'throttled' ? tr('尝试次数过多，请几分钟后再试')
              : ex.code === 'bad_code' ? tr('验证码错误或已过期') : ex.code === 'email_required' ? tr('请用邮箱注册')
              : ex.code === 'exists' && !verify() ? tr('用户名已被使用') : ex.code === 'cross_site' ? tr('请求被拒绝（跨站）') : signupError(ex) || ex.message;
          } finally { submit.disabled = false; }
        }
      }, h('div', { class: 'field' }, userLabel, withIcon('user', user)), inviteField, codeField, field(tr('密码'), withIcon('lock', pass)), err, submit);
      clear(root).appendChild(authFrame(h('div', { class: 'card login' }, h('h1', null, opts.title || 'ShareToken'), tabs, form, forgot, hint,
        h('p', { class: 'muted' }, h('a', { href: '/' }, tr('← 返回官网'))))));
      setMode('login');
      if (opts.allowRegister && location.hash === '#register') setMode('register');
      user.focus();
    });
  }

  // resetToken takes the token of a password-reset link out of the address, so
  // that it stays neither in the address bar nor in the browser's history.
  function resetToken() {
    const m = /^#reset=([A-Za-z0-9_-]+)$/.exec(location.hash);
    if (!m) return '';
    history.replaceState(null, '', location.pathname + location.search);
    return m[1];
  }
  // renderReset asks for the new password of a reset link; it resolves when it is
  // done or given up on, and the sign-in page comes next.
  function renderReset(root, token) {
    return new Promise(resolve => {
      const p1 = input({ type: 'password', id: 'reset-pass', autocomplete: 'new-password', minlength: 8, maxlength: 256, required: true, placeholder: tr('8-256 位') });
      const p2 = input({ type: 'password', id: 'reset-pass2', autocomplete: 'new-password', required: true });
      const err = h('div', { class: 'error', id: 'reset-error' });
      const form = h('form', {
        class: 'form', id: 'reset-form', onsubmit: async e => {
          e.preventDefault(); err.textContent = '';
          if (p1.value !== p2.value) { err.textContent = tr('两次输入的新密码不一致'); return; }
          try {
            await api('POST', '/api/v1/password/reset', { token, new_password: p1.value });
            toast(tr('密码已重置，请用新密码登录'), 'ok');
            resolve();
          } catch (ex) { err.textContent = ex.code === 'bad_reset_link' ? tr('这个链接无效、已使用或已过期，请重新获取') : ex.code === 'throttled' ? tr('尝试次数过多，请几分钟后再试') : ex.message; }
        }
      }, h('p', { class: 'muted' }, tr('设置新密码后，这个账户所有设备和浏览器都会退出登录。')), field(tr('新密码'), p1), field(tr('重复新密码'), p2), err,
        h('div', { class: 'row' }, h('button', { class: 'primary', type: 'submit' }, tr('重置密码')), h('button', { type: 'button', onclick: () => resolve() }, tr('返回登录'))));
      clear(root).appendChild(authFrame(h('div', { class: 'card login' }, h('h1', null, tr('设置新密码')), form)));
      p1.focus();
    });
  }

  // Hash router: routes = {name: async (main) => void}
  function router(routes, nav, main, fallback) {
    async function go() {
      const asked = location.hash.replace(/^#\/?/, '').split('/')[0];
      const name = Object.hasOwn(routes, asked) ? asked : fallback; // #register, left over from signing up, shows the first page
      const fn = routes[name];
      for (const a of nav.querySelectorAll('a')) a.classList.toggle('active', a.getAttribute('href') === '#/' + name);
      if (onRoute) onRoute(nav.querySelector('a.active'), name);
      clear(main);
      try { await fn(main, location.hash.replace(/^#\/?/, '').split('/').slice(1)); }
      catch (e) {
        if (e.status === 401) { location.reload(); return; }
        if (e.code === 'mfa_required') { toast(tr('管理功能需要重新登录并完成两步验证'), 'bad'); await logout(); return; }
        main.appendChild(h('div', { class: 'card bad' }, tr('加载失败：{error}', { error: e.message })));
      }
    }
    window.addEventListener('hashchange', go);
    return go;
  }
  function param(hash) { return hash; }

  return { icon, chip, pageChip, pageTitle, section, tile, shell, authFrame, api, ApiError, h, clear, add, $, credit, signed, num, time, ago, short, peer, status, badge, rtt, toast, fail, modal, confirm, copy, secret,
    table, kv, field, input, state, listView, dateRange, download, resetToken, renderReset, loadInfo, loadMe, logout, renderLogin, router, param, STATUS };
})();
