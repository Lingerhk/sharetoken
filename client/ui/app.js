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
      h('span', null, tr('发送匿名使用统计（活跃、登录结果、连接耗时）。不含邮箱、设备 ID、提示词或模型内容。')));
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
        const r = await api('POST', '/api/register/code', { pin: p.pin_required || p.known ? p.fingerprint : '', email: email.value.trim() });
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
        const s = await api('POST', isLogin ? '/api/login' : '/api/register', body);
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
          await api('POST', '/api/feedback', { category: kind.value, message: text.value, lang: I18N.lang });
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
    app.replaceChildren(
      h('header', { class: 'topbar' }, h('span', { class: 'brand' }, h('img', { class: 'brand-logo', src: '/ui/favicon.svg', alt: '' }), 'sharetoken'), pill, h('span', { class: 'spacer' }), I18N.picker(), who, logoutBtn),
      h('main', null, banner, tiles, endpoint.el, network.el, models.el, devices.el, jobs.el, feedback, telemetryToggle(session.telemetry)));

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
          tile('tile-available', tr('可用 credit'), credit(b.available_micros)),
          tile('tile-held', tr('冻结中'), credit(b.held_micros), tr('请求结束后多余部分退回')),
          tile('tile-bought', tr('买入'), credit(u.bought.micros), tr('{n} 次，{tokens} tokens', { n: num(u.bought.jobs), tokens: num(u.bought.prompt_tokens + u.bought.completion_tokens) })),
          tile('tile-sold', tr('卖出收益'), credit(u.sold.micros), tr('{n} 次，{tokens} tokens', { n: num(u.sold.jobs), tokens: num(u.sold.prompt_tokens + u.sold.completion_tokens) })));
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
      network.set([st.relays, c.connected, c.via, st.warnings],
        h('p', { class: 'small muted', id: 'coord-line' }, line),
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
    }

    function renderModels(ms) {
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
      who.textContent = s.user || '';
      who.title = s.user || '';
      const [cls, label] = STATE[s.state] || ['', s.state];
      pill.className = 'pill ' + cls;
      pill.textContent = label;
      endpoint.set(s.api, h('div', { class: 'endpoint' }, h('code', { id: 'endpoint-url' }, s.api), copyButton(() => s.api)),
        h('p', { class: 'small muted' }, tr('在支持 OpenAI 协议的工具里把 base URL 改成这个地址即可使用。')));
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
            setBanner(tr('暂时连不上门户，下面是上次获取的账户信息（{error}）', { error: message(e) }));
          }
        }
        if (s.state === 'running') {
          try {
            const st = await api('GET', '/api/status');
            renderNetwork(st);
            if (tick % 3 === 1) renderModels((await api('GET', '/api/models')).models);
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
      try { await api('POST', '/api/logout'); }
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
        try { s = await api('GET', '/api/session'); }
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
