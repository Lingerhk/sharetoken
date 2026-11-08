// The desktop app's page, served by `sharetoken daemon`. It only talks to the
// daemon's local API on its own origin, builds the DOM with textContent (no
// HTML from any server is ever parsed), and holds no token: the login stays
// inside the daemon.
'use strict';
(() => {
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
      throw new ApiError(0, 'daemon_down', '后台服务没有响应，请重新打开应用');
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
    bad_portal: '门户地址格式不对',
    insecure_portal: '拒绝使用其他机器上的明文 HTTP 门户：密码会以明文传输。请使用 https:// 地址',
    portal_unreachable: '连不上 sharetoken 服务器，请检查网络后重试',
    not_a_portal: '这个地址不是 sharetoken 门户',
    pin_required: '请先核对并确认门户证书指纹',
    pin_mismatch: '门户证书和确认的指纹不一致，连接可能被拦截。已停止，密码没有发送',
    bad_credentials: '账号或密码错误，或账号已被停用',
    throttled: '尝试次数太多，请几分钟后再试',
    registration_closed: '该门户未开放注册，请联系管理员开通账号',
    exists: '这个邮箱已经注册过了，请直接登录',
    bad_code: '两步验证码不正确',
    email_required: '请用邮箱注册',
    code_cooldown: '刚发送过验证码，请稍后再试',
    mfa_setup_required: '管理员账号需要先在门户网页上完成两步验证设置',
    already_logged_in: '已经登录，请先退出',
    daemon_down: '后台服务没有响应，请重新打开应用',
  };
  const message = e => MESSAGES[e.code] || (e.code === 'bad_request' ? badRequest(e.message) : e.message || String(e));
  function badRequest(m) {
    if (/email/.test(m)) return '邮箱地址格式不正确';
    if (/password/.test(m)) return '密码长度需要 8–256 个字符';
    return m;
  }

  const MICRO = 1e6;
  // Micro-credit precision: single requests cost a few micro-credits, and
  // rounding them away would make a balance look unchanged.
  const credit = (m, digits = 6) => (Number(m || 0) / MICRO).toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: digits });
  const num = n => Number(n || 0).toLocaleString('zh-CN');
  const time = nanos => nanos ? new Date(Number(nanos) / 1e6).toLocaleString('zh-CN', { hour12: false }) : '—';
  const pricePerM = m => credit(m, 6) + ' / M';
  const shortPeer = p => p && p.length > 16 ? p.slice(0, 8) + '…' + p.slice(-6) : (p || '—');

  function table(cols, rows) {
    return h('div', { class: 'scroll' }, h('table', null,
      h('thead', null, h('tr', null, cols.map(c => h('th', { class: c.num ? 'num' : null }, c.title)))),
      h('tbody', null, rows.map(r => h('tr', null, cols.map(c => h('td', { class: c.num ? 'num' : null }, c.get(r))))))));
  }

  // The usage-statistics setting; absent from builds that send none.
  function telemetryToggle(t) {
    if (!t || !t.available) return null;
    const box = h('input', { type: 'checkbox', id: 'telemetry-toggle', checked: t.enabled, onchange: async () => {
      box.disabled = true;
      try { t.enabled = (await api('PUT', '/api/telemetry', { enabled: box.checked })).enabled; }
      catch (e) { /* the box returns to the saved value below */ }
      box.checked = t.enabled;
      box.disabled = false;
    } });
    return h('label', { class: 'check small muted', id: 'telemetry' }, box,
      h('span', null, '发送匿名使用统计（活跃、登录结果、连接耗时）。不含邮箱、设备 ID、提示词或模型内容。'));
  }

  // ---- sign-in ----

  let poll = null;
  function stopPolling() { if (poll) { clearInterval(poll); poll = null; } }

  // notice: why the user is back here (a login the portal rejected).
  // A dev build must be impossible to mistake for the real thing.
  function markEnv(session) {
    if (session.env !== 'dev' || document.body.classList.contains('env-dev')) return;
    document.body.classList.add('env-dev');
    document.title = 'sharetoken DEV';
    document.body.prepend(h('div', { class: 'env-banner', id: 'env-banner' }, 'DEV 环境（' + session.portal + '）'));
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
    const sendCode = h('button', { type: 'button', id: 'send-code', onclick: requestCode }, '发送验证码');
    let cooldown = null;
    async function requestCode() {
      const p = auth.probe;
      auth.error = '';
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.value.trim())) { auth.error = '请填写正确的邮箱地址'; render(); return; }
      sendCode.disabled = true;
      try {
        const r = await api('POST', '/api/register/code', { pin: p.pin_required || p.known ? p.fingerprint : '', email: email.value.trim() });
        let left = r.cooldown_seconds || 60;
        sendCode.textContent = left + ' 秒后可重发';
        clearInterval(cooldown);
        cooldown = setInterval(() => {
          if (--left > 0) { sendCode.textContent = left + ' 秒后可重发'; return; }
          clearInterval(cooldown); sendCode.disabled = false; sendCode.textContent = '重新发送';
        }, 1000);
        mailCode.focus();
      } catch (e) {
        sendCode.disabled = false;
        auth.error = e.code === 'code_cooldown' ? '刚发送过，请稍后再试' : message(e);
        render();
      }
    }
    const totp = h('input', { type: 'text', id: 'totp', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: '7' });

    const telemetry = telemetryToggle(session.telemetry);

    function render() {
      root.replaceChildren(...[
        h('h1', null, 'sharetoken'),
        h('p', { class: 'tagline' }, '共享和使用模型算力'),
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
        auth.busy ? h('p', { class: 'small muted', id: 'connecting' }, '正在连接 sharetoken…') : null,
        errorLine(),
        auth.busy ? null : h('button', { class: 'primary', type: 'submit', id: 'portal-next' }, '重试'));
    }

    async function probe() {
      auth.error = '';
      auth.busy = true; render();
      try {
        auth.probe = await api('POST', '/api/portal/probe', {}); // empty: the daemon uses the built-in portal
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
          ? '门户证书和上次登录时不同。可能是运维更换了证书，也可能是连接被拦截。请向管理员核对新的指纹：'
          : '这个门户使用自签名证书。请向管理员核对指纹：'),
        h('div', null, h('code', { id: 'fingerprint' }, p.fingerprint)),
        h('label', { class: 'check' },
          h('input', { type: 'checkbox', id: 'cert-ok', checked: auth.confirmed, onchange: e => { auth.confirmed = e.target.checked; render(); } }),
          h('span', null, '指纹一致，信任这个门户')));
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
        h('div', { class: 'tabs', role: 'tablist' }, tab('login', '登录', true), tab('register', '注册', canRegister)),
        certBlock(),
        isLogin
          ? [h('label', { class: 'field' }, h('span', null, '邮箱（或用户名）'), account),
             h('label', { class: 'field' }, h('span', null, '密码'), password),
             auth.totp ? h('label', { class: 'field' }, h('span', null, '两步验证码（6 位）'), totp) : null]
          : [h('label', { class: 'field' }, h('span', null, '邮箱'), email),
             p.email_verification ? h('label', { class: 'field' }, h('span', null, '邮箱验证码（也看看垃圾邮件）'), h('div', { class: 'code-row' }, mailCode, sendCode)) : null,
             h('label', { class: 'field' }, h('span', null, '密码（至少 8 位）'), password),
             h('label', { class: 'field' }, h('span', null, '确认密码'), password2),
             p.signup_credit_micros ? h('p', { class: 'small muted', id: 'signup-credit' }, '注册赠送 ' + credit(p.signup_credit_micros) + ' credit') : null],
        !canRegister ? h('p', { class: 'small muted', id: 'reg-closed' }, '该门户未开放注册，账号由管理员开通。') : null,
        errorLine(),
        h('button', { class: 'primary', type: 'submit', id: 'submit', disabled: blocked },
          auth.busy ? '请稍候…' : (isLogin ? '登录' : '注册并登录')));
    }

    async function submit() {
      const p = auth.probe;
      auth.error = '';
      const isLogin = auth.tab === 'login';
      if (!isLogin && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.value.trim())) auth.error = '请填写正确的邮箱地址';
      else if (isLogin && !account.value.trim()) auth.error = '请填写邮箱或用户名';
      else if (!isLogin && p.email_verification && !mailCode.value.trim()) auth.error = '请填写邮箱收到的验证码';
      else if (!password.value) auth.error = '请填写密码';
      else if (!isLogin && password.value.length < 8) auth.error = '密码至少 8 位';
      else if (!isLogin && password.value !== password2.value) auth.error = '两次输入的密码不一致';
      else if (isLogin && auth.totp && !totp.value.trim()) auth.error = '请填写两步验证码';
      if (auth.error) { render(); return; }
      auth.busy = true; render();
      const body = { pin: p.pin_required || p.known ? p.fingerprint : '', password: password.value };
      if (isLogin) { body.account = account.value.trim(); if (auth.totp) body.totp = totp.value.trim(); }
      else { body.email = email.value.trim(); if (p.email_verification) body.code = mailCode.value.trim(); }
      try {
        const s = await api('POST', isLogin ? '/api/login' : '/api/register', body);
        password.value = password2.value = totp.value = '';
        showDashboard(s);
        return;
      } catch (e) {
        if (e.code === 'mfa_required') { auth.totp = true; auth.error = ''; }
        else if (e.code === 'already_logged_in') { boot(); return; }
        else {
          auth.error = !isLogin && e.code === 'bad_code' ? '验证码错误或已过期' : message(e);
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

  function copyButton(getText) {
    const b = h('button', { type: 'button', class: 'small', id: 'copy-endpoint', onclick: async () => {
      const text = getText();
      try {
        await navigator.clipboard.writeText(text);
      } catch (e) { // webviews may refuse the clipboard API
        const t = h('textarea', { readonly: true, value: text });
        document.body.append(t); t.select();
        try { document.execCommand('copy'); } catch (e2) { /* the address stays selectable */ }
        t.remove();
      }
      b.textContent = '已复制';
      setTimeout(() => { b.textContent = '复制'; }, 1500);
    } }, '复制');
    return b;
  }

  const STATE = {
    starting: ['warn', '正在连接网络…'],
    retrying: ['bad', '暂时连不上网络，正在重试'],
    running: ['ok', '已连接'],
  };
  const JOB_STATUS = { held: '进行中', pending: '待最终确认', disputed: '争议中', final: '已完成', released: '已释放', refunded: '已退款' };

  function showDashboard(session) {
    markEnv(session);
    stopPolling();
    let current = session;
    const pill = h('span', { class: 'pill', id: 'conn-state' });
    const logoutBtn = h('button', { type: 'button', id: 'logout', onclick: logout }, '退出登录');
    const who = h('span', { class: 'who', id: 'who' });
    const banner = h('div', { id: 'banner' });
    const tiles = h('div', { class: 'tiles', id: 'tiles' });
    const endpoint = section('endpoint', '本地 OpenAI 接口');
    const network = section('network', '网络');
    const models = section('models', '可用模型');
    const devices = section('devices', '我的设备');
    const jobs = section('jobs', '最近作业');
    app.replaceChildren(
      h('header', { class: 'topbar' }, h('span', { class: 'brand' }, 'sharetoken'), pill, h('span', { class: 'spacer' }), who, logoutBtn),
      h('main', null, banner, tiles, endpoint.el, network.el, models.el, devices.el, jobs.el, telemetryToggle(session.telemetry)));

    function tile(id, label, value, sub) {
      return h('div', { class: 'tile', id }, h('div', { class: 'label' }, label), h('div', { class: 'value' }, value), sub ? h('div', { class: 'sub' }, sub) : null);
    }

    let lastTiles;
    function renderAccount(me) {
      const b = me.balance, u = me.usage;
      const key = JSON.stringify([b, u]);
      if (key !== lastTiles) {
        lastTiles = key;
        tiles.replaceChildren(
          tile('tile-available', '可用 credit', credit(b.available_micros)),
          tile('tile-held', '冻结中', credit(b.held_micros), '请求结束后多余部分退回'),
          tile('tile-bought', '买入', credit(u.bought.micros), num(u.bought.jobs) + ' 次，' + num(u.bought.prompt_tokens + u.bought.completion_tokens) + ' tokens'),
          tile('tile-sold', '卖出收益', credit(u.sold.micros), num(u.sold.jobs) + ' 次，' + num(u.sold.prompt_tokens + u.sold.completion_tokens) + ' tokens'));
      }
      devices.set(me.devices, me.devices.length === 0 ? h('div', { class: 'empty' }, '暂无设备') : table([
        { title: '名称', get: d => d.name + (d.peer === current.peer ? '（本机）' : '') },
        { title: '设备 ID', get: d => h('span', { class: 'mono', title: d.peer }, shortPeer(d.peer)) },
        { title: '绑定时间', get: d => time(d.created_at) },
        { title: '最近在线', get: d => time(d.last_seen_at) },
      ], me.devices));
    }

    function renderNetwork(st) {
      const c = st.coordinator;
      const line = c.connected ? '协调器已连接' : '协调器未连接';
      network.set([st.relays, c.connected, c.via, st.warnings],
        h('p', { class: 'small muted', id: 'coord-line' }, line),
        (st.warnings || []).map(w => h('div', { class: 'notice warn small' }, w)),
        !st.relays || st.relays.length === 0 ? h('div', { class: 'empty' }, '门户还没有登记中继') : table([
          { title: '中继', get: r => r.name || shortPeer(r.addr) },
          { title: '地区', get: r => r.region || '—' },
          { title: '延迟', num: true, get: r => r.error ? '不可达' : (r.rtt_ms ? r.rtt_ms.toFixed(1) + ' ms' : '—') },
          { title: '', get: r => r.current ? '当前使用' : '' },
        ], st.relays));
    }

    function renderNetworkDown(s) {
      const t = s.state === 'retrying' ? (s.error || '连接失败，正在重试') : '正在连接…';
      network.set(['down', t], h('div', { class: 'empty', id: 'network-down' }, t));
      models.set(['down'], h('div', { class: 'empty' }, '连接网络后显示'));
    }

    function renderModels(ms) {
      models.set(ms, !ms || ms.length === 0 ? h('div', { class: 'empty', id: 'models-empty' }, '网络上暂时没有在线的模型') : table([
        { title: '模型', get: m => m.model },
        { title: '节点', num: true, get: m => num(m.nodes) },
        { title: '最低输入价', num: true, get: m => pricePerM(m.min_price_in) },
        { title: '最低输出价', num: true, get: m => pricePerM(m.min_price_out) },
      ], ms));
    }

    function renderJobs(js) {
      jobs.set(js, !js || js.length === 0 ? h('div', { class: 'empty', id: 'jobs-empty' }, '还没有作业') : table([
        { title: '时间', get: j => time(j.created_at) },
        { title: '方向', get: j => j.role === 'sell' ? '卖出' : '买入' },
        { title: '模型', get: j => j.model },
        { title: '状态', get: j => JOB_STATUS[j.status] || j.status },
        { title: 'Tokens', num: true, get: j => num(j.prompt_tokens + j.completion_tokens) },
        { title: 'Credit', num: true, get: j => j.role === 'sell' ? '+' + credit(j.earned_micros, 6) : '-' + credit(j.cost_micros, 6) },
      ], js));
    }

    function renderSession(s) {
      current = s;
      who.textContent = s.user || '';
      who.title = s.user || '';
      const [cls, label] = STATE[s.state] || ['', s.state];
      pill.className = 'pill ' + cls;
      pill.textContent = label;
      endpoint.set(s.api, h('div', { class: 'endpoint' }, h('code', { id: 'endpoint-url' }, s.api), copyButton(() => s.api)),
        h('p', { class: 'small muted' }, '在支持 OpenAI 协议的工具里把 base URL 改成这个地址即可使用。'));
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
        const s = await api('GET', '/api/session');
        if (stopped) return;
        if (!s.logged_in) { boot(s); return; }
        renderSession(s);
        // Account data: every third tick; the state: every tick.
        if (tick++ % 3 === 0) {
          try {
            const [me, j] = await Promise.all([api('GET', '/api/account'), api('GET', '/api/jobs?limit=10')]);
            if (stopped) return;
            renderAccount(me);
            renderJobs(j.jobs);
            setBanner('');
          } catch (e) {
            if (e.status === 401) { boot(); return; }
            setBanner('暂时连不上门户，下面是上次获取的账户信息（' + message(e) + '）');
          }
        }
        if (s.state === 'running') {
          try {
            const st = await api('GET', '/api/status');
            renderNetwork(st);
            if (tick % 3 === 1) renderModels((await api('GET', '/api/models')).models);
          } catch (e) {
            if (e.code === 'not_connected' || e.status === 401) renderNetworkDown(s);
            else models.set(['err', e.message], h('div', { class: 'empty' }, '暂时获取不到模型列表'));
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
      try { await api('POST', '/api/logout'); }
      catch (e) { stopped = false; logoutBtn.disabled = false; setBanner('退出失败：' + message(e)); poll = setInterval(refresh, 5000); return; }
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
        try { s = await api('GET', '/api/session'); }
        catch (e) {
          if (i === 0) app.replaceChildren(h('div', { class: 'auth' }, h('div', { class: 'notice bad', id: 'boot-error' }, message(e))));
          await new Promise(r => setTimeout(r, 2000));
        }
      }
    }
    if (s.logged_in) showDashboard(s);
    else showAuth(s, s.state === 'expired' ? '登录已失效（可能已在其他地方退出或被吊销），请重新登录。' : '');
  }

  boot();
})();
