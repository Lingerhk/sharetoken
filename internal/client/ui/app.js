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

  // The local API's key, for this page opened in a browser from the address
  // `sharetoken daemon` prints (…/#key=…). In the desktop window the shell adds
  // the key itself. sessionStorage keeps it to this tab and this port.
  const keyInAddress = () => {
    const m = /(?:^#|&)key=([0-9a-f]{16,128})(?:&|$)/.exec(location.hash);
    if (!m) return '';
    try { sessionStorage.setItem('st_local_key', m[1]); } catch (e) { /* this page only */ }
    history.replaceState(null, '', location.pathname + location.search);
    return m[1];
  };
  const localKey = keyInAddress() || (() => { try { return sessionStorage.getItem('st_local_key') || ''; } catch (e) { return ''; } })();
  // The address pasted into a tab already on the page changes only the
  // fragment, which loads nothing: start again with the key.
  addEventListener('hashchange', () => { if (keyInAddress()) location.reload(); });

  async function api(method, path, body) {
    let resp;
    const headers = body ? { 'Content-Type': 'application/json' } : {};
    if (localKey) headers.Authorization = 'Bearer ' + localKey;
    try {
      resp = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
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
    apikey: tr('无法读写 API key 文件（apikey.json），请检查数据目录的权限'),
    local_auth: tr('请用 sharetoken daemon 启动时打印的地址（带 #key=）打开这个页面'),
    bad_portal: tr('门户地址格式不对'),
    insecure_portal: tr('拒绝使用其他机器上的明文 HTTP 门户：验证码会以明文传输。请使用 https:// 地址'),
    portal_outdated: tr('ShareToken 服务器版本过旧，暂时无法登录'),
    portal_unreachable: tr('连不上 ShareToken 服务器，请检查网络后重试'),
    not_a_portal: tr('这个地址不是 ShareToken 门户'),
    pin_required: tr('请先核对并确认门户证书指纹'),
    pin_mismatch: tr('门户证书和确认的指纹不一致，连接可能被拦截。已停止，邮箱和验证码都没有发送'),
    bad_email: tr('请填写正确的邮箱地址'),
    account_disabled: tr('这个账号已被停用，请联系客服'),
    throttled: tr('尝试次数太多，请几分钟后再试'),
    registration_closed: tr('这个邮箱还没有账号，该门户也未开放注册，请联系管理员开通账号'),
    bad_code: tr('验证码错误或已过期'),
    invite_required: tr('新账号需要邀请码，填写后重新发送验证码'),
    bad_invite: tr('邀请码无效、已用完或已过期'),
    email_not_allowed: tr('这个邮箱不能在该门户注册'),
    mail_unavailable: tr('服务器暂时无法发送邮件，请稍后再试或联系客服'),
    code_cooldown: tr('刚发送过验证码，请稍后再试'),
    challenge_expired: tr('登录超时，请重新获取验证码'),
    mfa_setup_required: tr('管理员账号需要先在门户网页上完成两步验证设置'),
    already_logged_in: tr('已经登录，请先退出'),
    daemon_down: tr('后台服务没有响应，请重新打开应用'),
  };
  const message = e => MESSAGES[e.code] || (e.code === 'bad_request' ? badRequest(e.message) : e.message || String(e));
  function badRequest(m) {
    if (/email/.test(m)) return tr('请填写正确的邮箱地址');
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

  // tip: what hovering explains, for what would not fit.
  function tile(id, label, value, sub, tip) {
    return h('div', { class: 'tile', id, title: tip || (typeof sub === 'string' ? sub : null) }, h('div', { class: 'label' }, label), h('div', { class: 'value' }, value), sub ? h('div', { class: 'sub' }, sub) : null);
  }

  // A round-trip time in ms, coloured by how good it is, with the portal's
  // thresholds; 0 or nothing: not measured.
  const rttClass = ms => ms < 80 ? 'ok' : ms < 250 ? 'warn' : 'bad';
  function rtt(ms) {
    ms = Number(ms || 0);
    if (!ms) return h('span', { class: 'muted' }, '—');
    return h('span', { class: 'rtt ' + rttClass(ms) }, (ms < 10 ? ms.toFixed(1) : String(Math.round(ms))) + ' ms');
  }

  // richText draws the small markup of release notes: "# " a title, "## " a
  // subtitle, "- " a list item, any other line a paragraph; **bold** and `code`
  // inside a line. It builds elements itself: the text is never parsed as HTML.
  function richInline(text) {
    const out = [], re = /\*\*([^*]+)\*\*|`([^`]+)`/g;
    let last = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > last) out.push(text.slice(last, m.index));
      out.push(m[1] !== undefined ? h('strong', null, m[1]) : h('code', null, m[2]));
      last = re.lastIndex;
    }
    if (last < text.length) out.push(text.slice(last));
    return out;
  }
  function richText(src, id) {
    const out = h('div', { class: 'rich', id, dir: 'auto' });
    let list = null;
    for (const raw of String(src || '').split('\n')) {
      const line = raw.trim();
      let m;
      if (!line) { list = null; continue; }
      if ((m = /^##\s+(.*)$/.exec(line))) { list = null; out.append(h('h4', null, richInline(m[1]))); }
      else if ((m = /^#\s+(.*)$/.exec(line))) { list = null; out.append(h('h3', null, richInline(m[1]))); }
      else if ((m = /^[-*]\s+(.*)$/.exec(line))) { if (!list) { list = h('ul'); out.append(list); } list.append(h('li', null, richInline(m[1]))); }
      else { list = null; out.append(h('p', null, richInline(line))); }
    }
    return out;
  }

  // Outline icons on a 24×24 grid (Heroicons, MIT licence), as on the portal.
  const ICONS = {
    overview: 'M3.75 6A2.25 2.25 0 016 3.75h2.25A2.25 2.25 0 0110.5 6v2.25a2.25 2.25 0 01-2.25 2.25H6a2.25 2.25 0 01-2.25-2.25V6zM3.75 15.75A2.25 2.25 0 016 13.5h2.25a2.25 2.25 0 012.25 2.25V18a2.25 2.25 0 01-2.25 2.25H6A2.25 2.25 0 013.75 18v-2.25zM13.5 6a2.25 2.25 0 012.25-2.25H18A2.25 2.25 0 0120.25 6v2.25A2.25 2.25 0 0118 10.5h-2.25a2.25 2.25 0 01-2.25-2.25V6zM13.5 15.75a2.25 2.25 0 012.25-2.25H18a2.25 2.25 0 012.25 2.25V18A2.25 2.25 0 0118 20.25h-2.25A2.25 2.25 0 0113.5 18v-2.25z',
    share: 'M5.25 14.25h13.5m-13.5 0a3 3 0 01-3-3m3 3a3 3 0 100 6h13.5a3 3 0 100-6m-16.5-3a3 3 0 013-3h13.5a3 3 0 013 3m-19.5 0a4.5 4.5 0 01.9-2.7L5.737 5.1a3.375 3.375 0 012.7-1.35h7.126c1.062 0 2.062.5 2.7 1.35l2.587 3.45a4.5 4.5 0 01.9 2.7m0 0a3 3 0 01-3 3m0 3h.008v.008h-.008v-.008zm0-6h.008v.008h-.008v-.008zm-3 6h.008v.008h-.008v-.008zm0-6h.008v.008h-.008v-.008z',
    pairing: 'M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0zm6 3a2 2 0 11-4 0 2 2 0 014 0zM7 10a2 2 0 11-4 0 2 2 0 014 0z',
    activity: 'M12 6v6h4.5m4.5 0a9 9 0 11-18 0 9 9 0 0118 0z',
    settings: 'M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.324.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 011.37.49l1.296 2.247a1.125 1.125 0 01-.26 1.431l-1.003.827c-.293.24-.438.613-.431.992a6.759 6.759 0 010 .255c-.007.378.138.75.43.99l1.005.828c.424.35.534.954.26 1.43l-1.298 2.247a1.125 1.125 0 01-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.57 6.57 0 01-.22.128c-.331.183-.581.495-.644.869l-.213 1.28c-.09.543-.56.941-1.11.941h-2.594c-.55 0-1.02-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 01-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 01-1.369-.49l-1.297-2.247a1.125 1.125 0 01.26-1.431l1.004-.827c.292-.24.437-.613.43-.992a6.932 6.932 0 010-.255c.007-.378-.138-.75-.43-.99l-1.004-.828a1.125 1.125 0 01-.26-1.43l1.297-2.247a1.125 1.125 0 011.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.087.22-.128.332-.183.582-.495.644-.869l.214-1.281z M15 12a3 3 0 11-6 0 3 3 0 016 0z',
    feedback: 'M8.625 12a.375.375 0 11-.75 0 .375.375 0 01.75 0zm0 0H8.25m4.125 0a.375.375 0 11-.75 0 .375.375 0 01.75 0zm0 0H12m4.125 0a.375.375 0 11-.75 0 .375.375 0 01.75 0zm0 0h-.375M21 12c0 4.556-4.03 8.25-9 8.25a9.764 9.764 0 01-2.555-.337A5.972 5.972 0 015.41 20.97a5.969 5.969 0 01-.474-.065 4.48 4.48 0 00.978-2.025c.09-.457-.133-.901-.467-1.226C3.93 16.178 3 14.189 3 12c0-4.556 4.03-8.25 9-8.25s9 3.694 9 8.25z',
  };
  const icon = name => I18N.svg(ICONS[name], 20);

  // A time the daemon reports as text (RFC 3339), to the minute.
  const clock = t => t ? new Date(t).toLocaleString(I18N.lang, { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—';

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
    // sent: the portal's answer to the last code request; invite: the portal
    // asked for an invite code; mfa: the code checked out and the account
    // wants its authenticator code.
    const auth = { probe: null, sent: null, invite: false, mfa: false, confirmed: false, busy: false, error: '' };
    // The brand on one side stays; the form beside it is redrawn.
    const root = h('div', { class: 'auth-main', id: 'auth' });
    app.replaceChildren(h('div', { class: 'auth' },
      h('div', { class: 'auth-side' }, h('img', { class: 'auth-logo', src: '/ui/favicon.svg', alt: '' }), h('h1', null, 'ShareToken'), h('p', { class: 'tagline' }, tr('共享和使用模型算力'))),
      root));

    const lastEmail = (session.user || '').includes('@') ? session.user : '';
    const email = h('input', { type: 'email', id: 'email', autocomplete: 'email', value: lastEmail, autocapitalize: 'off', spellcheck: 'false' });
    const mailCode = h('input', { type: 'text', id: 'mail-code', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: '7' });
    const invite = h('input', { type: 'text', id: 'invite', autocomplete: 'off', autocapitalize: 'characters', spellcheck: 'false' });
    const totp = h('input', { type: 'text', id: 'totp', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: '7' });
    const sendCode = h('button', { type: 'button', id: 'send-code', onclick: requestCode }, tr('发送验证码'));
    const validEmail = () => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.value.trim());
    const pinOf = p => p.pin_required || p.known ? p.fingerprint : '';
    let cooldown = null;
    async function requestCode() {
      const p = auth.probe;
      auth.error = '';
      if (!validEmail()) { auth.error = tr('请填写正确的邮箱地址'); render(); email.focus(); return; }
      if (auth.invite && !invite.value.trim()) { auth.error = tr('请填写邀请码'); render(); invite.focus(); return; }
      sendCode.disabled = true;
      try {
        auth.sent = await api('POST', '/api/v1/auth/code', { pin: pinOf(p), email: email.value.trim(), invite_code: invite.value.trim() });
        let left = auth.sent.cooldown_seconds || 60;
        sendCode.textContent = tr('{n} 秒后可重发', { n: left });
        clearInterval(cooldown);
        cooldown = setInterval(() => {
          if (--left > 0) { sendCode.textContent = tr('{n} 秒后可重发', { n: left }); return; }
          clearInterval(cooldown); cooldown = null; sendCode.disabled = false; sendCode.textContent = tr('重新发送');
        }, 1000);
        render();
        mailCode.focus();
      } catch (e) {
        sendCode.disabled = false;
        if (e.code === 'invite_required') auth.invite = true;
        if (e.code === 'pin_mismatch' || e.code === 'pin_required') auth.probe = null;
        auth.error = message(e);
        render();
        if (auth.invite && !invite.value.trim()) invite.focus();
      }
    }

    function render() {
      root.replaceChildren(...[
        I18N.picker(),
        notice && h('div', { class: 'notice warn', id: 'notice' }, notice),
        auth.probe ? (auth.mfa ? mfaPanel() : signInPanel()) : connectPanel(),
      ].filter(Boolean)); // replaceChildren would print a null as text
    }

    function errorLine() {
      return auth.error ? h('div', { class: 'notice bad', id: 'error', role: 'alert' }, auth.error) : null;
    }

    // The portal address is built in: the page only says it is connecting, or
    // why it could not and offers a retry.
    function connectPanel() {
      return h('form', { class: 'panel', id: 'portal-form', novalidate: true, onsubmit: e => { e.preventDefault(); probe(); } },
        auth.busy ? h('p', { class: 'small muted', id: 'connecting' }, tr('正在连接 ShareToken…')) : null,
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
      if (auth.probe) (email.value ? mailCode : email).focus();
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

    // One form for everyone: an address without an account gets one.
    function signInPanel() {
      const p = auth.probe;
      const blocked = auth.busy || (p.pin_required && !auth.confirmed);
      if (blocked && !sendCode.disabled) sendCode.disabled = true;
      else if (!blocked && sendCode.disabled && !cooldown) sendCode.disabled = false;
      const isNew = auth.sent && auth.sent.new_account;
      return h('form', { class: 'panel', id: 'signin-form', novalidate: true, onsubmit: e => { e.preventDefault(); submit(); } },
        h('h2', { class: 'auth-title' }, tr('登录或注册')),
        certBlock(),
        h('label', { class: 'field' }, h('span', null, tr('邮箱')), email),
        auth.invite || isNew && p.invite_required ? h('label', { class: 'field', id: 'invite-field' }, h('span', null, tr('邀请码（新账号需要）')), invite) : null,
        h('label', { class: 'field' }, h('span', null, tr('邮箱验证码（也看看垃圾邮件）')), h('div', { class: 'code-row' }, mailCode, sendCode)),
        isNew ? h('p', { class: 'small muted', id: 'new-account' }, p.signup_credit_micros
          ? tr('这个邮箱还没有账号，登录时会自动创建，并赠送 {n} credit', { n: credit(p.signup_credit_micros) })
          : tr('这个邮箱还没有账号，登录时会自动创建')) : null,
        !auth.sent ? h('p', { class: 'small muted', id: 'signin-hint' }, p.registration_open
          ? tr('不需要密码：输入邮箱，获取验证码即可登录；没有账号会自动注册。')
          : tr('不需要密码：输入邮箱，获取验证码即可登录。该门户未开放注册，账号由管理员开通。')) : null,
        errorLine(),
        h('button', { class: 'primary', type: 'submit', id: 'submit', disabled: blocked },
          auth.busy ? tr('请稍候…') : tr('登录')));
    }

    // The account has two-factor authentication: its authenticator code.
    function mfaPanel() {
      return h('form', { class: 'panel', id: 'mfa-form', novalidate: true, onsubmit: e => { e.preventDefault(); submit(); } },
        h('h2', { class: 'auth-title' }, tr('两步验证')),
        h('p', { class: 'small muted' }, tr('{email} 开启了两步验证，请输入身份验证器 App 里的 6 位验证码。', { email: email.value.trim() })),
        h('label', { class: 'field' }, h('span', null, tr('两步验证码（6 位）')), totp),
        errorLine(),
        h('button', { class: 'primary', type: 'submit', id: 'submit', disabled: auth.busy }, auth.busy ? tr('请稍候…') : tr('验证并登录')),
        h('button', { type: 'button', class: 'link', id: 'mfa-back', onclick: () => { auth.mfa = false; auth.error = ''; totp.value = ''; mailCode.value = ''; render(); mailCode.focus(); } }, tr('返回')));
    }

    async function submit() {
      const p = auth.probe;
      auth.error = '';
      if (auth.mfa) {
        if (!totp.value.trim()) auth.error = tr('请填写两步验证码');
      } else if (!validEmail()) auth.error = tr('请填写正确的邮箱地址');
      else if (!mailCode.value.trim()) auth.error = auth.sent ? tr('请填写邮箱收到的验证码') : tr('请先发送验证码');
      if (auth.error) { render(); return; }
      auth.busy = true; render();
      try {
        const s = auth.mfa
          ? await api('POST', '/api/v1/auth/mfa', { code: totp.value.trim() })
          : await api('POST', '/api/v1/auth/verify', { pin: pinOf(p), email: email.value.trim(), code: mailCode.value.trim(), invite_code: invite.value.trim() });
        mailCode.value = totp.value = '';
        clearInterval(cooldown); cooldown = null;
        showDashboard(s);
        return;
      } catch (e) {
        if (e.code === 'already_logged_in') { boot(); return; }
        if (e.code === 'mfa_required') { auth.mfa = true; auth.error = ''; }
        else {
          auth.error = auth.mfa && e.code === 'bad_code' ? tr('两步验证码不正确') : message(e);
          if (e.code === 'challenge_expired') { auth.mfa = false; mailCode.value = ''; }
          if (e.code === 'invite_required') auth.invite = true;
          if (e.code === 'pin_mismatch' || e.code === 'pin_required') auth.probe = null;
          if (e.code === 'bad_code') (auth.mfa ? totp : mailCode).value = '';
        }
      }
      auth.busy = false; render();
      if (auth.mfa) totp.focus();
      else if (auth.probe && !mailCode.value) mailCode.focus();
    }

    render();
    probe();
  }

  // ---- dashboard ----

  // A section re-renders only when its data changed, so that polling does not
  // disturb a text selection or a scroll position.
  // opts.hint: what the panel is for, shown when its "?" is clicked, so that the
  // window stays compact. opts.actions: buttons at the end of its title line.
  // opts.fill: the panel takes the room left in its view and its body scrolls.
  // opts.row: a panel of one line, its title and its content side by side.
  function section(id, title, opts = {}) {
    const body = h('div', { class: 'panel-body', id: id + '-body' });
    let last;
    const hint = opts.hint ? h('p', { class: 'small muted hint-text', id: id + '-hint', hidden: true }, opts.hint) : null;
    const hintBtn = hint ? h('button', { type: 'button', class: 'hint-btn', id: id + '-hint-btn', title: opts.hint, 'aria-label': tr('说明'), 'aria-expanded': 'false',
      onclick: () => { hint.hidden = !hint.hidden; hintBtn.setAttribute('aria-expanded', String(!hint.hidden)); } }, '?') : null;
    const el = h('section', { class: 'panel' + (opts.fill ? ' fill' : '') + (opts.row ? ' row' : ''), id },
      h('div', { class: 'panel-head' }, title ? h('h2', null, title) : null, hintBtn, h('span', { class: 'spacer' }), opts.actions ? h('span', { class: 'actions' }, opts.actions) : null),
      hint, body);
    return { el, set(key, ...nodes) { const k = JSON.stringify(key); if (k !== last) { last = k; body.replaceChildren(...nodes.flat().filter(Boolean)); } } };
  }

  function copyButton(getText, id = 'copy-endpoint', label = tr('复制')) {
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
      setTimeout(() => { b.textContent = label; }, 1500);
    } }, label);
    return b;
  }

  const STATE = {
    starting: ['warn', tr('正在连接网络…')],
    retrying: ['bad', tr('暂时连不上网络，正在重试')],
    running: ['ok', tr('已连接')],
  };
  const JOB_STATUS = { held: tr('进行中'), pending: tr('待最终确认'), disputed: tr('争议中'), final: tr('已完成'), released: tr('已释放'), refunded: tr('已退款') };

  // A dialog: polling never touches it, so typing is not disturbed.
  function feedbackDialog() {
    const kind = h('select', { id: 'feedback-kind' },
      h('option', { value: 'bug' }, tr('问题')), h('option', { value: 'idea' }, tr('建议')), h('option', { value: 'other' }, tr('其他')));
    const text = h('textarea', { id: 'feedback-text', rows: '4', maxlength: '4000', dir: 'auto' });
    const line = h('div', { id: 'feedback-result', role: 'status' });
    const send = h('button', { class: 'primary', type: 'submit', id: 'feedback-send' }, tr('提交反馈'));
    const say = (cls, msg) => { line.className = 'notice small ' + cls; line.textContent = msg; };
    modal('dlg-feedback', tr('反馈'),
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
  // explain turns an API error into a sentence (shareError when not given).
  async function run(btn, err, fn, explain) {
    if (btn.disabled) return;
    btn.disabled = true;
    err.say('');
    try { await fn(); }
    catch (e) { err.say(e instanceof ApiError ? (explain || shareError)(e) : (e.message || String(e))); }
    finally { btn.disabled = false; }
  }
  // Asks before something is removed: fn does it, after follows a success.
  function confirmBox(text, label, fn, after, explain) {
    const err = errorBox('confirm-error');
    const okBtn = h('button', { type: 'button', class: 'danger', id: 'confirm-ok', onclick: () => run(okBtn, err, async () => { await fn(); closeModal(); await after(); }, explain) }, label);
    modal('dlg-confirm', tr('请确认'), h('p', { id: 'confirm-text' }, text), err.el,
      h('div', { class: 'modal-actions' }, h('button', { type: 'button', id: 'confirm-cancel', onclick: () => closeModal() }, tr('取消')), okBtn));
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
    const state = section('share-state', tr('共享状态'), { hint: tr('开启后，这台设备用下面配置的模型为网络上的买家服务，收益计入你的账户。') });
    const provs = section('providers', tr('厂商 API'), { fill: true,
      hint: tr('把你在模型厂商开通的 API 或订阅提供给网络。买家的提示词会以明文发给厂商，用量记在你的厂商账户上。'),
      actions: h('button', { type: 'button', id: 'provider-add', onclick: () => providerDialog(null) }, tr('添加厂商')) });
    const local = section('local-models', tr('本机模型服务'), { fill: true,
      hint: tr('共享这台电脑上的模型服务（如 Ollama），它需要提供 OpenAI 兼容接口。'),
      actions: [h('button', { type: 'button', id: 'local-add-model', onclick: () => modelDialog(null, null) }, tr('添加模型')),
        h('button', { type: 'button', id: 'local-settings', onclick: () => localDialog() }, tr('设置'))] });
    const relays = section('share-relays', tr('中继预约'), { fill: true });
    const soldJobs = section('sold-jobs', tr('最近卖出的作业'), { fill: true });
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
        ui.error ? h('div', { class: 'notice bad', id: 'share-error', role: 'alert' }, ui.error) : null,
        !s.enabled && n === 0 && !ui.error ? h('p', { class: 'small muted', id: 'share-start-hint' }, tr('先添加厂商 API 或本机模型，再开启共享。')) : null,
        !s.enabled && n > 0 ? h('div', { class: 'notice warn', id: 'share-hint' }, tr('已配置 {n} 个模型，开启共享后开始接单。', { n: num(n) })) : null,
        s.running ? h('div', { class: 'tiles', id: 'share-stats' },
          tile('share-tile-models', tr('在售模型'), num((s.serving || []).length)),
          tile('share-tile-jobs', tr('已完成作业'), num(stats.jobs_served), tr('失败：{n}', { n: num(stats.jobs_failed) })),
          tile('share-tile-active', tr('进行中'), num(stats.active_jobs) + ' / ' + num(s.concurrency)),
          tile('share-tile-earned', tr('本次运行收益'), credit(stats.earned_micros), stats.last_announce ? tr('最近报价：{time}', { time: clock(stats.last_announce) }) : null),
          acct ? tile('share-tile-total', tr('卖出收益'), credit(acct.usage.sold.micros), tr('{n} 次，{tokens} tokens', { n: num(acct.usage.sold.jobs), tokens: num(acct.usage.sold.prompt_tokens + acct.usage.sold.completion_tokens) })) : null) : null,
        stats.announce_error ? h('div', { class: 'notice warn small' }, tr('向协调器报价失败：{error}', { error: stats.announce_error })) : null,
        (st.warnings || []).map(w => h('div', { class: 'notice warn small share-warning' }, w)));

      provs.set([ps, [...serving], ui.flash, marketKey()],
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
        { title: tr('延迟'), num: true, get: r => rtt((r.rtt_ns || 0) / 1e6) },
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
      confirmBox(text, label, fn, async () => { ui.flash = null; await settle(); });
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
    // The panels by tab; the jobs sold go with the rest of the activity.
    return { panes: { state: [state.el, relays.el], providers: [provs.el], local: [local.el] }, sold: soldJobs.el,
      update(next) { st = next; render(); }, account(me, jobs) { acct = me; sold = jobs || []; render(); } };
  }

  // ---- pairing ----

  // Pairing ties this account, as a buyer, to the sellers whose codes it
  // redeemed, and as a seller to the buyers that redeemed its codes. The portal
  // keeps it; the daemon passes the requests on. Two panels: the sellers go on
  // the overview, the codes and the buyers on the sharing view.
  function pairingView() {
    let view = null, error = '';
    const flash = { sellers: null, buyers: null };
    const sellers = section('paired-sellers', tr('配对的卖家'), { fill: true,
      hint: tr('输入卖家给你的配对码。配对后，这个账户只向配对的卖家购买。'),
      actions: h('button', { type: 'button', id: 'pair-join', onclick: () => joinDialog() }, tr('输入配对码')) });
    const buyers = section('pairing', tr('配对码'), { fill: true,
      hint: tr('生成配对码发给你的买家。对方输入后，他的账户只向和他配对的卖家购买。没有配对的买家仍然可以向你购买。'),
      actions: h('button', { type: 'button', id: 'pair-new-code', onclick: () => codeDialog() }, tr('生成配对码')) });

    function pairError(e) {
      if (e.code === 'bad_code') return tr('配对码不对，或者已经过期、被撤销或用完');
      if (e.code === 'own_code') return tr('这是你自己的配对码，请把它发给要配对的买家');
      if (e.code === 'limit') return tr('已达到数量上限，请先解除或撤销一些');
      if (e.code === 'not_found') return tr('这一项已经不存在了');
      if (e.code === 'not_logged_in') return tr('请先登录');
      return message(e);
    }

    async function reload() {
      try { view = await api('GET', '/api/v1/pairing'); error = ''; }
      catch (e) { error = pairError(e); }
      render();
    }

    const who = u => [u.username, u.disabled ? h('span', { class: 'pill bad pair-off' }, tr('已停用')) : null];
    const uses = c => c.max_uses ? num(c.used) + ' / ' + num(c.max_uses) : tr('{n}（不限）', { n: num(c.used) });
    const failed = () => error ? h('div', { class: 'notice warn small pair-error' }, tr('暂时获取不到配对信息：{error}', { error })) : null;
    const flashLine = (f, id) => f ? h('div', { class: 'notice ' + f.cls, id }, f.text) : null;

    function render() {
      const v = view || { sellers: [], buyers: [], codes: [], restricted: false, private: false, max_sellers: 0 };
      sellers.set([v.sellers, v.restricted, v.max_sellers, error, flash.sellers, !view],
        failed(),
        flashLine(flash.sellers, 'pair-join-flash'),
        v.restricted ? h('div', { class: 'notice warn small', id: 'pair-restricted' }, tr('这个账户现在只向下面的卖家购买，「可用模型」也只列出他们的模型。全部解除后，恢复为向整个网络购买。')) : null,
        v.max_sellers ? h('p', { class: 'small muted', id: 'pair-max' }, tr('最多同时配对 {n} 个卖家。', { n: num(v.max_sellers) })) : null,
        v.sellers.length === 0 ? h('div', { class: 'empty', id: 'pair-sellers-empty' }, tr('还没有配对卖家，可以向网络上所有卖家购买')) : table([
          { title: tr('卖家'), get: u => who(u) },
          { title: tr('备注'), get: u => u.label || '—' },
          { title: tr('配对时间'), get: u => time(u.since) },
          { title: '', get: u => h('button', { type: 'button', class: 'mini danger pair-leave', 'data-id': String(u.id), onclick: () => leave(u) }, tr('解除')) },
        ], v.sellers));

      buyers.set([v.buyers, v.codes, v.private, error, flash.buyers, !view],
        failed(),
        flashLine(flash.buyers, 'pair-code-flash'),
        privateToggle(v),
        v.codes.length === 0 ? h('div', { class: 'empty', id: 'pair-codes-empty' }, tr('没有有效的配对码')) : table([
          { title: tr('配对码'), get: c => h('span', { class: 'pair-code-cell' }, h('code', { class: 'pair-code' }, c.code), copyButton(() => c.code, 'pair-copy-' + c.id)) },
          { title: tr('备注'), get: c => c.label || '—' },
          { title: tr('已配对'), num: true, get: uses },
          { title: tr('到期时间'), get: c => time(c.expires_at) },
          { title: '', get: c => h('button', { type: 'button', class: 'mini danger pair-revoke', 'data-id': String(c.id), onclick: () => revoke(c) }, tr('撤销')) },
        ], v.codes),
        h('h3', { class: 'sub-title' }, tr('配对的买家')),
        v.buyers.length === 0 ? h('div', { class: 'empty', id: 'pair-buyers-empty' }, tr('还没有买家和你配对')) : table([
          { title: tr('买家'), get: u => who(u) },
          { title: tr('备注'), get: u => u.label || '—' },
          { title: tr('配对时间'), get: u => time(u.since) },
          { title: '', get: u => h('button', { type: 'button', class: 'mini danger pair-remove', 'data-id': String(u.id), onclick: () => remove(u) }, tr('移除')) },
        ], v.buyers));
    }

    // Private mode: this account, as a seller, serves only its paired buyers.
    function privateToggle(v) {
      const box = h('input', { type: 'checkbox', id: 'pair-private', checked: v.private, disabled: !view, onchange: async () => {
        box.disabled = true;
        flash.buyers = null;
        try { await api('PUT', '/api/v1/pairing/private', { enabled: box.checked }); }
        catch (e) { flash.buyers = { cls: 'bad', text: pairError(e) }; box.checked = v.private; }
        box.disabled = false;
        await reload();
      } });
      return h('label', { class: 'check pair-private' }, box, h('span', null,
        h('strong', null, tr('私有模式')), ' ',
        h('span', { class: 'small muted' }, tr('只为和你配对的买家服务。开启后，其他买家看不到你的模型，也不能向你购买。'))));
    }

    function joinDialog() {
      const code = h('input', { type: 'text', id: 'pair-code-input', autocapitalize: 'characters', autocomplete: 'off', spellcheck: 'false', maxlength: '64', placeholder: 'XXXX-XXXX-XXXX-XXXX', class: 'mono' });
      const err = errorBox('pair-join-error');
      const okBtn = h('button', { class: 'primary', type: 'submit', id: 'pair-join-ok' }, tr('配对'));
      modal('dlg-pair-join', tr('输入配对码'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(okBtn, err, async () => {
        if (!code.value.trim()) throw new Error(tr('请输入配对码'));
        const seller = await api('POST', '/api/v1/pairing/redeem', { code: code.value.trim() });
        closeModal();
        flash.sellers = { cls: 'ok', text: tr('已和卖家 {name} 配对', { name: seller.username }) };
        await reload();
      }, pairError); } },
        field(tr('配对码'), code, tr('配对后，这个账户的所有设备都只向配对的卖家购买。随时可以解除。')),
        err.el, okBtn));
    }

    const TTLS = [[24, tr('1 天')], [168, tr('7 天')], [720, tr('30 天')]];
    function codeDialog() {
      const label = h('input', { type: 'text', id: 'pair-label', maxlength: '64', dir: 'auto' });
      const ttl = h('select', { id: 'pair-ttl' }, TTLS.map(([hours, text]) => h('option', { value: String(hours), selected: hours === 24 }, text)));
      const limit = h('input', { type: 'text', id: 'pair-uses', inputmode: 'numeric', placeholder: tr('不限') });
      const err = errorBox('pair-code-error');
      const okBtn = h('button', { class: 'primary', type: 'submit', id: 'pair-code-ok' }, tr('生成'));
      modal('dlg-pair-code', tr('生成配对码'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(okBtn, err, async () => {
        const n = limit.value.trim() === '' ? 0 : Number(limit.value.trim());
        if (!Number.isInteger(n) || n < 0 || n > 1000) throw new Error(tr('可配对的买家数需要是 1 到 1000 的整数，不填表示不限'));
        const c = await api('POST', '/api/v1/pairing/codes', { label: label.value.trim(), ttl_hours: Number(ttl.value), max_uses: n });
        flash.buyers = null;
        await reload();
        shownCode(c);
      }, pairError); } },
        field(tr('备注（配对的买家也会看到，可不填）'), label),
        field(tr('有效期'), ttl, tr('到期后不能再用它配对，已经配对的买家不受影响。')),
        field(tr('可配对的买家数'), limit, tr('不填表示不限。')),
        err.el, okBtn));
    }
    function shownCode(c) {
      modal('dlg-pair-shown', tr('配对码已生成'),
        h('p', { class: 'small muted' }, tr('把它发给要配对的买家。拿到它的人都能和你配对，请只发给你信任的人。')),
        h('div', { class: 'endpoint' }, h('code', { id: 'pair-shown-code', class: 'pair-code big' }, c.code), copyButton(() => c.code, 'pair-shown-copy')),
        h('p', { class: 'small muted', id: 'pair-shown-meta' }, tr('到期时间：{time}', { time: time(c.expires_at) })),
        h('div', { class: 'modal-actions' }, h('button', { type: 'button', class: 'primary', id: 'pair-shown-done', onclick: () => closeModal() }, tr('完成'))));
    }

    function leave(u) {
      confirmBox(tr('解除和卖家 {name} 的配对？之后这个账户不再向他购买，除非重新配对。', { name: u.username }), tr('解除'),
        () => api('DELETE', '/api/v1/pairing/sellers/' + u.id), async () => { flash.sellers = null; await reload(); }, pairError);
    }
    function remove(u) {
      confirmBox(tr('移除买家 {name}？如果这是他唯一配对的卖家，他会恢复为向整个网络购买。', { name: u.username }), tr('移除'),
        () => api('DELETE', '/api/v1/pairing/buyers/' + u.id), async () => { flash.buyers = null; await reload(); }, pairError);
    }
    function revoke(c) {
      confirmBox(tr('撤销配对码 {code}？它不能再用来配对，已经配对的买家不受影响。', { code: c.code }), tr('撤销'),
        () => api('DELETE', '/api/v1/pairing/codes/' + c.id), async () => { flash.buyers = null; await reload(); }, pairError);
    }

    render();
    return { sellers: sellers.el, buyers: buyers.el, reload };
  }

  // subTabs puts panels of one view behind a row of tabs: one shows at a time,
  // the first at the start. items: [name, label, panels].
  function subTabs(view, items) {
    const btns = {}, panes = {};
    function show(name) {
      for (const [n] of items) {
        panes[n].hidden = n !== name;
        btns[n].setAttribute('aria-selected', n === name ? 'true' : 'false');
      }
    }
    const bar = h('div', { class: 'seg', role: 'tablist', id: 'tabs-' + view, hidden: true }, items.map(([n, label, nodes]) => {
      panes[n] = h('div', { class: 'pane', id: 'pane-' + view + '-' + n }, nodes);
      return (btns[n] = h('button', { type: 'button', role: 'tab', id: 'tab-' + view + '-' + n, onclick: () => show(n) }, label));
    }));
    show(items[0][0]);
    return { bar, panes: Object.values(panes), show };
  }

  // The window after sign-in: a sidebar that chooses the view, the view, and a
  // status bar. Every view stays in the page, so that polling keeps the hidden
  // ones current and switching shows them at once.
  function showDashboard(session) {
    markEnv(session);
    stopPolling();
    let current = session;
    const pill = h('span', { class: 'pill', id: 'conn-state' });
    const relayStat = h('span', { id: 'status-relay' });
    const logoutBtn = h('button', { type: 'button', id: 'logout', onclick: logout }, tr('退出登录'));
    const who = h('span', { class: 'who', id: 'who' });
    const accountName = h('span', { class: 'v', id: 'account-name' });
    const banner = h('div', { id: 'banner' });
    const tiles = h('div', { class: 'tiles', id: 'tiles' });
    const endpoint = section('endpoint', tr('本地 OpenAI 接口'), { row: true, hint: tr('在支持 OpenAI 协议的工具里把 base URL 改成这个地址即可使用。') });
    const network = section('network', tr('网络'), { fill: true });
    const models = section('models', tr('可用模型'), { fill: true });
    const devices = section('devices', tr('我的设备'), { fill: true });
    const jobs = section('jobs', tr('最近作业'), { fill: true });
    const updateBox = h('div', { id: 'update' });
    const versionLabel = h('span', { id: 'app-version' });
    const versionText = h('span', { id: 'settings-version' });
    const checkResult = h('span', { class: 'small muted', id: 'update-check-result', role: 'status' });
    const checkBtn = h('button', { type: 'button', id: 'update-check', onclick: checkUpdate }, tr('检查更新'));
    // The endpoint's API key (internal/client/apikey.go): switched and made
    // anew in the settings, copied from the overview. It is fetched with the
    // account data, so a change made with `sharetoken apikey` shows up too.
    let apiKey = null, apiURL = '';
    const apiKeyBox = h('input', { type: 'checkbox', id: 'apikey-required', disabled: true, onchange: async () => {
      apiKeyBox.disabled = true;
      try { apiKey = await api('PUT', '/api/v1/apikey', { required: apiKeyBox.checked }); setBanner(''); }
      catch (e) { setBanner(message(e)); }
      apiKeyBox.disabled = false;
      drawAPIKey();
    } });
    const apiKeyText = h('code', { id: 'apikey-text' });
    const apiKeyNote = h('span', { class: 'small muted', id: 'apikey-note' });
    const apiKeyCopy = copyButton(() => apiKey ? apiKey.key : '', 'apikey-copy', tr('复制 key'));
    const rotateBtn = h('button', { type: 'button', id: 'apikey-rotate', onclick: () => confirmBox(
      tr('旧 key 会立即失效，用到它的工具要换成新 key。'), tr('重新生成'),
      async () => { apiKey = await api('POST', '/api/v1/apikey/rotate'); }, async () => drawAPIKey()) }, tr('重新生成'));
    function drawAPIKey() {
      if (!apiKey) return;
      apiKeyBox.checked = apiKey.required;
      apiKeyBox.disabled = false;
      apiKeyText.textContent = apiKey.key.slice(0, 6) + '…' + apiKey.key.slice(-4);
      apiKeyNote.textContent = apiKey.required ? tr('工具把它填作 OpenAI API key') : tr('未开启：本机任何程序都能用这个接口花你的 credit');
      apiKeyNote.className = 'small ' + (apiKey.required ? 'muted' : 'warn-text');
      drawEndpoint();
    }
    function drawEndpoint() {
      if (!apiURL) return;
      const req = !!(apiKey && apiKey.required);
      endpoint.set([apiURL, req, req && apiKey.key], h('div', { class: 'endpoint' }, h('code', { id: 'endpoint-url' }, apiURL), copyButton(() => apiURL),
        req ? copyButton(() => apiKey.key, 'copy-apikey', tr('复制 API key')) : null));
    }
    async function loadAPIKey() {
      try { apiKey = await api('GET', '/api/v1/apikey'); drawAPIKey(); } catch (e) { /* shown again at the next try */ }
    }

    const settings = h('section', { class: 'panel', id: 'settings' }, h('div', { class: 'rows' },
      h('span', { class: 'k' }, tr('账户')), accountName, logoutBtn,
      h('span', { class: 'k' }, tr('API key')), h('label', { class: 'v check' }, apiKeyBox, h('span', null, tr('调用本地 OpenAI 接口需要 API key'))), apiKeyCopy,
      h('span', { class: 'k' }), h('span', { class: 'v' }, apiKeyText, ' ', apiKeyNote), rotateBtn,
      h('span', { class: 'k' }, tr('语言')), h('span', { class: 'v' }), I18N.picker() || h('span'),
      h('span', { class: 'k' }, tr('版本')), h('span', { class: 'v' }, versionText, ' ', checkResult), checkBtn,
      h('span', { class: 'k' }, tr('反馈')), h('span', { class: 'v muted' }, tr('告诉我们你遇到的问题或建议。')),
      h('button', { type: 'button', id: 'settings-feedback', onclick: () => feedbackDialog() }, tr('提交反馈'))));
    let market = [];
    const pairing = pairingView();
    const share = shareView(() => market);

    // name, label, panels, or tabs of panels.
    const VIEWS = [
      ['overview', tr('概览'), [tiles, endpoint.el, h('div', { class: 'cols' }, network.el, models.el)]],
      ['share', tr('共享'), null, [['state', tr('状态'), share.panes.state], ['providers', tr('厂商 API'), share.panes.providers], ['local', tr('本机模型'), share.panes.local]]],
      ['pairing', tr('配对'), null, [['sellers', tr('我的卖家'), [pairing.sellers]], ['buyers', tr('我的买家'), [pairing.buyers]]]],
      ['activity', tr('记录'), null, [['all', tr('全部'), [jobs.el]], ['sold', tr('卖出'), [share.sold]]]],
      ['settings', tr('设置'), [settings, devices.el]],
    ];
    const views = {}, navButtons = {}, tabs = {}, labels = {};
    const title = h('h1', { id: 'view-title' });
    const head = h('header', { class: 'head' }, title);
    for (const [name, label, panels, tabbed] of VIEWS) {
      if (tabbed) { tabs[name] = subTabs(name, tabbed); head.append(tabs[name].bar); }
      labels[name] = label;
      views[name] = h('div', { class: 'view', id: 'view-' + name, hidden: true }, tabbed ? tabs[name].panes : panels);
      navButtons[name] = h('button', { type: 'button', role: 'tab', id: 'nav-' + name, onclick: () => showView(name) }, icon(name), h('span', null, label));
    }
    // tab: the tab of the view to show, when it has tabs.
    function showView(name, tab) {
      if (!views[name]) name = 'overview';
      for (const k of Object.keys(views)) {
        views[k].hidden = k !== name;
        navButtons[k].setAttribute('aria-selected', k === name ? 'true' : 'false');
        if (tabs[k]) tabs[k].bar.hidden = k !== name;
      }
      if (tab && tabs[name]) tabs[name].show(tab);
      title.textContent = labels[name];
      try { localStorage.setItem('st_view', name); } catch (e) { /* this visit only */ }
    }
    app.replaceChildren(h('div', { class: 'shell' },
      h('aside', { class: 'side' },
        h('img', { class: 'brand-logo', src: '/ui/favicon.svg', alt: 'ShareToken', title: 'ShareToken' }),
        h('nav', { class: 'nav', role: 'tablist' }, ['overview', 'share', 'pairing', 'activity'].map(n => navButtons[n])),
        h('div', { class: 'side-foot' },
          h('button', { type: 'button', id: 'nav-feedback', onclick: () => feedbackDialog() }, icon('feedback'), h('span', null, tr('反馈'))),
          navButtons.settings)),
      h('main', { class: 'main' }, head, h('div', { class: 'notices' }, updateBox, banner), Object.values(views)),
      h('footer', { class: 'statusbar' }, pill, relayStat, h('span', { class: 'spacer' }), who, versionLabel)));
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
      t.addEventListener('click', () => showView('share', 'state'));
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
          tile('tile-held', tr('冻结中'), credit(b.held_micros), null, tr('请求结束后多余部分退回')),
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

    // The status bar names the relay in use and how fast it answers.
    let lastRelay = '';
    function setRelayStat(r, ms) {
      const key = r ? (r.name || r.addr) + '|' + Math.round(ms || 0) : '';
      if (key === lastRelay) return;
      lastRelay = key;
      relayStat.replaceChildren(...(r ? [h('span', null, tr('中继')), h('span', { id: 'status-relay-name', title: r.addr }, r.name || shortPeer(r.addr)), rtt(ms)] : []));
    }

    function renderNetwork(st) {
      const c = st.coordinator;
      const q = c.link && c.link.samples ? c.link : null;
      // Under 10 ms a whole number would say 0.
      const ms1 = v => { v = Number(v || 0); return v > 0 && v < 10 ? Math.round(v * 10) / 10 : Math.round(v); };
      const link = q ? [ms1(q.avg_ms), ms1(q.jitter_ms), Math.round(q.loss_pct || 0)] : null;
      const cur = (st.relays || []).find(r => r.current);
      // The live pings of the relay in use say more than the last survey of all relays.
      setRelayStat(cur, link && link[0] ? link[0] : cur && !cur.error ? cur.rtt_ms : 0);
      network.set([st.relays, c.connected, c.via, st.warnings, link],
        h('div', { class: 'kv' },
          h('span', { class: 'pill ' + (c.connected ? 'ok' : 'bad'), id: 'coord-line' }, c.connected ? tr('协调器已连接') : tr('协调器未连接')),
          link ? h('span', { class: 'small', id: 'link-line' }, I18N.parts('链路质量：平均 {avg} ms · 抖动 {jitter} ms · 丢包 {loss}%', {
            avg: h('strong', { class: link[0] ? rttClass(link[0]) + ' rtt-text' : null }, String(link[0])), jitter: String(link[1]),
            loss: h('strong', { class: link[2] >= 5 ? 'bad rtt-text' : link[2] > 0 ? 'warn rtt-text' : null }, String(link[2])) })) : null),
        (st.warnings || []).map(w => h('div', { class: 'notice warn small' }, w)),
        !st.relays || st.relays.length === 0 ? h('div', { class: 'empty' }, tr('门户还没有登记中继')) : table([
          { title: tr('中继'), get: r => r.name || shortPeer(r.addr) },
          { title: tr('地区'), get: r => r.region || '—' },
          { title: tr('延迟'), num: true, get: r => r.error ? h('span', { class: 'rtt bad', title: r.error }, tr('不可达')) : rtt(r.rtt_ms) },
          { title: '', get: r => [r.current ? h('span', { class: 'badge accent' }, tr('当前使用')) : null, r.reserved ? h('span', { class: 'badge' }, tr('已预约')) : null].filter(Boolean) },
        ], st.relays));
    }

    function renderNetworkDown(s) {
      const t = s.state === 'retrying' ? (s.error || tr('连接失败，正在重试')) : tr('正在连接…');
      network.set(['down', t], h('div', { class: 'empty', id: 'network-down' }, t));
      models.set(['down'], h('div', { class: 'empty' }, tr('连接网络后显示')));
      share.update(null);
      setBrief(null);
      setRelayStat(null);
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
      who.textContent = accountName.textContent = s.user || '';
      who.title = s.user || '';
      const [cls, label] = STATE[s.state] || ['', s.state];
      pill.className = 'pill ' + cls;
      pill.textContent = label;
      apiURL = s.api;
      drawEndpoint();
    }

    // What a new release means for this client: one line over the views (the
    // version and the release's own sentence about itself), and a dialog with
    // the release notes and the download. Links show their address: not every
    // platform's window can open them, and then they can be copied. The line
    // is rebuilt only when the offer changes, so a click is not lost.
    const web = x => typeof x === 'string' && /^https?:\/\//.test(x) ? x : '';
    // The notes in the page's language, else in English, else in any.
    const notesOf = u => (u.notes && (u.notes[I18N.lang] || u.notes.en || Object.values(u.notes)[0])) || null;
    const updateTitle = u => u.required ? tr('这个版本已不再受支持，请升级到最新版本后继续使用') : tr('有新版本 {version} 可用', { version: u.latest || '' });
    let shownUpdate = '';
    function renderUpdate(s) {
      const u = s.update || {};
      versionLabel.textContent = tr('版本 {version}', { version: s.version || '' });
      versionText.textContent = s.version || '';
      const show = u.available && (u.required || !u.skipped);
      const n = notesOf(u);
      const sig = show ? u.key + '|' + u.required + '|' + (n ? n.summary : '') : '';
      if (sig === shownUpdate) return;
      shownUpdate = sig;
      if (!show) { updateBox.replaceChildren(); return; }
      updateBox.replaceChildren(h('div', { class: 'notice strip ' + (u.required ? 'bad' : 'warn'), id: 'update-notice' },
        h('span', { class: 'strip-text' }, h('strong', { id: 'update-text' }, updateTitle(u)),
          n && n.summary ? [' · ', h('span', { id: 'update-summary', title: n.summary, dir: 'auto' }, n.summary)] : null),
        canInstall(u) ? h('button', { type: 'button', class: 'mini accent', id: 'update-install', onclick: () => updateDialog(u, true) }, tr('立即更新')) : null,
        h('button', { type: 'button', class: 'mini', id: 'update-open', onclick: () => updateDialog(u) }, tr('查看详情')),
        !u.required && u.key ? h('button', { type: 'button', class: 'link small', id: 'update-skip', onclick: async () => {
          try {
            const next = await api('POST', '/api/v1/update/skip', { key: u.key });
            renderUpdate(Object.assign({}, s, { update: next }));
          } catch (e) { setBanner(message(e)); }
        } }, tr('跳过此版本')) : null));
    }

    // The desktop window installs a signed package by itself (gui/update.go):
    // its shell says so on the page, and the release offers the file.
    const canInstall = u => document.documentElement.dataset.shellUpdate === '1' && !!(u && u.install && u.key);

    function updateDialog(u, start) {
      const n = notesOf(u);
      const dl = (u.package && web(u.package.url)) || web(u.download_url), notes = web(u.notes_url);
      const progress = h('div', { class: 'small', id: 'update-progress', role: 'status' });
      const installBtn = canInstall(u) ? h('button', { type: 'button', class: 'accent', id: 'update-now', onclick: () => installUpdate(u, installBtn, progress) }, tr('立即更新并重启')) : null;
      modal('dlg-update', u.required ? tr('需要升级') : tr('新版本 {version}', { version: u.latest || '' }),
        u.required ? h('div', { class: 'notice bad', id: 'update-required' }, tr('升级前不能调用模型或接单，已有作业照常结算。')) : null,
        n && n.body ? richText(n.body, 'update-body') : h('p', { class: 'muted', id: 'update-no-notes' }, updateTitle(u)),
        dl ? h('div', { class: 'update-link' }, h('span', { class: 'muted' }, tr('下载')), h('a', { id: 'update-link', href: dl, target: '_blank', rel: 'noopener' }, dl), copyButton(() => dl, 'update-copy')) : null,
        progress,
        h('div', { class: 'modal-actions' },
          notes ? h('a', { id: 'update-notes', href: notes, target: '_blank', rel: 'noopener' }, tr('更新内容')) : null,
          h('span', { class: 'spacer' }),
          h('button', { type: 'button', id: 'update-close', onclick: () => closeModal() }, tr('关闭')),
          installBtn));
      if (start && installBtn) installUpdate(u, installBtn, progress);
    }

    // installUpdate asks the window's shell to fetch, check and install the
    // offered build, and follows it until the app restarts or it fails.
    async function installUpdate(u, btn, progress) {
      btn.disabled = true;
      progress.className = 'small';
      progress.textContent = tr('正在准备更新…');
      try {
        await api('POST', '/__shell/update', { key: u.key });
      } catch (e) {
        progress.className = 'small bad';
        progress.textContent = tr('更新失败：{error}', { error: message(e) });
        btn.disabled = false;
        return;
      }
      for (;;) {
        await new Promise(r => setTimeout(r, 400));
        let st;
        try { st = await api('GET', '/__shell/update'); } catch (e) { continue; } // the service stops before the restart
        if (st.state === 'downloading') {
          progress.textContent = st.total > 0 ? tr('正在下载 {pct}%', { pct: Math.floor(st.written * 100 / st.total) }) : tr('正在下载…');
        } else if (st.state === 'installing') {
          progress.textContent = tr('已校验签名，正在安装…');
        } else if (st.state === 'restarting') {
          progress.textContent = tr('正在重启…');
        } else if (st.state === 'failed') {
          progress.className = 'small bad';
          progress.textContent = tr('更新失败：{error}', { error: st.error || '' });
          btn.disabled = false;
          return;
        }
      }
    }

    async function checkUpdate() {
      checkBtn.disabled = true;
      checkResult.textContent = tr('正在检查…');
      try {
        const u = await api('POST', '/api/v1/update/check');
        renderUpdate(Object.assign({}, current, { update: u }));
        checkResult.textContent = u.available ? '' : tr('已是最新版本');
        if (u.available) updateDialog(u);
      } catch (e) { checkResult.textContent = message(e); }
      checkBtn.disabled = false;
    }

    function setBanner(text) {
      if (banner.textContent === text) return;
      banner.replaceChildren(...(text ? [h('div', { class: 'notice warn small', id: 'banner-text' }, text)] : []));
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
          loadAPIKey();
          try {
            const [me, j, sj] = await Promise.all([api('GET', '/api/v1/account'), api('GET', '/api/v1/jobs?limit=10'), api('GET', '/api/v1/jobs?role=sell&limit=10')]);
            if (stopped) return;
            renderAccount(me);
            renderJobs(j.jobs);
            share.account(me, sj.jobs);
            setBanner('');
            await pairing.reload();
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
          if (i === 0) app.replaceChildren(h('div', { class: 'boot' }, h('div', { class: 'notice bad', id: 'boot-error' }, message(e))));
          await new Promise(r => setTimeout(r, 2000));
        }
      }
    }
    if (s.logged_in) showDashboard(s);
    else showAuth(s, s.state === 'expired' ? tr('登录已失效（可能已在其他地方退出或被吊销），请重新登录。') : '');
  }

  boot();
});
