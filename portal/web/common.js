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
        h('div', { class: 'side-brand' }, h('b', null, 'sharetoken'), h('small', null, o.title))),
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
      const user = input({ type: 'text', id: 'login-user', autocomplete: 'username', placeholder: tr('邮箱，或 3-32 位小写字母、数字、_ . -'), required: true });
      const pass = input({ type: 'password', id: 'login-pass', autocomplete: 'current-password', placeholder: tr('至少 8 位'), required: true });
      const submit = h('button', { class: 'primary', type: 'submit' }, tr('登录'));
      const verify = () => state.info && state.info.email_verification;
      const mailCode = input({ type: 'text', id: 'login-code', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: 7, placeholder: tr('6 位验证码') });
      const sendCode = h('button', { type: 'button', id: 'send-code', class: 'small', onclick: requestCode }, tr('发送验证码'));
      const codeField = h('div', { class: 'field code-field', hidden: true }, h('label', null, tr('邮箱验证码')), h('div', { class: 'row' }, mailCode, sendCode));
      let cooldown = null;
      async function requestCode() {
        err.textContent = '';
        const email = user.value.trim();
        if (!email.includes('@')) { err.textContent = tr('请先填写邮箱'); user.focus(); return; }
        sendCode.disabled = true;
        try {
          const r = await api('POST', '/api/v1/register/code', { email });
          toast(tr('验证码已发送，请查收邮件（也看看垃圾邮件）'));
          startCooldown(r.cooldown_seconds || 60);
          mailCode.focus();
        } catch (ex) {
          sendCode.disabled = false;
          err.textContent = ex.code === 'code_cooldown' ? tr('刚发送过，请稍后再试') : ex.code === 'throttled' ? tr('请求太频繁，请稍后再试') : ex.code === 'bad_request' ? tr('邮箱地址格式不正确') : ex.message;
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
        userLabel.textContent = m === 'register' && verify() ? tr('邮箱') : tr('用户名');
        pass.autocomplete = m === 'login' ? 'current-password' : 'new-password';
        hint.textContent = m === 'register' && state.info && state.info.signup_credit_micros > 0
          ? tr('注册即赠送 {n} credit', { n: credit(state.info.signup_credit_micros) }) : '';
      }
      const codeError = ex => ex.code === 'bad_code' ? tr('验证码错误') : ex.code === 'throttled' ? tr('尝试次数过多，请几分钟后再试') : ex.message;
      const restart = () => renderLogin(root, opts).then(resolve);
      const codeInput = () => input({ type: 'text', id: 'mfa-code', inputmode: 'numeric', autocomplete: 'one-time-code', pattern: '[0-9 ]*', maxlength: 7, placeholder: tr('6 位验证码'), required: true });
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
              : ex.code === 'exists' ? tr('用户名已被使用') : ex.code === 'cross_site' ? tr('请求被拒绝（跨站）') : ex.message;
          } finally { submit.disabled = false; }
        }
      }, h('div', { class: 'field' }, userLabel, withIcon('user', user)), codeField, field(tr('密码'), withIcon('lock', pass)), err, submit);
      clear(root).appendChild(authFrame(h('div', { class: 'card login' }, h('h1', null, opts.title || 'sharetoken'), tabs, form, hint,
        h('p', { class: 'muted' }, h('a', { href: '/' }, tr('← 返回官网'))))));
      if (opts.allowRegister && location.hash === '#register') setMode('register');
      user.focus();
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
    table, kv, field, input, state, loadInfo, loadMe, logout, renderLogin, router, param, STATUS };
})();
