// The desktop app's page, served by `sharetoken daemon`. It only talks to the
// daemon's local API on its own origin, builds the DOM with textContent (no
// HTML from any server is ever parsed), and holds no token: the login stays
// inside the daemon.
'use strict';
I18N.ready.then(() => {
  const tr = I18N.t;

  // ---- helpers ----
  const app = document.getElementById('app');

  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === undefined || v === null || v === false) continue;
      if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (k === 'class') el.className = v;
      else if (k === 'value') el.value = v;
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const kid of kids.flat()) {
      if (kid === undefined || kid === null || kid === false) continue;
      el.append(kid instanceof Node ? kid : String(kid));
    }
    return el;
  }

  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }

  async function api(method, path, body) {
    let resp;
    try {
      resp = await fetch(path, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined });
    } catch (e) {
      throw new ApiError(0, 'daemon_down', tr('后台服务没有响应，请重新打开应用'));
    }
    let data = null;
    try { data = await resp.json(); } catch (e) { /* not JSON */ }
    if (!resp.ok) {
      const err = (data && data.error) || {};
      throw new ApiError(resp.status, err.code || '', err.message || ('HTTP ' + resp.status));
    }
    return data;
  }

  // Messages for the codes the daemon and the portal report.
  const MESSAGES = {
    upgrade_required: tr('这个版本已不再受支持，请升级到最新版本后继续使用'),
    bad_portal: tr('门户地址格式不对'),
    insecure_portal: tr('拒绝使用其他机器上的明文 HTTP 门户：密码会以明文传输。请使用 https:// 地址'),
    portal_unreachable: tr('连不上 sharetoken 服务器，请检查网络后重试'),
    not_a_portal: tr('这个地址不是 sharetoken 门户'),
    pin_required: tr('请先核对并确认门户证书指纹'),
    pin_mismatch: tr('门户证书和确认的指纹不一致，连接可能被拦截。已停止，密码没有发送'),
    bad_credentials: tr('账号或密码错误，或账号已被停用'),
    throttled: tr('尝试次数太多，请几分钟后再试'),
    registration_closed: tr('该门户未开放注册，请联系管理员开通账号'),
    exists: tr('这个邮箱已经注册过了，请直接登录'),
    bad_code: tr('两步验证码不正确'),
    email_required: tr('请用邮箱注册'),
    code_cooldown: tr('刚发送过验证码，请稍后再试'),
    mfa_setup_required: tr('管理员账号需要先在门户网页上完成两步验证设置'),
    already_logged_in: tr('已经登录，请先退出'),
    daemon_down: tr('后台服务没有响应，请重新打开应用'),
  };
  const message = e => MESSAGES[e.code] || (e.code === 'bad_request' ? badRequest(e.message) : e.message || String(e));
  function badRequest(m) {
    if (/email/.test(m)) return tr('邮箱地址格式不正确');
    if (/password/.test(m)) return tr('密码长度需要 8–256 个字符');
    return m;
  }

  const MICRO = 1e6;
  // Micro-credit precision: single requests cost a few micro-credits, and
  // rounding them away would make a balance look unchanged.
  const credit = (m, digits = 6) => (Number(m || 0) / MICRO).toLocaleString(I18N.lang, { minimumFractionDigits: 0, maximumFractionDigits: digits });
  const num = n => Number(n || 0).toLocaleString(I18N.lang);
  const time = nanos => nanos ? new Date(Number(nanos) / 1e6).toLocaleString(I18N.lang, { hour12: false }) : '—';
  const pricePerM = m => credit(m, 6) + ' / M';
  const shortPeer = p => p && p.length > 16 ? p.slice(0, 8) + '…' + p.slice(-6) : (p || '—');

  function table(cols, rows) {
    return h('div', { class: 'scroll' }, h('table', null,
      h('thead', null, h('tr', null, cols.map(c => h('th', { class: c.num ? 'num' : null }, c.title)))),
      h('tbody', null, rows.map(r => h('tr', null, cols.map(c => h('td', { class: c.num ? 'num' : null }, c.get(r))))))));
  }

  function tile(id, label, value, sub) {
    return h('div', { class: 'tile', id }, h('div', { class: 'label' }, label), h('div', { class: 'value' }, value), sub ? h('div', { class: 'sub' }, sub) : null);
  }
  // A time the daemon reports as text (RFC 3339), to the minute.
  const clock = t => t ? new Date(t).toLocaleString(I18N.lang, { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—';

  // The usage-statistics setting; absent from builds that send none.
  function telemetryToggle(t) {
    if (!t || !t.available) return null;
    const box = h('input', { type: 'checkbox', id: 'telemetry-toggle', checked: t.enabled, onchange: async () => {
      box.disabled = true;
      try { t.enabled = (await api('PUT', '/api/v1/telemetry', { enabled: box.checked })).enabled; }
      catch (e) { /* the box returns to the saved value below */ }
      box.checked = t.enabled;
      box.disabled = false;
    } });
    return h('label', { class: 'check small muted', id: 'telemetry' }, box,
      h('span', null, tr('发送匿名使用统计（活跃、登录结果、连接耗时）。不含邮箱、设备 ID、提示词或模型内容。')));
  }

  // ---- sign-in ----

  let poll = null;
  function stopPolling() { closeModal(); if (poll) { clearInterval(poll); poll = null; } }

  // notice: why the user is back here (a login the portal rejected).
  // A dev build must be impossible to mistake for the real thing.
  function markEnv(session) {
    if (session.env !== 'dev' || document.body.classList.contains('env-dev')) return;
    document.body.classList.add('env-dev');
    document.title = 'ShareToken DEV';
    document.body.prepend(h('div', { class: 'env-banner', id: 'env-banner' }, tr('DEV 环境（{portal}）', { portal: session.portal })));
  }

  async function showAuth(session, notice) {
    markEnv(session);
    stopPolling();
    const auth = { probe: null, tab: 'login', totp: false, confirmed: false, busy: false, error: '' };
    const root = h('div', { class: 'auth', id: 'auth' });
    app.replaceChildren(root);

    const account = h('input', { type: 'text', id: 'account', autocomplete: 'username', value: session.user || '', autocapitalize: 'off', spellcheck: 'false' });
    const email = h('input', { type: 'email', id: 'email', autocomplete: 'email', autocapitalize: 'off', spellcheck: 'false' });
    const password = h('input', { type: 'password', id: 'password', autocomplete: 'current-password' });
    const password2 = h('input', { type: 'password', id: 'password2', autocomplete: 'new-password' });
    const mailCode = h('input', { type: 'text', id: 'mail-code', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: '7' });
    const sendCode = h('button', { type: 'button', id: 'send-code', onclick: requestCode }, tr('发送验证码'));
    let cooldown = null;
    async function requestCode() {
      const p = auth.probe;
      auth.error = '';
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.value.trim())) { auth.error = tr('请填写正确的邮箱地址'); render(); return; }
      sendCode.disabled = true;
      try {
        const r = await api('POST', '/api/v1/register/code', { pin: p.pin_required || p.known ? p.fingerprint : '', email: email.value.trim() });
        let left = r.cooldown_seconds || 60;
        sendCode.textContent = tr('{n} 秒后可重发', { n: left });
        clearInterval(cooldown);
        cooldown = setInterval(() => {
          if (--left > 0) { sendCode.textContent = tr('{n} 秒后可重发', { n: left }); return; }
          clearInterval(cooldown); sendCode.disabled = false; sendCode.textContent = tr('重新发送');
        }, 1000);
        mailCode.focus();
      } catch (e) {
        sendCode.disabled = false;
        auth.error = e.code === 'code_cooldown' ? tr('刚发送过，请稍后再试') : message(e);
        render();
      }
    }
    const totp = h('input', { type: 'text', id: 'totp', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: '7' });

    const telemetry = telemetryToggle(session.telemetry);

    function render() {
      root.replaceChildren(...[
        h('h1', null, 'sharetoken'),
        h('p', { class: 'tagline' }, tr('共享和使用模型算力')),
        I18N.picker(),
        notice && h('div', { class: 'notice warn', id: 'notice' }, notice),
        auth.probe ? signInPanel() : connectPanel(),
        telemetry,
      ].filter(Boolean)); // replaceChildren would print a null as text
    }

    function errorLine() {
      return auth.error ? h('div', { class: 'notice bad', id: 'error', role: 'alert' }, auth.error) : null;
    }

    // The portal address is built in: the page only says it is connecting, or
    // why it could not and offers a retry.
    function connectPanel() {
      return h('form', { class: 'panel', id: 'portal-form', novalidate: true, onsubmit: e => { e.preventDefault(); probe(); } },
        auth.busy ? h('p', { class: 'small muted', id: 'connecting' }, tr('正在连接 sharetoken…')) : null,
        errorLine(),
        auth.busy ? null : h('button', { class: 'primary', type: 'submit', id: 'portal-next' }, tr('重试')));
    }

    async function probe() {
      auth.error = '';
      auth.busy = true; render();
      try {
        auth.probe = await api('POST', '/api/v1/portal/probe', {}); // empty: the daemon uses the built-in portal
        auth.confirmed = false;
      } catch (e) { auth.error = message(e); }
      auth.busy = false; render();
      const first = auth.tab === 'register' ? email : (account.value ? password : account);
      if (auth.probe) first.focus();
    }

    function certBlock() {
      const p = auth.probe;
      if (!p.pin_required) return null;
      return h('div', { class: 'notice ' + (p.pin_changed ? 'bad' : 'warn'), id: 'cert' },
        h('div', null, p.pin_changed
          ? tr('门户证书和上次登录时不同。可能是运维更换了证书，也可能是连接被拦截。请向管理员核对新的指纹：')
          : tr('这个门户使用自签名证书。请向管理员核对指纹：')),
        h('div', null, h('code', { id: 'fingerprint' }, p.fingerprint)),
        h('label', { class: 'check' },
          h('input', { type: 'checkbox', id: 'cert-ok', checked: auth.confirmed, onchange: e => { auth.confirmed = e.target.checked; render(); } }),
          h('span', null, tr('指纹一致，信任这个门户'))));
    }

    function signInPanel() {
      const p = auth.probe;
      const canRegister = p.registration_open;
      if (!canRegister && auth.tab === 'register') auth.tab = 'login';
      const blocked = auth.busy || (p.pin_required && !auth.confirmed);
      const isLogin = auth.tab === 'login';
      const tab = (name, label, enabled) => h('button', { type: 'button', role: 'tab', id: 'tab-' + name, 'aria-selected': auth.tab === name ? 'true' : 'false', disabled: !enabled,
        onclick: () => { auth.tab = name; auth.error = ''; auth.totp = false; render(); } }, label);
      password.setAttribute('autocomplete', isLogin ? 'current-password' : 'new-password');
      return h('form', { class: 'panel', id: 'signin-form', novalidate: true, onsubmit: e => { e.preventDefault(); submit(); } },
        h('div', { class: 'tabs', role: 'tablist' }, tab('login', tr('登录'), true), tab('register', tr('注册'), canRegister)),
        certBlock(),
        isLogin
          ? [h('label', { class: 'field' }, h('span', null, tr('邮箱（或用户名）')), account),
             h('label', { class: 'field' }, h('span', null, tr('密码')), password),
             auth.totp ? h('label', { class: 'field' }, h('span', null, tr('两步验证码（6 位）')), totp) : null]
          : [h('label', { class: 'field' }, h('span', null, tr('邮箱')), email),
             p.email_verification ? h('label', { class: 'field' }, h('span', null, tr('邮箱验证码（也看看垃圾邮件）')), h('div', { class: 'code-row' }, mailCode, sendCode)) : null,
             h('label', { class: 'field' }, h('span', null, tr('密码（至少 8 位）')), password),
             h('label', { class: 'field' }, h('span', null, tr('确认密码')), password2),
             p.signup_credit_micros ? h('p', { class: 'small muted', id: 'signup-credit' }, tr('注册赠送 {n} credit', { n: credit(p.signup_credit_micros) })) : null],
        !canRegister ? h('p', { class: 'small muted', id: 'reg-closed' }, tr('该门户未开放注册，账号由管理员开通。')) : null,
        errorLine(),
        h('button', { class: 'primary', type: 'submit', id: 'submit', disabled: blocked },
          auth.busy ? tr('请稍候…') : (isLogin ? tr('登录') : tr('注册并登录'))));
    }

    async function submit() {
      const p = auth.probe;
      auth.error = '';
      const isLogin = auth.tab === 'login';
      if (!isLogin && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.value.trim())) auth.error = tr('请填写正确的邮箱地址');
      else if (isLogin && !account.value.trim()) auth.error = tr('请填写邮箱或用户名');
      else if (!isLogin && p.email_verification && !mailCode.value.trim()) auth.error = tr('请填写邮箱收到的验证码');
      else if (!password.value) auth.error = tr('请填写密码');
      else if (!isLogin && password.value.length < 8) auth.error = tr('密码至少 8 位');
      else if (!isLogin && password.value !== password2.value) auth.error = tr('两次输入的密码不一致');
      else if (isLogin && auth.totp && !totp.value.trim()) auth.error = tr('请填写两步验证码');
      if (auth.error) { render(); return; }
      auth.busy = true; render();
      const body = { pin: p.pin_required || p.known ? p.fingerprint : '', password: password.value };
      if (isLogin) { body.account = account.value.trim(); if (auth.totp) body.totp = totp.value.trim(); }
      else { body.email = email.value.trim(); if (p.email_verification) body.code = mailCode.value.trim(); }
      try {
        const s = await api('POST', isLogin ? '/api/v1/login' : '/api/v1/register', body);
        password.value = password2.value = totp.value = '';
        showDashboard(s);
        return;
      } catch (e) {
        if (e.code === 'mfa_required') { auth.totp = true; auth.error = ''; }
        else if (e.code === 'already_logged_in') { boot(); return; }
        else {
          auth.error = !isLogin && e.code === 'bad_code' ? tr('验证码错误或已过期') : message(e);
          if (e.code === 'pin_mismatch' || e.code === 'pin_required') { auth.probe = null; }
          if (e.code === 'bad_code') totp.value = '';
        }
      }
      auth.busy = false; render();
      if (auth.totp && !totp.value) totp.focus();
    }

    render();
    probe();
  }

  // ---- dashboard ----

  // A section re-renders only when its data changed, so that polling does not
  // disturb a text selection or a scroll position.
  function section(id, title) {
    const body = h('div', { id: id + '-body' });
    let last;
    const el = h('section', { class: 'panel', id }, title ? h('h2', null, title) : null, body);
    return { el, set(key, ...nodes) { const k = JSON.stringify(key); if (k !== last) { last = k; body.replaceChildren(...nodes.flat().filter(Boolean)); } } };
  }

  function copyButton(getText, id = 'copy-endpoint') {
    const b = h('button', { type: 'button', class: 'small', id, onclick: async () => {
      const text = getText();
      try {
        await navigator.clipboard.writeText(text);
      } catch (e) { // webviews may refuse the clipboard API
        const t = h('textarea', { readonly: true, value: text });
        document.body.append(t); t.select();
        try { document.execCommand('copy'); } catch (e2) { /* the address stays selectable */ }
        t.remove();
      }
      b.textContent = tr('已复制');
      setTimeout(() => { b.textContent = tr('复制'); }, 1500);
    } }, tr('复制'));
    return b;
  }

  const STATE = {
    starting: ['warn', tr('正在连接网络…')],
    retrying: ['bad', tr('暂时连不上网络，正在重试')],
    running: ['ok', tr('已连接')],
  };
  const JOB_STATUS = { held: tr('进行中'), pending: tr('待最终确认'), disputed: tr('争议中'), final: tr('已完成'), released: tr('已释放'), refunded: tr('已退款') };

  // Built once: account polling never touches it, so typing is not disturbed.
  function feedbackPanel() {
    const kind = h('select', { id: 'feedback-kind' },
      h('option', { value: 'bug' }, tr('问题')), h('option', { value: 'idea' }, tr('建议')), h('option', { value: 'other' }, tr('其他')));
    const text = h('textarea', { id: 'feedback-text', rows: '4', maxlength: '4000', dir: 'auto' });
    const line = h('div', { id: 'feedback-result', role: 'status' });
    const send = h('button', { class: 'primary', type: 'submit', id: 'feedback-send' }, tr('提交反馈'));
    const say = (cls, msg) => { line.className = 'notice small ' + cls; line.textContent = msg; };
    return h('section', { class: 'panel', id: 'feedback' }, h('h2', null, tr('反馈')),
      h('p', { class: 'small muted' }, tr('告诉我们你遇到的问题或建议。请不要填写密码、token 或 prompt 内容。我们可能会通过账户邮箱回复你。')),
      h('form', { novalidate: true, onsubmit: async e => {
        e.preventDefault();
        if (!text.value.trim()) { say('bad', tr('请填写反馈内容')); text.focus(); return; }
        send.disabled = true;
        try {
          await api('POST', '/api/v1/feedback', { category: kind.value, message: text.value, lang: I18N.lang });
          text.value = '';
          say('ok', tr('反馈已提交，谢谢！'));
        } catch (ex) {
          say('bad', ex.code === 'throttled' ? tr('提交太频繁，请稍后再试') : message(ex));
        } finally { send.disabled = false; }
      } },
        h('label', { class: 'field' }, h('span', null, tr('类型')), kind),
        h('label', { class: 'field' }, h('span', null, tr('内容')), text),
        line,
        send,
        h('p', { class: 'small muted' }, tr('提交时会附带应用版本和操作系统类型。'))));
  }

  // ---- sharing (seller mode) ----

  // A dialog over the page. Polling re-renders the lists underneath and never
  // touches it, so what the user is typing stays. One at a time.
  let closeModal = () => {};
  function modal(id, title, ...body) {
    closeModal();
    const box = h('div', { class: 'modal panel', id, role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      h('div', { class: 'modal-head' }, h('h2', null, title),
        h('button', { type: 'button', class: 'link modal-x', id: id + '-close', 'aria-label': tr('关闭'), onclick: () => closeModal() }, '✕')),
      ...body.flat().filter(Boolean));
    const back = h('div', { class: 'modal-back' }, box);
    const onKey = e => { if (e.key === 'Escape') closeModal(); };
    closeModal = () => { back.remove(); document.removeEventListener('keydown', onKey); closeModal = () => {}; };
    document.addEventListener('keydown', onKey);
    document.body.append(back);
    const first = box.querySelector('input:not([type=checkbox]):not([disabled]), select:not([disabled]), textarea');
    if (first) first.focus();
    return box;
  }
  const field = (label, input, hint) => h('label', { class: 'field' }, h('span', null, label), input, hint ? h('div', { class: 'small muted hint' }, hint) : null);
  function errorBox(id) {
    const el = h('div', { class: 'notice bad', id, role: 'alert', hidden: true });
    return { el, say(msg) { el.textContent = msg || ''; el.hidden = !msg; } };
  }
  // run does one request for a button: the button waits, a failure is explained in err.
  async function run(btn, err, fn) {
    if (btn.disabled) return;
    btn.disabled = true;
    err.say('');
    try { await fn(); }
    catch (e) { err.say(e instanceof ApiError ? shareError(e) : (e.message || String(e))); }
    finally { btn.disabled = false; }
  }
  function shareError(e) {
    if (e.code === 'not_connected' || e.code === 'not_logged_in') return tr('连接网络后才能设置共享');
    if (e.code) return message(e);
    if (e.status === 401) return tr('厂商拒绝了这份凭据：{error}', { error: e.message });
    if (e.status === 409) return tr('这个模型 ID 已被其他来源使用，请换一个 ID');
    if (e.status === 502) return tr('厂商返回了错误：{error}', { error: e.message });
    return e.message || String(e);
  }
  // Prices are kept in micro-credits per million tokens and typed in credits.
  const priceText = m => String(Number(m || 0) / MICRO);
  function parsePrice(s) {
    const v = Number(s.trim());
    return s.trim() !== '' && isFinite(v) && v >= 0 ? Math.round(v * MICRO) : null;
  }
  const DEFAULT_PRICE_IN = 100000, DEFAULT_PRICE_OUT = 200000;

  // The sharing view: whether this device sells, the hosted APIs it sells
  // through with their models, and the models of the local model server.
  // getMarket: the models on the network, for a price hint.
  function shareView(getMarket) {
    let st = null;    // the last /api/v1/status; null while the network is not joined
    let kinds = null; // what can be added; fetched once
    const ui = { busy: false, error: '', flash: null };
    const state = section('share-state', tr('共享状态'));
    const provs = section('providers', tr('厂商 API'));
    const local = section('local-models', tr('本机模型服务'));
    const relays = section('share-relays', tr('中继预约'));
    const soldJobs = section('sold-jobs', tr('最近卖出的作业'));
    const el = h('div', { id: 'view-share', hidden: true }, state.el, provs.el, local.el, relays.el, soldJobs.el);
    let acct = null;  // the account, for what was earned in all
    let sold = null;  // the latest jobs this account served
    relays.el.hidden = true;
    // The lowest offer on the network for a model ID, this device's own included.
    const marketOf = id => getMarket().find(o => o.model === id);
    const marketKey = () => getMarket().map(o => [o.model, o.nodes, o.min_price_in, o.min_price_out]);

    const providersOf = s => s.providers || [];
    // What would be sold with sharing on.
    const sellable = s => (s.models || []).length + providersOf(s).filter(p => p.enabled).reduce((n, p) => n + p.models.length, 0);
    const shareConfig = (s, patch) => Object.assign({ enabled: s.enabled, upstream: s.upstream, upstream_key_env: s.upstream_key_env,
      concurrency: s.concurrency, models: s.models || [], relays: s.relays, tee: s.tee }, patch);
    const providerPath = (name, rest = '') => '/api/v1/providers/' + encodeURIComponent(name) + rest;

    async function reload() {
      try { st = await api('GET', '/api/v1/status'); }
      catch (e) { if (e.code === 'not_connected' || e.code === 'not_logged_in') st = null; }
      render();
    }
    // settle follows a change: sharing with nothing left to sell is turned off,
    // rather than left on and failing.
    async function settle() {
      await reload();
      if (st && st.share.enabled && sellable(st.share) === 0) {
        try { await api('POST', '/api/v1/share/stop'); } catch (e) { /* the state panel says what is wrong */ }
        await reload();
      }
    }
    async function loadKinds() {
      if (!kinds) kinds = (await api('GET', '/api/v1/providers')).kinds || [];
      return kinds;
    }

    function modelTable(source, ms, serving) {
      const act = (cls, label, m, fn, extra) => h('button', { type: 'button', class: 'mini ' + cls + (extra || ''), 'data-model': m.id, onclick: fn }, label);
      return table([
        { title: tr('模型 ID'), get: m => m.id },
        { title: tr('上游模型'), get: m => h('span', { class: 'mono' }, m.upstream_model) },
        { title: tr('输入价'), num: true, get: m => pricePerM(m.price_in) },
        { title: tr('输出价'), num: true, get: m => pricePerM(m.price_out) },
        { title: tr('输出上限'), num: true, get: m => m.max_output ? num(m.max_output) : '—' },
        { title: tr('网络最低价'), num: true, get: m => { const o = marketOf(m.id); return o ? h('span', { class: 'market', title: tr('节点') + ' ' + num(o.nodes) }, credit(o.min_price_in) + ' / ' + credit(o.min_price_out)) : '—'; } },
        { title: tr('状态'), get: m => serving.has((source ? source.name : '') + '\n' + m.id) ? h('span', { class: 'on-sale' }, tr('在售')) : h('span', { class: 'muted' }, tr('未出售')) },
        { title: '', get: m => h('span', { class: 'row-actions' },
          source ? act('model-test', tr('测试'), m, () => testDialog(source, m)) : null,
          act('model-edit', tr('编辑'), m, () => modelDialog(source, m)),
          act('model-remove', tr('移除'), m, () => removeModel(source, m), ' danger')) },
      ], ms);
    }

    function render() {
      if (!st) {
        state.set(['down'], h('div', { class: 'empty', id: 'share-down' }, tr('连接网络后才能设置共享')));
        provs.el.hidden = local.el.hidden = relays.el.hidden = true;
        renderSold();
        return;
      }
      provs.el.hidden = local.el.hidden = false;
      const s = st.share, ps = providersOf(s), n = sellable(s);
      // What the running node sells, by source: an ID two sources claim is sold by one.
      const serving = new Set();
      if (s.running) {
        const taken = new Set();
        const claim = (source, ms) => { for (const m of ms) if (!taken.has(m.id)) { taken.add(m.id); serving.add(source + '\n' + m.id); } };
        claim('', s.models || []);
        for (const p of ps) if (p.enabled) claim(p.name, p.models);
      }

      const stats = s.stats || {};
      const [cls, label] = s.running ? ['ok', tr('正在共享')] : s.enabled ? ['bad', tr('已开启，但没有在运行')] : ['', tr('未开启')];
      state.set([s.enabled, s.running, n, s.concurrency, (s.serving || []).length, stats.jobs_served, stats.jobs_failed, stats.active_jobs, stats.earned_micros, clock(stats.last_announce), stats.announce_error, st.warnings, ui, acct && acct.usage.sold],
        h('div', { class: 'share-line' },
          h('span', { class: 'pill ' + cls, id: 'share-pill' }, label),
          h('span', { class: 'spacer' }),
          h('button', { type: 'button', id: 'share-toggle', class: s.enabled ? '' : 'accent', disabled: ui.busy, onclick: toggle }, s.enabled ? tr('停止共享') : tr('开启共享'))),
        h('p', { class: 'small muted' }, tr('开启后，这台设备用下面配置的模型为网络上的买家服务，收益计入你的账户。')),
        ui.error ? h('div', { class: 'notice bad', id: 'share-error', role: 'alert' }, ui.error) : null,
        !s.enabled && n > 0 ? h('div', { class: 'notice warn', id: 'share-hint' }, tr('已配置 {n} 个模型，开启共享后开始接单。', { n: num(n) })) : null,
        s.running ? h('div', { class: 'tiles', id: 'share-stats' },
          tile('share-tile-models', tr('在售模型'), num((s.serving || []).length)),
          tile('share-tile-jobs', tr('已完成作业'), num(stats.jobs_served), tr('失败：{n}', { n: num(stats.jobs_failed) })),
          tile('share-tile-active', tr('进行中'), num(stats.active_jobs) + ' / ' + num(s.concurrency), tr('同时处理的请求数：{n}', { n: num(s.concurrency) })),
          tile('share-tile-earned', tr('本次运行收益'), credit(stats.earned_micros), stats.last_announce ? tr('最近报价：{time}', { time: clock(stats.last_announce) }) : null),
          acct ? tile('share-tile-total', tr('卖出收益'), credit(acct.usage.sold.micros), tr('{n} 次，{tokens} tokens', { n: num(acct.usage.sold.jobs), tokens: num(acct.usage.sold.prompt_tokens + acct.usage.sold.completion_tokens) })) : null) : null,
        stats.announce_error ? h('div', { class: 'notice warn small' }, tr('向协调器报价失败：{error}', { error: stats.announce_error })) : null,
        (st.warnings || []).map(w => h('div', { class: 'notice warn small share-warning' }, w)));

      provs.set([ps, [...serving], ui.flash, marketKey()],
        h('div', { class: 'share-line' },
          h('p', { class: 'small muted' }, tr('把你在模型厂商开通的 API 或订阅提供给网络。买家的提示词会以明文发给厂商，用量记在你的厂商账户上。')),
          h('button', { type: 'button', id: 'provider-add', onclick: () => providerDialog(null) }, tr('添加厂商'))),
        ui.flash ? h('div', { class: 'notice ' + ui.flash.cls, id: 'provider-flash' }, ui.flash.text) : null,
        ps.length === 0 ? h('div', { class: 'empty', id: 'providers-empty' }, tr('还没有添加厂商')) : ps.map(p => h('div', { class: 'prov', id: 'prov-' + p.name },
          h('div', { class: 'prov-head' },
            h('strong', { class: 'prov-name' }, p.name),
            h('span', { class: 'pill ' + (p.enabled ? 'ok' : '') }, p.enabled ? tr('已启用') : tr('已停用')),
            h('span', { class: 'badge' }, p.type_name),
            h('span', { class: 'muted small prov-cred' }, (p.auth === 'oauth' ? [tr('账号登录'), p.account, p.plan] : ['API key', p.key]).filter(Boolean).join(' ')),
            h('span', { class: 'spacer' }),
            h('span', { class: 'row-actions' },
              h('button', { type: 'button', class: 'mini prov-add-model', onclick: () => modelDialog(p, null) }, tr('添加模型')),
              h('button', { type: 'button', class: 'mini prov-toggle', onclick: e => enableProvider(e.target, p) }, p.enabled ? tr('停用') : tr('启用')),
              h('button', { type: 'button', class: 'mini prov-renew', onclick: () => providerDialog(p) }, tr('更换凭据')),
              h('button', { type: 'button', class: 'mini danger prov-remove', onclick: () => removeProvider(p) }, tr('删除')))),
          h('div', { class: 'kv small prov-meta' },
            h('span', { class: 'mono' }, p.base_url),
            p.token_expires ? h('span', null, tr('令牌有效期至 {time}（会自动续期）', { time: clock(p.token_expires) })) : null,
            p.headers && p.headers.length ? h('span', null, tr('附加请求头：{names}', { names: p.headers.join(', ') })) : null),
          p.models.length === 0 ? h('div', { class: 'empty prov-empty' }, tr('还没有要出售的模型')) : modelTable(p, p.models, serving))));

      const lm = s.models || [];
      local.set([s.upstream, s.concurrency, lm, [...serving], marketKey()],
        h('div', { class: 'share-line' },
          h('p', { class: 'small muted' }, tr('共享这台电脑上的模型服务（如 Ollama），它需要提供 OpenAI 兼容接口。')),
          h('span', { class: 'row-actions' },
            h('button', { type: 'button', id: 'local-add-model', onclick: () => modelDialog(null, null) }, tr('添加模型')),
            h('button', { type: 'button', id: 'local-settings', onclick: localDialog }, tr('设置')))),
        h('div', { class: 'kv small' },
          h('span', null, tr('地址'), ' ', h('code', { id: 'local-upstream' }, s.upstream)),
          h('span', { id: 'local-concurrency' }, tr('同时处理的请求数：{n}', { n: num(s.concurrency) }))),
        lm.length === 0 ? h('div', { class: 'empty', id: 'local-empty' }, tr('还没有要出售的本机模型')) : modelTable(null, lm, serving));
      renderRelays();
      renderSold();
    }

    // The relays buyers reach this device through.
    function renderRelays() {
      const rs = (st && st.share.running && st.share.reservations) || [];
      relays.el.hidden = rs.length === 0;
      const named = a => { const r = (st.relays || []).find(o => o.addr === a); return r && r.name ? r.name : shortPeer(a); };
      relays.set(rs.map(r => [r.addr, r.reserved, r.error, Math.round((r.rtt_ns || 0) / 1e6), clock(r.expires)]), rs.length === 0 ? null : table([
        { title: tr('中继'), get: r => named(r.addr) },
        { title: tr('状态'), get: r => r.reserved ? h('span', { class: 'on-sale' }, tr('已预约')) : h('span', { class: 'muted' }, r.error || tr('未预约')) },
        { title: tr('延迟'), num: true, get: r => r.rtt_ns ? Math.round(r.rtt_ns / 1e6) + ' ms' : '—' },
        { title: tr('到期时间'), get: r => clock(r.expires) },
      ], rs));
    }

    function renderSold() {
      soldJobs.set(sold, !sold || sold.length === 0 ? h('div', { class: 'empty', id: 'sold-empty' }, tr('还没有卖出的作业')) : table([
        { title: tr('时间'), get: j => time(j.created_at) },
        { title: tr('模型'), get: j => j.model },
        { title: tr('状态'), get: j => JOB_STATUS[j.status] || j.status },
        { title: 'Tokens', num: true, get: j => num(j.prompt_tokens + j.completion_tokens) },
        { title: 'Credit', num: true, get: j => '+' + credit(j.earned_micros, 6) },
      ], sold));
    }

    async function toggle() {
      const s = st.share;
      if (!s.enabled && sellable(s) === 0) { ui.error = tr('请先添加至少一个模型，再开启共享'); render(); return; }
      ui.busy = true; ui.error = ''; render();
      try { await api('POST', s.enabled ? '/api/v1/share/stop' : '/api/v1/share/start'); }
      catch (e) { ui.error = shareError(e); }
      ui.busy = false;
      await reload();
    }

    async function enableProvider(btn, p) {
      btn.disabled = true;
      ui.error = '';
      try { await api('PATCH', providerPath(p.name), { enabled: !p.enabled }); }
      catch (e) { ui.error = shareError(e); }
      await settle();
      btn.disabled = false;
    }

    function confirmDialog(text, label, fn) {
      const err = errorBox('confirm-error');
      const okBtn = h('button', { type: 'button', class: 'danger', id: 'confirm-ok', onclick: () => run(okBtn, err, async () => { await fn(); closeModal(); ui.flash = null; await settle(); }) }, label);
      modal('dlg-confirm', tr('请确认'), h('p', { id: 'confirm-text' }, text), err.el,
        h('div', { class: 'modal-actions' }, h('button', { type: 'button', id: 'confirm-cancel', onclick: () => closeModal() }, tr('取消')), okBtn));
    }

    function removeProvider(p) {
      confirmDialog(tr('删除厂商 {name}？它的凭据和要出售的模型会一起删除。', { name: p.name }), tr('删除'), () => api('DELETE', providerPath(p.name)));
    }

    function removeModel(source, m) {
      confirmDialog(tr('不再出售模型 {id}？', { id: m.id }), tr('移除'), () => {
        if (source) return api('DELETE', providerPath(source.name, '/models/' + encodeURIComponent(m.id)));
        const s = st.share, rest = (s.models || []).filter(o => o.id !== m.id);
        // Sharing cannot stay on with nothing to sell: the daemon refuses that.
        const nothingLeft = rest.length + sellable(s) - (s.models || []).length === 0;
        return api('PUT', '/api/v1/share', shareConfig(s, { models: rest, enabled: s.enabled && !nothingLeft }));
      });
    }

    function localDialog() {
      const s = st.share;
      const url = h('input', { type: 'url', id: 'local-url', value: s.upstream, autocapitalize: 'off', spellcheck: 'false' });
      const conc = h('input', { type: 'text', id: 'local-conc', inputmode: 'numeric', value: String(s.concurrency) });
      const err = errorBox('local-error');
      const save = h('button', { class: 'primary', type: 'submit', id: 'local-save' }, tr('保存'));
      modal('dlg-local', tr('本机模型服务'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(save, err, async () => {
        const c = Number(conc.value.trim());
        if (!/^https?:\/\/\S+$/.test(url.value.trim())) throw new Error(tr('地址需要以 http:// 或 https:// 开头'));
        if (!Number.isInteger(c) || c < 1 || c > 64) throw new Error(tr('同时处理的请求数需要是 1 到 64 的整数'));
        await api('PUT', '/api/v1/share', shareConfig(st.share, { upstream: url.value.trim().replace(/\/+$/, ''), concurrency: c }));
        closeModal();
        await reload();
      }); } },
        field(tr('模型服务地址（OpenAI 兼容的 base URL）'), url, tr('Ollama 的默认地址是 http://127.0.0.1:11434/v1')),
        field(tr('同时处理的请求数'), conc, tr('对厂商 API 的模型同样生效。')),
        err.el, save));
    }

    // source: the provider (null: the local model server). old: the model to
    // edit (null: a new one). known: the models the source offers, when the
    // caller already has them.
    function modelDialog(source, old, known) {
      const name = source ? source.name : tr('本机模型服务');
      const pick = h('select', { id: 'md-pick' });
      const upstream = h('input', { type: 'text', id: 'md-upstream', value: old ? old.upstream_model : '', disabled: !!old, autocapitalize: 'off', spellcheck: 'false' });
      const id = h('input', { type: 'text', id: 'md-id', value: old ? old.id : '', disabled: !!old, autocapitalize: 'off', spellcheck: 'false' });
      const priceIn = h('input', { type: 'text', id: 'md-in', inputmode: 'decimal', value: priceText(old ? old.price_in : DEFAULT_PRICE_IN) });
      const priceOut = h('input', { type: 'text', id: 'md-out', inputmode: 'decimal', value: priceText(old ? old.price_out : DEFAULT_PRICE_OUT) });
      const maxOut = h('input', { type: 'text', id: 'md-max', inputmode: 'numeric', value: old && old.max_output ? String(old.max_output) : '' });
      const listLine = h('p', { class: 'small muted', id: 'md-list' });
      const marketLine = h('p', { class: 'small muted', id: 'md-market', hidden: true });
      const upstreamField = field(tr('模型名（厂商或模型服务使用的名字）'), upstream);
      const pickField = field(tr('模型'), pick);
      pickField.hidden = true;
      const err = errorBox('md-error');
      const save = h('button', { class: 'primary', type: 'submit', id: 'md-save' }, old ? tr('保存') : tr('添加'));
      let idTouched = !!old;

      function showMarket() {
        const m = getMarket().find(o => o.model === id.value.trim());
        marketLine.hidden = !m;
        if (m) marketLine.textContent = tr('网络上这个模型的最低价：输入 {in}，输出 {out}（credit / 百万 tokens）', { in: credit(m.min_price_in), out: credit(m.min_price_out) });
      }
      function follow() { if (!idTouched) id.value = upstream.value.trim(); showMarket(); }
      upstream.addEventListener('input', follow);
      id.addEventListener('input', () => { idTouched = true; showMarket(); });
      pick.addEventListener('change', () => {
        upstreamField.hidden = pick.value !== '';
        if (pick.value !== '') { upstream.value = pick.value; follow(); } else { upstream.value = ''; follow(); upstream.focus(); }
      });
      function offer(ms) {
        if (!ms || ms.length === 0) { listLine.textContent = tr('没有拿到模型列表，请手动填写模型名'); return; }
        const sold = new Set((source ? source.models : st.share.models || []).map(m => m.upstream_model));
        pick.replaceChildren(...ms.map(m => h('option', { value: m }, sold.has(m) ? tr('{model}（已在出售）', { model: m }) : m)), h('option', { value: '' }, tr('手动填写…')));
        pick.value = ms.find(m => !sold.has(m)) || ms[0];
        pickField.hidden = false; listLine.hidden = true; upstreamField.hidden = true;
        upstream.value = pick.value; follow();
      }

      const box = modal('dlg-model', old ? tr('编辑模型') : tr('添加模型'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(save, err, async () => {
        const m = { id: id.value.trim() || upstream.value.trim(), upstream_model: upstream.value.trim(), price_in: parsePrice(priceIn.value), price_out: parsePrice(priceOut.value) };
        if (!m.upstream_model) throw new Error(tr('请填写模型名'));
        if (m.price_in === null || m.price_out === null) throw new Error(tr('价格需要是不小于 0 的数字'));
        if (maxOut.value.trim() !== '') {
          m.max_output = Number(maxOut.value.trim());
          if (!Number.isInteger(m.max_output) || m.max_output < 0) throw new Error(tr('输出上限需要是整数'));
        }
        const s = st.share;
        const others = providersOf(s).filter(p => !source || p.name !== source.name).flatMap(p => p.models).concat(source ? s.models || [] : []);
        if (others.some(o => o.id === m.id)) throw new Error(tr('这个模型 ID 已被其他来源使用，请换一个 ID'));
        if (source) await api('PUT', providerPath(source.name, '/models'), m);
        else await api('PUT', '/api/v1/share', shareConfig(s, { models: (s.models || []).filter(o => o.id !== m.id).concat([m]) }));
        closeModal();
        ui.flash = null;
        await reload();
      }); } },
        h('p', { class: 'small muted', id: 'md-source' }, tr('来源：{name}', { name })),
        old ? null : listLine, old ? null : pickField, upstreamField,
        field(tr('网络上的模型 ID'), id, old ? null : tr('买家用这个名字调用，默认和模型名相同。')),
        marketLine,
        h('div', { class: 'two' },
          field(tr('输入价（credit / 百万 tokens）'), priceIn),
          field(tr('输出价（credit / 百万 tokens）'), priceOut)),
        field(tr('单次输出上限（tokens，可不填）'), maxOut, tr('模型拒绝过大的输出请求时才需要填。')),
        err.el, save));
      showMarket();
      if (old) { priceIn.focus(); return; }
      if (known) { offer(known); return; }
      listLine.textContent = tr('正在获取可用的模型…');
      const got = source ? api('GET', providerPath(source.name, '/models')) : api('GET', '/api/v1/share/detect');
      got.then(r => { if (box.isConnected) offer(r.models); },
        e => { if (box.isConnected) listLine.textContent = tr('获取不到模型列表（{error}），请手动填写模型名', { error: shareError(e) }); });
    }

    function testDialog(p, m) {
      const out = h('div', { id: 'test-result' }, h('p', { class: 'small muted', id: 'test-wait' }, tr('正在通过 {name} 向 {model} 发送一条测试请求…', { name: p.name, model: m.upstream_model })));
      const box = modal('dlg-test', tr('测试模型'), out, h('div', { class: 'modal-actions' }, h('button', { type: 'button', id: 'test-done', onclick: () => closeModal() }, tr('关闭'))));
      api('POST', providerPath(p.name, '/test'), { model: m.id }).then(r => {
        if (!box.isConnected) return;
        const res = r.result || {}, text = (res.text || '').trim();
        out.replaceChildren(...[
          text ? h('div', { class: 'notice ok', id: 'test-ok' }, tr('测试通过')) : h('div', { class: 'notice warn', id: 'test-empty' }, tr('模型没有返回可见内容')),
          text ? h('pre', { class: 'reply', id: 'test-text', dir: 'auto' }, text.length > 600 ? text.slice(0, 600) + '…' : text) : null,
          h('p', { class: 'small muted', id: 'test-usage' }, tr('输入 tokens：{in} · 输出 tokens：{out}', { in: num(res.prompt_tokens), out: num(res.completion_tokens) })),
          res.completion_tokens_reported ? h('div', { class: 'notice warn small', id: 'test-hidden' }, tr('模型报告了 {n} 个输出 tokens。买家看不到的部分（隐藏推理）不计费，定价时请考虑。', { n: num(res.completion_tokens_reported) })) : null,
        ].filter(Boolean));
      }, e => { if (box.isConnected) out.replaceChildren(h('div', { class: 'notice bad', id: 'test-error', role: 'alert' }, shareError(e))); });
    }

    // old: the provider whose credentials are replaced (null: a new provider).
    async function providerDialog(old) {
      let ks;
      try { ks = (await loadKinds()).filter(k => k.apikey || k.oauth); }
      catch (e) { ui.error = shareError(e); render(); return; }
      const type = h('select', { id: 'pv-type', disabled: !!old }, ks.map(k => h('option', { value: k.id }, k.name)));
      if (old) type.value = old.type;
      const name = h('input', { type: 'text', id: 'pv-name', value: old ? old.name : '', disabled: !!old, maxlength: '32', autocapitalize: 'off', spellcheck: 'false' });
      const key = h('input', { type: 'password', id: 'pv-key', autocomplete: 'off' });
      const url = h('input', { type: 'url', id: 'pv-url', autocapitalize: 'off', spellcheck: 'false' });
      const header = h('input', { type: 'text', id: 'pv-header', autocapitalize: 'off', spellcheck: 'false' });
      const noCheck = h('input', { type: 'checkbox', id: 'pv-nocheck' });
      const project = h('input', { type: 'text', id: 'pv-project', autocapitalize: 'off', spellcheck: 'false' });
      const code = h('textarea', { id: 'pv-code', rows: '3', autocapitalize: 'off', spellcheck: 'false' });
      const advanced = h('details', { id: 'pv-advanced' });
      const err = errorBox('pv-error');
      const body = h('div');
      let auth = old ? old.auth : '', login = null;
      const kind = () => ks.find(k => k.id === type.value);
      const chosenName = () => (name.value.trim() || type.value).toLowerCase();

      function check() {
        if (!/^[a-z0-9][a-z0-9._-]{0,31}$/.test(chosenName())) throw new Error(tr('名称只能用小写字母、数字和 . _ -，最多 32 个字符'));
        if (!old && providersOf(st.share).some(p => p.name === chosenName())) throw new Error(tr('已有名为 {name} 的厂商，请换一个名称；要更换它的凭据，请用它的“更换凭据”', { name: chosenName() }));
      }
      async function added(out) {
        closeModal();
        const c = out.check || {};
        ui.flash = c.unusable ? { cls: 'warn', text: tr('凭据有效，但账户余额不足，暂时无法完成请求') }
          : { cls: 'ok', text: (old ? tr('已更换 {name} 的凭据', { name: out.provider.name }) : tr('已添加 {name}', { name: out.provider.name })) + (c.balance ? ' · ' + tr('余额：{balance}', { balance: c.balance }) : '') };
        await reload();
        if (out.provider.models.length === 0) modelDialog(out.provider, null, c.models && c.models.length ? c.models : undefined);
      }
      const saveBtn = h('button', { class: 'primary', type: 'submit', id: 'pv-save' }, tr('验证并保存'));
      const startBtn = h('button', { class: 'primary', type: 'submit', id: 'pv-login-start' }, tr('获取登录地址'));
      const finishBtn = h('button', { class: 'primary', type: 'submit', id: 'pv-login-finish' }, tr('完成登录'));

      const saveKey = () => run(saveBtn, err, async () => {
        const k = kind();
        check();
        const req = { type: k.id, name: chosenName(), api_key: key.value.trim(), base_url: url.value.trim(), no_check: noCheck.checked };
        if (!req.api_key && !k.key_optional) throw new Error(tr('请填写 API key'));
        if (k.needs_url && !req.base_url) throw new Error(tr('请填写 API 地址'));
        const hv = header.value.trim();
        if (hv) {
          const i = hv.indexOf('=');
          if (i < 1 || i === hv.length - 1) throw new Error(tr('请求头的格式是 名称=值'));
          req.headers = { [hv.slice(0, i).trim()]: hv.slice(i + 1).trim() };
        }
        const out = await api('POST', '/api/v1/providers', req);
        key.value = '';
        await added(out);
      });
      const startLogin = () => run(startBtn, err, async () => {
        check();
        login = await api('POST', '/api/v1/providers/oauth/start', { type: type.value, project: project.value.trim() });
        draw();
        code.focus();
      });
      const finishLogin = () => run(finishBtn, err, async () => {
        if (!code.value.trim()) throw new Error(tr('请粘贴授权码或页面地址'));
        const out = await api('POST', '/api/v1/providers/oauth/finish', { id: login.id, code: code.value.trim(), name: chosenName() });
        code.value = '';
        await added(out);
      });

      function draw() {
        const k = kind();
        if (!(auth === 'apikey' && k.apikey) && !(auth === 'oauth' && k.oauth)) auth = k.apikey ? 'apikey' : 'oauth';
        name.setAttribute('placeholder', k.id);
        url.setAttribute('placeholder', k.base_url || 'https://');
        const tab = (mode, label) => h('button', { type: 'button', role: 'tab', id: 'pv-auth-' + mode, 'aria-selected': auth === mode ? 'true' : 'false',
          onclick: () => { auth = mode; login = null; err.say(''); draw(); } }, label);
        const urlField = field(k.needs_url ? tr('API 地址（OpenAI 兼容的 base URL）') : tr('API 地址（不填用官方地址）'), url);
        advanced.replaceChildren(...[h('summary', null, tr('高级选项')),
          k.needs_url ? null : urlField,
          field(tr('附加请求头（名称=值，可不填）'), header),
          h('label', { class: 'check small' }, noCheck, h('span', null, tr('不验证密钥，直接保存')))].filter(Boolean));
        const keyPage = k.key_page ? h('a', { href: k.key_page, target: '_blank', rel: 'noopener', id: 'pv-key-page' }, k.key_page) : null;
        body.replaceChildren(...[
          k.apikey && k.oauth ? h('div', { class: 'tabs', role: 'tablist' }, tab('apikey', 'API key'), tab('oauth', tr('账号登录'))) : null,
          auth === 'apikey' ? [
            field(k.key_optional ? tr('API key（没有可不填）') : 'API key', key, keyPage ? I18N.parts('在 {link} 创建', { link: keyPage }) : null),
            k.needs_url ? urlField : null,
            advanced,
          ] : [
            h('div', { class: 'notice warn small', id: 'pv-oauth-risk' }, tr('这是用厂商官方客户端的方式登录你的订阅账号。把订阅额度提供给他人使用可能违反厂商条款，账号有被封的风险，请先确认条款允许。')),
            !login ? (k.id === 'gemini' || k.id === 'antigravity' ? field(tr('Google Cloud 项目 ID（一般不用填）'), project) : null) : [
              h('p', { class: 'small' }, tr('1. 在浏览器中打开这个地址并登录：')),
              h('div', { class: 'login-url' }, h('a', { href: login.url, target: '_blank', rel: 'noopener', id: 'pv-login-url' }, login.url)),
              h('div', { class: 'modal-actions start' }, copyButton(() => login.url, 'pv-login-copy')),
              field(tr('2. 登录后，把页面显示的授权码，或浏览器最后停在的页面的完整地址粘贴到这里：'), code),
            ],
          ],
        ].flat(3).filter(Boolean));
        actions.replaceChildren(auth === 'apikey' ? saveBtn : login ? finishBtn : startBtn);
      }
      const actions = h('div');
      type.addEventListener('change', () => { login = null; err.say(''); draw(); });
      modal('dlg-provider', old ? tr('更换凭据') : tr('添加厂商'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); (auth === 'apikey' ? saveKey : login ? finishLogin : startLogin)(); } },
        field(tr('厂商'), type),
        field(tr('名称（区分同一厂商的多个账号，可不填）'), name),
        body, err.el, actions));
      draw();
      (old && auth === 'apikey' ? key : type).focus();
    }

    render();
    return { el, update(next) { st = next; render(); }, account(me, jobs) { acct = me; sold = jobs || []; render(); } };
  }

  function showDashboard(session) {
    markEnv(session);
    stopPolling();
    let current = session;
    const pill = h('span', { class: 'pill', id: 'conn-state' });
    const logoutBtn = h('button', { type: 'button', id: 'logout', onclick: logout }, tr('退出登录'));
    const who = h('span', { class: 'who', id: 'who' });
    const banner = h('div', { id: 'banner' });
    const tiles = h('div', { class: 'tiles', id: 'tiles' });
    const endpoint = section('endpoint', tr('本地 OpenAI 接口'));
    const network = section('network', tr('网络'));
    const models = section('models', tr('可用模型'));
    const devices = section('devices', tr('我的设备'));
    const jobs = section('jobs', tr('最近作业'));
    const feedback = feedbackPanel();
    const updateBox = h('div', { id: 'update' });
    const versionLabel = h('p', { class: 'small muted', id: 'app-version' });
    let market = [];
    const share = shareView(() => market);
    // Two views under one top bar. Both stay in the page, so that polling
    // keeps the hidden one current and switching shows it at once.
    const views = {
      overview: h('div', { id: 'view-overview' }, tiles, endpoint.el, network.el, models.el, devices.el, jobs.el, feedback, telemetryToggle(session.telemetry), versionLabel),
      share: share.el,
    };
    const navButtons = {};
    function showView(name) {
      if (!views[name]) name = 'overview';
      for (const k of Object.keys(views)) {
        views[k].hidden = k !== name;
        navButtons[k].setAttribute('aria-selected', k === name ? 'true' : 'false');
      }
      try { localStorage.setItem('st_view', name); } catch (e) { /* this visit only */ }
    }
    const navButton = (name, label) => (navButtons[name] = h('button', { type: 'button', role: 'tab', id: 'nav-' + name, onclick: () => showView(name) }, label));
    const nav = h('nav', { class: 'nav', role: 'tablist' }, navButton('overview', tr('概览')), navButton('share', tr('共享')));
    app.replaceChildren(
      h('header', { class: 'topbar' }, h('span', { class: 'brand' }, h('img', { class: 'brand-logo', src: '/ui/favicon.svg', alt: '' }), 'sharetoken'), pill, nav, h('span', { class: 'spacer' }), I18N.picker(), who, logoutBtn),
      h('main', null, updateBox, banner, views.overview, views.share));
    let savedView = '';
    try { savedView = localStorage.getItem('st_view') || ''; } catch (e) { /* the overview */ }
    showView(savedView);

    let lastTiles, lastMe = null, brief = null;
    // What the overview says about sharing; a click opens the sharing view.
    function setBrief(st) {
      brief = st ? { running: st.share.running, enabled: st.share.enabled, n: (st.share.serving || []).length } : null;
      if (lastMe) renderAccount(lastMe);
    }
    function shareTile() {
      const t = tile('tile-share', tr('共享'), !brief ? '—' : brief.running ? tr('正在共享') : brief.enabled ? tr('未运行') : tr('未开启'),
        brief && brief.running ? tr('在售模型：{n}', { n: num(brief.n) }) : null);
      t.classList.add('tile-link');
      t.setAttribute('role', 'button');
      t.addEventListener('click', () => showView('share'));
      return t;
    }
    function renderAccount(me) {
      lastMe = me;
      const b = me.balance, u = me.usage;
      const key = JSON.stringify([b, u, brief]);
      if (key !== lastTiles) {
        lastTiles = key;
        tiles.replaceChildren(
          tile('tile-available', tr('可用 credit'), credit(b.available_micros)),
          tile('tile-held', tr('冻结中'), credit(b.held_micros), tr('请求结束后多余部分退回')),
          tile('tile-bought', tr('买入'), credit(u.bought.micros), tr('{n} 次，{tokens} tokens', { n: num(u.bought.jobs), tokens: num(u.bought.prompt_tokens + u.bought.completion_tokens) })),
          tile('tile-sold', tr('卖出收益'), credit(u.sold.micros), tr('{n} 次，{tokens} tokens', { n: num(u.sold.jobs), tokens: num(u.sold.prompt_tokens + u.sold.completion_tokens) })),
          shareTile());
      }
      devices.set(me.devices, me.devices.length === 0 ? h('div', { class: 'empty' }, tr('暂无设备')) : table([
        { title: tr('名称'), get: d => d.peer === current.peer ? tr('{name}（本机）', { name: d.name }) : d.name },
        { title: tr('设备 ID'), get: d => h('span', { class: 'mono', title: d.peer }, shortPeer(d.peer)) },
        { title: tr('绑定时间'), get: d => time(d.created_at) },
        { title: tr('最近在线'), get: d => time(d.last_seen_at) },
      ], me.devices));
    }

    function renderNetwork(st) {
      const c = st.coordinator;
      const line = c.connected ? tr('协调器已连接') : tr('协调器未连接');
      const q = c.link && c.link.samples ? c.link : null;
      const link = q ? [Math.round(q.avg_ms || 0), Math.round(q.jitter_ms || 0), Math.round(q.loss_pct || 0)] : null;
      network.set([st.relays, c.connected, c.via, st.warnings, link],
        h('p', { class: 'small muted', id: 'coord-line' }, line),
        link ? h('p', { class: 'small muted', id: 'link-line' }, tr('链路质量：平均 {avg} ms · 抖动 {jitter} ms · 丢包 {loss}%', { avg: link[0], jitter: link[1], loss: link[2] })) : null,
        (st.warnings || []).map(w => h('div', { class: 'notice warn small' }, w)),
        !st.relays || st.relays.length === 0 ? h('div', { class: 'empty' }, tr('门户还没有登记中继')) : table([
          { title: tr('中继'), get: r => r.name || shortPeer(r.addr) },
          { title: tr('地区'), get: r => r.region || '—' },
          { title: tr('延迟'), num: true, get: r => r.error ? tr('不可达') : (r.rtt_ms ? r.rtt_ms.toFixed(1) + ' ms' : '—') },
          { title: '', get: r => r.current ? tr('当前使用') : '' },
        ], st.relays));
    }

    function renderNetworkDown(s) {
      const t = s.state === 'retrying' ? (s.error || tr('连接失败，正在重试')) : tr('正在连接…');
      network.set(['down', t], h('div', { class: 'empty', id: 'network-down' }, t));
      models.set(['down'], h('div', { class: 'empty' }, tr('连接网络后显示')));
      share.update(null);
      setBrief(null);
    }

    function renderModels(ms) {
      market = ms || [];
      models.set(ms, !ms || ms.length === 0 ? h('div', { class: 'empty', id: 'models-empty' }, tr('网络上暂时没有在线的模型')) : table([
        { title: tr('模型'), get: m => m.model },
        { title: tr('节点'), num: true, get: m => num(m.nodes) },
        { title: tr('最低输入价'), num: true, get: m => pricePerM(m.min_price_in) },
        { title: tr('最低输出价'), num: true, get: m => pricePerM(m.min_price_out) },
      ], ms));
    }

    function renderJobs(js) {
      jobs.set(js, !js || js.length === 0 ? h('div', { class: 'empty', id: 'jobs-empty' }, tr('还没有作业')) : table([
        { title: tr('时间'), get: j => time(j.created_at) },
        { title: tr('方向'), get: j => j.role === 'sell' ? tr('卖出') : tr('买入') },
        { title: tr('模型'), get: j => j.model },
        { title: tr('状态'), get: j => JOB_STATUS[j.status] || j.status },
        { title: 'Tokens', num: true, get: j => num(j.prompt_tokens + j.completion_tokens) },
        { title: 'Credit', num: true, get: j => j.role === 'sell' ? '+' + credit(j.earned_micros, 6) : '-' + credit(j.cost_micros, 6) },
      ], js));
    }

    function renderSession(s) {
      current = s;
      renderUpdate(s);
      who.textContent = s.user || '';
      who.title = s.user || '';
      const [cls, label] = STATE[s.state] || ['', s.state];
      pill.className = 'pill ' + cls;
      pill.textContent = label;
      endpoint.set(s.api, h('div', { class: 'endpoint' }, h('code', { id: 'endpoint-url' }, s.api), copyButton(() => s.api)),
        h('p', { class: 'small muted' }, tr('在支持 OpenAI 协议的工具里把 base URL 改成这个地址即可使用。')));
    }

    // What a new release means for this client. Links show their address: not
    // every platform's window can open them, and then they can be copied. The
    // box is rebuilt only when the offer changes, so a click is not lost.
    let shownUpdate = '';
    function renderUpdate(s) {
      const u = s.update || {};
      versionLabel.textContent = tr('版本 {version}', { version: s.version || '' });
      const web = x => typeof x === 'string' && /^https?:\/\//.test(x) ? x : '';
      const show = u.available && (u.required || !u.skipped);
      const sig = show ? u.key + '|' + u.required : '';
      if (sig === shownUpdate) return;
      shownUpdate = sig;
      if (!show) { updateBox.replaceChildren(); return; }
      const lines = [h('div', null,
        h('strong', { id: 'update-text' }, u.required ? tr('这个版本已不再受支持，请升级到最新版本后继续使用') : tr('有新版本 {version} 可用', { version: u.latest || '' })))];
      if (u.required) lines.push(h('div', { id: 'update-required' }, tr('升级前不能调用模型或接单，已有作业照常结算。')));
      const pkg = u.package && web(u.package.url), page = web(u.download_url), notes = web(u.notes_url);
      const dl = pkg || page;
      if (dl) lines.push(h('div', null, tr('下载'), ' ', h('a', { id: 'update-link', href: dl, target: '_blank', rel: 'noopener' }, dl)));
      const actions = [];
      if (notes) actions.push(h('a', { id: 'update-notes', href: notes, target: '_blank', rel: 'noopener' }, tr('更新内容')));
      if (!u.required && u.key) actions.push(h('button', { type: 'button', class: 'link', id: 'update-skip', onclick: async () => {
        try {
          const next = await api('POST', '/api/v1/update/skip', { key: u.key });
          renderUpdate(Object.assign({}, s, { update: next }));
        } catch (e) { setBanner(message(e)); }
      } }, tr('跳过此版本')));
      if (actions.length) lines.push(h('div', { class: 'update-actions' }, ...actions));
      updateBox.replaceChildren(h('div', { class: 'notice ' + (u.required ? 'bad' : 'warn'), id: 'update-notice' }, ...lines));
    }

    function setBanner(text) {
      if (banner.textContent === text) return;
      banner.replaceChildren(...(text ? [h('div', { class: 'notice warn', id: 'banner-text' }, text)] : []));
    }

    let stopped = false, tick = 0, inflight = false;

    async function refresh() {
      if (inflight || stopped) return;
      inflight = true;
      try {
        const s = await api('GET', '/api/v1/session');
        if (stopped) return;
        if (!s.logged_in) { boot(s); return; }
        renderSession(s);
        // Account data: every third tick; the state: every tick.
        if (tick++ % 3 === 0) {
          try {
            const [me, j, sj] = await Promise.all([api('GET', '/api/v1/account'), api('GET', '/api/v1/jobs?limit=10'), api('GET', '/api/v1/jobs?role=sell&limit=10')]);
            if (stopped) return;
            renderAccount(me);
            renderJobs(j.jobs);
            share.account(me, sj.jobs);
            setBanner('');
          } catch (e) {
            if (e.status === 401) { boot(); return; }
            setBanner(tr('暂时连不上门户，下面是上次获取的账户信息（{error}）', { error: message(e) }));
          }
        }
        if (s.state === 'running') {
          try {
            const st = await api('GET', '/api/v1/status');
            renderNetwork(st);
            share.update(st);
            setBrief(st);
            if (tick % 3 === 1) renderModels((await api('GET', '/api/v1/models')).models);
          } catch (e) {
            if (e.code === 'not_connected' || e.status === 401) renderNetworkDown(s);
            else models.set(['err', e.message], h('div', { class: 'empty' }, tr('暂时获取不到模型列表')));
          }
        } else {
          renderNetworkDown(s);
        }
      } catch (e) {
        setBanner(message(e));
      } finally {
        inflight = false;
      }
    }

    async function logout() {
      logoutBtn.disabled = true;
      stopped = true;
      stopPolling();
      try { await api('POST', '/api/v1/logout'); }
      catch (e) { stopped = false; logoutBtn.disabled = false; setBanner(tr('退出失败：{error}', { error: message(e) })); poll = setInterval(refresh, 5000); return; }
      boot();
    }

    renderSession(session);
    renderNetworkDown(session);
    refresh();
    poll = setInterval(refresh, 5000);
  }

  // ---- start ----

  async function boot(known) {
    stopPolling();
    let s = known;
    if (!s) {
      for (let i = 0; !s; i++) {
        try { s = await api('GET', '/api/v1/session'); }
        catch (e) {
          if (i === 0) app.replaceChildren(h('div', { class: 'auth' }, h('div', { class: 'notice bad', id: 'boot-error' }, message(e))));
          await new Promise(r => setTimeout(r, 2000));
        }
      }
    }
    if (s.logged_in) showDashboard(s);
    else showAuth(s, s.state === 'expired' ? tr('登录已失效（可能已在其他地方退出或被吊销），请重新登录。') : '');
  }

  boot();
});
