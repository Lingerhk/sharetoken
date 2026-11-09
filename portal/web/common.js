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
    if (!rows || rows.length === 0) return h('div', { class: 'empty' }, empty || tr('暂无数据'));
    const thead = h('thead', null, h('tr', null, columns.map(c => h('th', { class: c.num ? 'num' : null }, c.title))));
    const tbody = h('tbody', null, rows.map(r => h('tr', null, columns.map(c => h('td', { class: c.num ? 'num' : null }, c.cell(r))))));
    return h('div', { class: 'table-wrap' }, h('table', null, thead, tbody));
  }
  function kv(pairs) {
    return h('dl', { class: 'kv' }, pairs.map(([k, v]) => [h('dt', null, k), h('dd', null, v)]));
  }
  function field(label, input) { return h('div', { class: 'field' }, h('label', null, label), input); }
  function input(attrs) { return h('input', Object.assign({ autocomplete: 'off' }, attrs)); }

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
        clear(root).appendChild(h('div', { class: 'card login' }, h('h1', null, tr('两步验证')), form2));
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
        clear(root).appendChild(h('div', { class: 'card login' }, h('h1', null, tr('启用两步验证')), form2));
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
      }, h('div', { class: 'field' }, userLabel, user), codeField, field(tr('密码'), pass), err, submit);
      clear(root).appendChild(h('div', { class: 'card login' }, I18N.picker(), h('h1', null, opts.title || 'sharetoken'), tabs, form, hint,
        h('p', { class: 'muted' }, h('a', { href: '/' }, tr('← 返回官网')))));
      if (opts.allowRegister && location.hash === '#register') setMode('register');
      user.focus();
    });
  }

  // Hash router: routes = {name: async (main) => void}
  function router(routes, nav, main, fallback) {
    async function go() {
      const name = (location.hash.replace(/^#\/?/, '').split('/')[0]) || fallback;
      const fn = routes[name] || routes[fallback];
      for (const a of nav.querySelectorAll('a')) a.classList.toggle('active', a.getAttribute('href') === '#/' + name);
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

  return { api, ApiError, h, clear, add, $, credit, signed, num, time, ago, short, peer, status, badge, rtt, toast, fail, modal, confirm, copy, secret,
    table, kv, field, input, state, loadInfo, loadMe, logout, renderLogin, router, param, STATUS };
})();
