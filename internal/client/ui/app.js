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
    // An invite link the app was opened with waits for the sign-in.
    const inviteNote = h('div', { class: 'notice small', id: 'auth-invite', hidden: !session.invite },
      tr('你收到了一个邀请。登录或注册后，会请你确认是否接受。'));
    const checkInvite = async () => {
      try { const s = await api('GET', '/api/v1/session'); inviteNote.hidden = !s.invite; } catch (e) { /* stays as it was */ }
    };
    window.addEventListener('focus', checkInvite);
    // A window brought forward may not get the focus (GNOME on Wayland keeps it
    // where it was), so look now and then too; the dashboard's stopPolling ends it.
    poll = setInterval(checkInvite, 3000);
    app.replaceChildren(h('div', { class: 'auth' },
      h('div', { class: 'auth-side' }, h('img', { class: 'auth-logo', src: '/ui/favicon.svg', alt: '' }), h('h1', null, 'ShareToken'), h('p', { class: 'tagline' }, tr('共享和使用模型算力')), inviteNote),
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
  // A subscription's usage windows in a line, as the warm-up and the
  // providers' answers read them (internal/provider/limits.go).
  function usageText(u) {
    if (!u || !u.windows || !u.windows.length) return '—';
    const now = Date.now();
    const at = d => new Date(d).toLocaleString(I18N.lang, d - now > 86400e3
      ? { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' } : { hour12: false, hour: '2-digit', minute: '2-digit' });
    return u.windows.map(w => {
      const r = w.resets_at ? Date.parse(w.resets_at) : 0;
      const name = w.name === '5h' ? tr('5 小时') : w.name === '7d' ? tr('周') : w.name;
      if (r && r <= now) return tr('{name} 已重置', { name });
      const pct = Math.round(w.used) + '%';
      return r ? tr('{name} {pct}，{time} 重置', { name, pct, time: at(r) }) : name + ' ' + pct;
    }).join(' · ');
  }

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
              h('button', { type: 'button', class: 'mini prov-options', onclick: () => providerSettings(p) }, tr('连接设置')),
              h('button', { type: 'button', class: 'mini prov-renew', onclick: () => providerDialog(p) }, tr('更换凭据')),
              h('button', { type: 'button', class: 'mini danger prov-remove', onclick: () => removeProvider(p) }, tr('删除')))),
          h('div', { class: 'kv small prov-meta' },
            h('span', { class: 'mono' }, p.base_url),
            p.token_expires ? h('span', null, p.can_refresh ? tr('令牌有效期至 {time}（会自动续期）', { time: clock(p.token_expires) }) : tr('令牌有效期至 {time}（需重新登录续期）', { time: clock(p.token_expires) })) : null,
            p.runtime?.retry_at && new Date(p.runtime.retry_at) > new Date() ? h('span', null, tr('上游限流，{time} 后恢复', { time: clock(p.runtime.retry_at) })) : null,
            p.headers && p.headers.length ? h('span', null, tr('附加请求头：{names}', { names: p.headers.join(', ') })) : null,
            p.warm_capable && p.usage ? h('span', { class: 'prov-usage' }, tr('用量：{text}', { text: usageText(p.usage) })) : null,
            p.warm_capable ? h('span', { class: 'prov-warmup', title: tr('在“设置 → 预热”里修改') }, p.warmup && (p.warmup.times || []).length ? tr('预热：每天 {times}', { times: p.warmup.times.join(' ') })
              : p.warmup && p.warmup.on_reset ? tr('预热：窗口重置时') : tr('预热：未开启')) : null),
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
        { title: 'Credit', num: true, get: j => j.shared ? h('span', { class: 'pill ok job-shared' }, tr('伙伴共享')) : '+' + credit(j.earned_micros, 6) },
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

    function providerOptions(old) {
      const proxyMode = h('select', { id: 'pv-proxy-mode' }, [
        ...(old ? [h('option', { value: 'keep' }, tr('保留现有代理设置'))] : []),
        h('option', { value: 'environment' }, tr('使用环境代理')),
        h('option', { value: 'direct' }, tr('直接连接')),
        h('option', { value: 'custom' }, tr('指定代理'))]);
      const proxyURL = h('input', { type: 'password', id: 'pv-proxy-url', autocomplete: 'off', placeholder: old?.proxy_address || 'http://127.0.0.1:8080' });
      const profile = h('select', { id: 'pv-profile' }, h('option', { value: 'standard' }, tr('标准请求')), h('option', { value: 'claude-code' }, tr('Claude Code OAuth 兼容请求')));
      const tlsProfile = h('select', { id: 'pv-tls' }, h('option', { value: 'native' }, tr('原生 TLS')), h('option', { value: 'chrome' }, tr('浏览器 TLS')));
      const tokenTLS = h('select', { id: 'pv-token-tls' }, h('option', { value: 'native' }, tr('原生 TLS')), h('option', { value: 'chrome' }, tr('浏览器 TLS')));
      tlsProfile.value = old?.tls_profile || 'native';
      tokenTLS.value = old?.token_tls_profile || 'native';
      const sampling = h('select', { id: 'pv-sampling' }, h('option', { value: 'strict' }, tr('拒绝不支持的采样参数')), h('option', { value: 'default' }, tr('使用模型默认采样')));
      profile.value = old?.anthropic?.profile || 'standard';
      sampling.value = old?.anthropic?.sampling || 'strict';
      const proxyField = field(tr('代理地址（可含认证信息）'), proxyURL);
      const profileField = field(tr('Anthropic 请求方式'), profile);
      const samplingField = field(tr('采样参数处理'), sampling);
      const tokenTLSField = field(tr('登录与续期 TLS'), tokenTLS);
      const el = h('div', { class: 'provider-options' }, field(tr('上游连接'), proxyMode), proxyField, field(tr('推理连接 TLS'), tlsProfile), tokenTLSField, profileField, samplingField);
      const refreshProxy = () => { proxyField.hidden = proxyMode.value !== 'custom'; };
      proxyMode.addEventListener('change', refreshProxy); refreshProxy();
      return { el, show(kind, auth) {
        profileField.hidden = kind !== 'anthropic' || auth !== 'oauth';
        tokenTLSField.hidden = auth !== 'oauth';
        samplingField.hidden = kind !== 'anthropic';
      }, read(kind, auth) {
        const out = {};
        if (proxyMode.value !== 'keep') out.transport = { proxy_mode: proxyMode.value, proxy_url: proxyMode.value === 'custom' ? proxyURL.value.trim() : '' };
        out.transport = { ...out.transport, tls_profile: tlsProfile.value, token_tls_profile: tokenTLS.value };
        if (kind === 'anthropic') out.anthropic = { profile: auth === 'oauth' ? profile.value : 'standard', sampling: sampling.value };
        return out;
      }};
    }

    function providerSettings(p) {
      const options = providerOptions(p), err = errorBox('pv-settings-error');
      options.show(p.type, p.auth);
      const save = h('button', { type: 'submit', class: 'primary', id: 'pv-settings-save' }, tr('保存'));
      modal('dlg-provider-settings', tr('连接设置'), h('form', { onsubmit: e => {
        e.preventDefault();
        run(save, err, async () => {
          const req = options.read(p.type, p.auth);
          if (Object.keys(req).length) await api('PATCH', providerPath(p.name), req);
          closeModal(); await reload();
        });
      } }, options.el, err.el, save));
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
      const options = providerOptions(old);
      const loginMode = h('select', { id: 'pv-login-mode' }, h('option', { value: 'full' }, tr('完整授权')), h('option', { value: 'inference' }, tr('仅推理授权')));
      loginMode.value = old?.login_mode || 'full';
      const loginMethod = h('select', { id: 'pv-login-method' }, h('option', { value: 'browser' }, tr('浏览器授权')), h('option', { value: 'session' }, tr('sessionKey 登录')));
      const sessionKey = h('input', { type: 'password', id: 'pv-session-key', autocomplete: 'off' });
      const organization = h('select', { id: 'pv-organization' });
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
        ui.flash = c.warning ? { cls: 'warn', text: c.warning } : c.unusable ? { cls: 'warn', text: tr('凭据有效，但账户余额不足，暂时无法完成请求') }
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
        const req = { type: k.id, name: chosenName(), api_key: key.value.trim(), base_url: url.value.trim(), no_check: noCheck.checked, ...options.read(k.id, 'apikey') };
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
        const req = { type: type.value, name: chosenName(), project: project.value.trim(), mode: type.value === 'anthropic' ? loginMode.value : '', ...options.read(type.value, 'oauth') };
        if (type.value === 'anthropic' && loginMethod.value === 'session') {
          if (!sessionKey.value.trim()) throw new Error(tr('请填写 sessionKey'));
          login = { ...await api('POST', '/api/v1/providers/oauth/session/start', { ...req, session_key: sessionKey.value.trim() }), session: true };
          organization.replaceChildren(...login.organizations.map(o => h('option', { value: o.uuid }, o.name + ' (' + o.uuid + ')')));
        } else login = await api('POST', '/api/v1/providers/oauth/start', req);
        draw();
        (login.session ? organization : code).focus();
      });
      const finishLogin = () => run(finishBtn, err, async () => {
        if (!login.session && !code.value.trim()) throw new Error(tr('请粘贴授权码或页面地址'));
        const out = login.session
          ? await api('POST', '/api/v1/providers/oauth/session/finish', { id: login.id, session_key: sessionKey.value.trim(), organization: organization.value, name: chosenName() })
          : await api('POST', '/api/v1/providers/oauth/finish', { id: login.id, code: code.value.trim(), name: chosenName() });
        code.value = ''; sessionKey.value = '';
        await added(out);
      });

      function draw() {
        const k = kind();
        if (!(auth === 'apikey' && k.apikey) && !(auth === 'oauth' && k.oauth)) auth = k.apikey ? 'apikey' : 'oauth';
        options.show(k.id, auth);
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
            advanced, options.el,
          ] : [
            h('div', { class: 'notice warn small', id: 'pv-oauth-risk' }, tr('这是用厂商官方客户端的方式登录你的订阅账号。把订阅额度提供给他人使用可能违反厂商条款，账号有被封的风险，请先确认条款允许。')),
            !login ? options.el : null,
            !login && k.id === 'anthropic' ? [field(tr('登录方式'), loginMethod), field(tr('授权范围'), loginMode), loginMethod.value === 'session' ? field('sessionKey', sessionKey) : null] : null,
            !login ? (k.id === 'gemini' || k.id === 'antigravity' ? field(tr('Google Cloud 项目 ID（一般不用填）'), project) : null) : login.session ? field(tr('选择使用的组织'), organization) : [
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
      loginMethod.addEventListener('change', () => { login = null; err.say(''); draw(); });
      type.addEventListener('change', () => { login = null; sessionKey.value = ''; err.say(''); draw(); });
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

  // ---- invite links ----

  // Every pairing code and share invite also has a link on the portal this
  // app uses (/p#… or /s#…): it works for someone who has no app yet, and it
  // opens in this app once installed (sharetoken://, the shell). A code or
  // invite may ask its owner to approve whoever uses it.
  let portalBase = '';
  const inviteLink = (kind, token) => portalBase.replace(/\/+$/, '') + '/' + kind + '#' + token;
  function inviteText(kind, c) {
    const vars = { link: inviteLink(kind, c.link_token), code: c.code, time: time(c.expires_at) };
    return kind === 'p'
      ? tr('我在 ShareToken 共享了 AI 模型额度，点这个链接就能和我配对：{link}', vars) + '\n' + tr('已经装了客户端的话，也可以输入配对码 {code}（{time} 前有效）。', vars)
      : tr('来做 ShareToken 共享伙伴吧，互相免费使用对方的模型额度。点这个链接接受邀请：{link}', vars) + '\n' + tr('已经装了客户端的话，也可以输入邀请码 {code}（{time} 前有效）。', vars);
  }
  const linkButtons = (kind, c, id) => c.link_token ? h('span', { class: 'row-actions' },
    copyButton(() => inviteLink(kind, c.link_token), id + '-link', tr('复制链接')),
    copyButton(() => inviteText(kind, c), id + '-text', tr('复制邀请文案'))) : null;
  // shownInvite: the dialog after a code or invite is made.
  function shownInvite(kind, c, intro) {
    const prefix = kind === 'p' ? 'pair' : 'partner';
    modal('dlg-' + prefix + '-shown', kind === 'p' ? tr('配对码已生成') : tr('邀请码已生成'),
      h('p', { class: 'small muted' }, intro),
      h('div', { class: 'endpoint' }, h('code', { id: prefix + '-shown-code', class: 'pair-code big' }, c.code), copyButton(() => c.code, prefix + '-shown-copy')),
      h('p', { class: 'small muted' }, tr('邀请链接（对方没装客户端也能打开）')),
      h('div', { class: 'endpoint' }, h('code', { id: prefix + '-shown-link', class: 'invite-link' }, inviteLink(kind, c.link_token)), copyButton(() => inviteLink(kind, c.link_token), prefix + '-shown-link-copy')),
      c.approval ? h('p', { class: 'small notice', id: prefix + '-shown-approval' }, tr('已开启确认：对方使用后，要你在这里确认才生效。')) : null,
      h('p', { class: 'small muted', id: prefix + '-shown-meta' }, tr('到期时间：{time}', { time: time(c.expires_at) })),
      h('div', { class: 'modal-actions' },
        copyButton(() => inviteText(kind, c), prefix + '-shown-text', tr('复制邀请文案')),
        h('button', { type: 'button', class: 'primary', id: prefix + '-shown-done', onclick: () => closeModal() }, tr('完成'))));
  }
  // Requests waiting for this account's approval, and its own waiting for
  // someone else's. area: "pairing" or "sharing".
  function requestTables(area, requests, waiting, after, explain) {
    const act = (cls, label, method, path, danger) => r => h('button', { type: 'button', class: 'mini ' + cls + (danger ? ' danger' : ' accent'), 'data-id': String(r.id), onclick: async e => {
      e.currentTarget.disabled = true;
      try { await api(method, path(r)); } catch (ex) { await after({ cls: 'bad', text: explain(ex) }); return; }
      await after(null);
    } }, label);
    // One flat list: a panel's set() flattens one level only.
    return [
      requests && requests.length ? [h('h3', { class: 'sub-title', id: area + '-requests-title' }, tr('等待你确认')), table([
        { title: tr('账户'), get: r => r.username },
        { title: tr('备注'), get: r => r.label || '—' },
        { title: tr('申请时间'), get: r => time(r.created_at) },
        { title: '', get: r => h('span', { class: 'row-actions' },
          act(area + '-approve', tr('确认'), 'POST', r => '/api/v1/' + area + '/requests/' + r.id + '/approve')(r),
          act(area + '-reject', tr('拒绝'), 'DELETE', r => '/api/v1/' + area + '/requests/' + r.id, true)(r)) },
      ], requests)] : null,
      waiting && waiting.length ? [h('h3', { class: 'sub-title', id: area + '-waiting-title' }, tr('等待对方确认')), table([
        { title: tr('账户'), get: r => r.username },
        { title: tr('申请时间'), get: r => time(r.created_at) },
        { title: '', get: act(area + '-withdraw', tr('撤回'), 'DELETE', r => '/api/v1/' + area + '/waiting/' + r.id, true) },
      ], waiting)] : null,
    ].flat().filter(Boolean);
  }
  const approvalField = id => h('label', { class: 'check' }, h('input', { type: 'checkbox', id }), h('span', null, tr('需要我确认'), ' ', h('span', { class: 'small muted' }, tr('对方使用后，要你确认才生效'))));

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
      if (e.code === 'wrong_kind') return tr('这是共享伙伴的邀请链接，请到「共享伙伴」里输入');
      if (e.code === 'throttled') return tr('尝试次数太多，请几分钟后再试');
      if (e.code === 'codes_paused') return tr('配对码暂时停用：全站输错的次数太多。请改用邀请链接，或一小时后再试');
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
      const v = view || { sellers: [], buyers: [], codes: [], restricted: false, private: false, max_sellers: 0, requests: [], waiting: [] };
      const after = async f => { flash.sellers = flash.buyers = f; await reload(); };
      // "waiting for approval" goes once nothing waits any more
      if (view && flash.sellers && flash.sellers.pending && !(v.waiting || []).length) flash.sellers = null;
      sellers.set([v.sellers, v.restricted, v.max_sellers, v.waiting, error, flash.sellers, !view],
        failed(),
        flashLine(flash.sellers, 'pair-join-flash'),
        v.restricted ? h('div', { class: 'notice warn small', id: 'pair-restricted' }, tr('这个账户现在只向下面的卖家购买，「可用模型」也只列出他们的模型。全部解除后，恢复为向整个网络购买。')) : null,
        v.max_sellers ? h('p', { class: 'small muted', id: 'pair-max' }, tr('最多同时配对 {n} 个卖家。', { n: num(v.max_sellers) })) : null,
        v.sellers.length === 0 ? h('div', { class: 'empty', id: 'pair-sellers-empty' }, tr('还没有配对卖家，可以向网络上所有卖家购买')) : table([
          { title: tr('卖家'), get: u => who(u) },
          { title: tr('备注'), get: u => u.label || '—' },
          { title: tr('配对时间'), get: u => time(u.since) },
          { title: '', get: u => h('button', { type: 'button', class: 'mini danger pair-leave', 'data-id': String(u.id), onclick: () => leave(u) }, tr('解除')) },
        ], v.sellers),
        requestTables('pairing', null, v.waiting, after, pairError));

      buyers.set([v.buyers, v.codes, v.private, v.requests, portalBase, error, flash.buyers, !view],
        failed(),
        flashLine(flash.buyers, 'pair-code-flash'),
        requestTables('pairing', v.requests, null, after, pairError),
        privateToggle(v),
        v.codes.length === 0 ? h('div', { class: 'empty', id: 'pair-codes-empty' }, tr('没有有效的配对码')) : table([
          { title: tr('配对码'), get: c => h('span', { class: 'pair-code-cell' }, h('code', { class: 'pair-code' }, c.code), copyButton(() => c.code, 'pair-copy-' + c.id), c.approval ? h('span', { class: 'pill pair-approval' }, tr('需确认')) : null) },
          { title: tr('邀请链接'), get: c => linkButtons('p', c, 'pair-' + c.id) },
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
      const code = h('input', { type: 'text', id: 'pair-code-input', autocapitalize: 'off', autocomplete: 'off', spellcheck: 'false', maxlength: '256', placeholder: tr('配对码或邀请链接'), class: 'mono' });
      const err = errorBox('pair-join-error');
      const okBtn = h('button', { class: 'primary', type: 'submit', id: 'pair-join-ok' }, tr('配对'));
      modal('dlg-pair-join', tr('输入配对码'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(okBtn, err, async () => {
        if (!code.value.trim()) throw new Error(tr('请输入配对码'));
        const seller = await api('POST', '/api/v1/pairing/redeem', { code: code.value.trim() });
        closeModal();
        flash.sellers = { cls: 'ok', pending: !!seller.pending, text: seller.pending ? tr('已发送，等待 {name} 确认', { name: seller.username }) : tr('已和卖家 {name} 配对', { name: seller.username }) };
        await reload();
      }, pairError); } },
        field(tr('配对码或邀请链接'), code, tr('配对后，这个账户的所有设备都只向配对的卖家购买。随时可以解除。')),
        err.el, okBtn));
    }

    const TTLS = [[24, tr('1 天')], [168, tr('7 天')]];
    function codeDialog() {
      const label = h('input', { type: 'text', id: 'pair-label', maxlength: '64', dir: 'auto' });
      const ttl = h('select', { id: 'pair-ttl' }, TTLS.map(([hours, text]) => h('option', { value: String(hours), selected: hours === 24 }, text)));
      const limit = h('input', { type: 'text', id: 'pair-uses', inputmode: 'numeric', placeholder: tr('不限') });
      const approval = approvalField('pair-approval');
      const err = errorBox('pair-code-error');
      const okBtn = h('button', { class: 'primary', type: 'submit', id: 'pair-code-ok' }, tr('生成'));
      modal('dlg-pair-code', tr('生成配对码'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(okBtn, err, async () => {
        const n = limit.value.trim() === '' ? 0 : Number(limit.value.trim());
        if (!Number.isInteger(n) || n < 0 || n > 1000) throw new Error(tr('可配对的买家数需要是 1 到 1000 的整数，不填表示不限'));
        const c = await api('POST', '/api/v1/pairing/codes', { label: label.value.trim(), ttl_hours: Number(ttl.value), max_uses: n, approval: approval.querySelector('input').checked });
        flash.buyers = null;
        await reload();
        shownInvite('p', c, tr('把配对码或链接发给要配对的买家。拿到它的人都能和你配对，请只发给你信任的人。'));
      }, pairError); } },
        field(tr('备注（配对的买家也会看到，可不填）'), label),
        field(tr('有效期'), ttl, tr('到期后不能再用它配对，已经配对的买家不受影响。')),
        field(tr('可配对的买家数'), limit, tr('不填表示不限。')),
        approval,
        err.el, okBtn));
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
    // note: say something above the sellers, as after accepting an invite link.
    const note = async (text, pending) => { flash.sellers = { cls: 'ok', text, pending }; await reload(); };
    return { sellers: sellers.el, buyers: buyers.el, reload, note };
  }

  // ---- share partners ----

  // Share partners use each other's nodes for free, before the market. One
  // makes an invite, the other enters it. Each limits what it gives: tokens per
  // 24 hours and a part of each node's concurrent jobs. The portal keeps it all;
  // the daemon passes the requests on. Two panels: partners and invites, and
  // the settings.
  function partnersView() {
    let view = null, error = '', flash = null, settingsFlash = null, dirty = false;
    const partners = section('share-partners', tr('共享伙伴'), { fill: true,
      hint: tr('和伙伴配对后，双方优先使用对方的节点，不扣费。伙伴的节点不可用时，再到网络上付费购买。'),
      actions: [
        h('button', { type: 'button', id: 'partner-join', onclick: () => joinDialog() }, tr('输入邀请码')),
        h('button', { type: 'button', class: 'primary', id: 'partner-invite', onclick: () => inviteDialog() }, tr('邀请伙伴')),
      ] });
    const settings = section('share-settings', tr('共享设置'), {
      hint: tr('限制你的节点给伙伴用多少，对所有伙伴合计生效。每日上限按最近 24 小时计，不填表示不限；并发比例 100 表示不限，0 表示暂停共享。打开「只用伙伴的节点」后，不再到网络上付费购买，伙伴的节点不可用时请求会失败。') });

    function partnerError(e) {
      if (e.code === 'bad_code') return tr('邀请码不对，或者已经过期、被撤销或用过了');
      if (e.code === 'own_code') return tr('这是你自己的邀请码，请把它发给要共享的伙伴');
      if (e.code === 'limit') return tr('已达到数量上限，请先解除或撤销一些');
      if (e.code === 'not_found') return tr('这一项已经不存在了');
      if (e.code === 'not_logged_in') return tr('请先登录');
      if (e.code === 'wrong_kind') return tr('这是配对的邀请链接，请到「我的卖家」里输入');
      if (e.code === 'throttled') return tr('尝试次数太多，请几分钟后再试');
      if (e.code === 'codes_paused') return tr('配对码暂时停用：全站输错的次数太多。请改用邀请链接，或一小时后再试');
      return message(e);
    }

    async function reload() {
      try { view = await api('GET', '/api/v1/sharing'); error = ''; }
      catch (e) { error = partnerError(e); }
      render();
    }

    const failed = () => error ? h('div', { class: 'notice warn small partner-error' }, tr('暂时获取不到共享伙伴信息：{error}', { error })) : null;
    const flashLine = (f, id) => f ? h('div', { class: 'notice ' + f.cls, id }, f.text) : null;
    const who = u => [u.username, u.disabled ? h('span', { class: 'pill bad partner-off' }, tr('已停用')) : null];

    // The settings form keeps what is typed until saved; a reload does not
    // overwrite it.
    const onlyBox = h('input', { type: 'checkbox', id: 'partner-only', onchange: () => { dirty = true; } });
    const limitInput = h('input', { type: 'text', id: 'partner-limit', class: 'narrow', inputmode: 'numeric', placeholder: tr('不限'), 'aria-label': tr('每天最多给伙伴的 tokens'), oninput: () => { dirty = true; } });
    const pctInput = h('input', { type: 'text', id: 'partner-pct', class: 'narrow short', inputmode: 'numeric', 'aria-label': tr('伙伴最多占用每个节点并发的百分比'), oninput: () => { dirty = true; } });
    const servedText = h('span', { class: 'v muted', id: 'partner-served' });
    const saveBtn = h('button', { type: 'submit', class: 'primary', id: 'partner-save' }, tr('保存'));
    const settingsErr = errorBox('partner-settings-error');
    const settingsForm = h('form', { novalidate: true, id: 'partner-settings', onsubmit: e => { e.preventDefault(); run(saveBtn, settingsErr, async () => {
      const lim = limitInput.value.trim().replace(/[,\s]/g, '');
      const limit = lim === '' ? 0 : Number(lim);
      if (!Number.isInteger(limit) || limit < 0) throw new Error(tr('每日上限需要是不小于 0 的整数，不填表示不限'));
      const pct = Number(pctInput.value);
      if (!Number.isInteger(pct) || pct < 0 || pct > 100) throw new Error(tr('并发比例需要是 0 到 100 的整数'));
      await api('PUT', '/api/v1/sharing/settings', { only_partners: onlyBox.checked, daily_limit_tokens: limit, share_pct: pct });
      dirty = false;
      settingsFlash = { cls: 'ok', text: tr('已保存') };
      await reload();
    }, partnerError); } },
      h('div', { class: 'rows' },
        h('span', { class: 'k' }, tr('使用')), h('label', { class: 'v check' }, onlyBox, h('span', null, tr('只用伙伴的节点，不到网络上购买'))), h('span'),
        h('span', { class: 'k' }, tr('每日上限')), h('span', { class: 'v' }, limitInput, ' ', h('span', { class: 'muted small' }, tr('tokens / 24 小时，所有伙伴合计'))), h('span'),
        h('span', { class: 'k' }, tr('并发比例')), h('span', { class: 'v' }, pctInput, ' ', h('span', { class: 'muted small' }, tr('% 每个节点的并发，0 暂停共享'))), h('span'),
        h('span', { class: 'k' }, tr('已给出')), servedText, saveBtn),
      settingsErr.el);

    function fillSettings(st) {
      if (dirty) return;
      onlyBox.checked = !!st.only_partners;
      limitInput.value = st.daily_limit_tokens ? String(st.daily_limit_tokens) : '';
      pctInput.value = String(st.share_pct);
    }

    function render() {
      const v = view || { partners: [], invites: [], settings: { only_partners: false, daily_limit_tokens: 0, share_pct: 100 }, served: 0, window_hours: 24, max_partners: 0, requests: [], waiting: [] };
      const after = async f => { flash = f; await reload(); };
      if (view && flash && flash.pending && !(v.waiting || []).length) flash = null;
      partners.set([v.partners, v.invites, v.requests, v.waiting, portalBase, error, flash, !view],
        failed(),
        flashLine(flash, 'partner-flash'),
        requestTables('sharing', v.requests, v.waiting, after, partnerError),
        v.max_partners ? h('p', { class: 'small muted', id: 'partner-max' }, tr('最多同时和 {n} 个伙伴共享。下表是最近 {h} 小时的用量。', { n: num(v.max_partners), h: num(v.window_hours) })) : null,
        v.partners.length === 0 ? h('div', { class: 'empty', id: 'partners-empty' }, tr('还没有共享伙伴')) : table([
          { title: tr('伙伴'), get: u => who(u) },
          { title: tr('配对时间'), get: u => time(u.since) },
          { title: tr('我给出'), num: true, get: u => num(u.given || 0) },
          { title: tr('我使用'), num: true, get: u => num(u.taken || 0) },
          { title: '', get: u => h('button', { type: 'button', class: 'mini danger partner-leave', 'data-id': String(u.id), onclick: () => leave(u) }, tr('解除')) },
        ], v.partners),
        v.invites.length === 0 ? null : [
          h('h3', { class: 'sub-title' }, tr('未使用的邀请码')),
          table([
            { title: tr('邀请码'), get: c => h('span', { class: 'pair-code-cell' }, h('code', { class: 'pair-code' }, c.code), copyButton(() => c.code, 'partner-copy-' + c.id), c.approval ? h('span', { class: 'pill partner-approval' }, tr('需确认')) : null) },
            { title: tr('邀请链接'), get: c => linkButtons('s', c, 'partner-' + c.id) },
            { title: tr('到期时间'), get: c => time(c.expires_at) },
            { title: '', get: c => h('button', { type: 'button', class: 'mini danger partner-revoke', 'data-id': String(c.id), onclick: () => revoke(c) }, tr('撤销')) },
          ], v.invites)]);
      if (view) fillSettings(v.settings);
      saveBtn.disabled = !view;
      const st = v.settings;
      servedText.textContent = st.daily_limit_tokens > 0
        ? tr('最近 {h} 小时已给出 {used} / {limit} tokens', { h: num(v.window_hours), used: num(v.served), limit: num(st.daily_limit_tokens) })
        : tr('最近 {h} 小时已给出 {used} tokens', { h: num(v.window_hours), used: num(v.served) });
      settings.set([st, error, settingsFlash, !view],
        failed(),
        flashLine(settingsFlash, 'partner-settings-flash'),
        st.share_pct === 0 ? h('div', { class: 'notice warn small', id: 'partner-paused' }, tr('共享已暂停：伙伴现在不能使用你的节点。')) : null,
        settingsForm);
    }

    function joinDialog() {
      const code = h('input', { type: 'text', id: 'partner-code-input', autocapitalize: 'off', autocomplete: 'off', spellcheck: 'false', maxlength: '256', placeholder: tr('邀请码或邀请链接'), class: 'mono' });
      const err = errorBox('partner-join-error');
      const okBtn = h('button', { class: 'primary', type: 'submit', id: 'partner-join-ok' }, tr('配对'));
      modal('dlg-partner-join', tr('输入邀请码'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(okBtn, err, async () => {
        if (!code.value.trim()) throw new Error(tr('请输入邀请码'));
        const p = await api('POST', '/api/v1/sharing/join', { code: code.value.trim() });
        closeModal();
        flash = { cls: 'ok', pending: !!p.pending, text: p.pending ? tr('已发送，等待 {name} 确认', { name: p.username }) : tr('已和 {name} 成为共享伙伴', { name: p.username }) };
        await reload();
      }, partnerError); } },
        field(tr('邀请码或邀请链接'), code, tr('配对后，你们双方的所有设备都优先使用对方的节点，不扣费。随时可以解除。')),
        err.el, okBtn));
    }

    const TTLS = [[24, tr('1 天')], [72, tr('3 天')], [168, tr('7 天')]];
    function inviteDialog() {
      const ttl = h('select', { id: 'partner-ttl' }, TTLS.map(([hours, text]) => h('option', { value: String(hours), selected: hours === 24 }, text)));
      const approval = approvalField('partner-approval');
      const err = errorBox('partner-invite-error');
      const okBtn = h('button', { class: 'primary', type: 'submit', id: 'partner-invite-ok' }, tr('生成'));
      modal('dlg-partner-invite', tr('邀请伙伴'), h('form', { novalidate: true, onsubmit: e => { e.preventDefault(); run(okBtn, err, async () => {
        const c = await api('POST', '/api/v1/sharing/invites', { ttl_hours: Number(ttl.value), approval: approval.querySelector('input').checked });
        flash = null;
        await reload();
        shownInvite('s', c, tr('把邀请码或链接发给要共享的伙伴。对方接受后，你们就能免费使用对方的节点。'));
      }, partnerError); } },
        field(tr('有效期'), ttl, tr('邀请码只能用一次，到期后失效。')),
        approval,
        err.el, okBtn));
    }

    function leave(u) {
      confirmBox(tr('解除和 {name} 的共享？之后双方不能再免费使用对方的节点，除非重新配对。', { name: u.username }), tr('解除'),
        () => api('DELETE', '/api/v1/sharing/partners/' + u.id), async () => { flash = null; await reload(); }, partnerError);
    }
    function revoke(c) {
      confirmBox(tr('撤销邀请码 {code}？', { code: c.code }), tr('撤销'),
        () => api('DELETE', '/api/v1/sharing/invites/' + c.id), async () => { flash = null; await reload(); }, partnerError);
    }

    render();
    const note = async (text, pending) => { flash = { cls: 'ok', text, pending }; await reload(); };
    return { partners: partners.el, settings: settings.el, reload, note };
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

  // ---- warm-up (internal/warmup) ----

  // The warm-up: the Claude Code and Codex of this machine, and the Claude
  // and ChatGPT logins sold, each with times of day to start its 5-hour
  // window at, and whether to start windows as they reset.
  function warmupView() {
    const panel = section('warmup', tr('预热'), { fill: true, hint: tr('Claude 和 ChatGPT 订阅的 5 小时窗口从重置后的第一次请求开始计时。在设定的时间发一条极小的请求，窗口就提前开始，工作时更早重置。只在本机运行，不经过 sharetoken 网络。') });
    const err = errorBox('warmup-error');
    let st = null;
    const RESET = { '': tr('不预热'), week: tr('周窗口'), all: tr('5 小时和周窗口') };
    const RESULT = { warmed: tr('已预热'), running: tr('窗口已在运行'), used_up: tr('额度已用完，未发送'), failed: tr('失败') };
    const SKIP = { paused: tr('已暂停'), disabled: tr('厂商已停用'), not_installed: tr('未找到程序'), off: tr('未开启') };
    const label = t => t.kind === 'local' ? (t.agent === 'claude' ? tr('Claude Code（本机）') : tr('Codex（本机）')) : t.name;
    const clock = d => new Date(d).toLocaleString(I18N.lang, { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
    function last(t) {
      if (t.skip && t.skip !== 'off' && !t.last_try) return h('span', { class: 'muted', title: t.skip_error || '' }, SKIP[t.skip] || t.skip);
      if (!t.last_try) return '—';
      const text = (RESULT[t.result] || t.result) + ' · ' + clock(t.last_try);
      return h('span', { class: t.result === 'failed' ? 'warn-text' : null, title: t.error || t.last_model || '' }, text);
    }
    function render() {
      if (!st) return;
      const pause = h('input', { type: 'checkbox', id: 'warmup-paused', checked: st.paused || null, onchange: async () => {
        pause.disabled = true;
        try { st = await api('PUT', '/api/v1/warmup', { paused: pause.checked }); err.say(''); }
        catch (e) { err.say(message(e)); }
        render();
      } });
      panel.set(JSON.stringify(st), err.el,
        h('div', { class: 'warmup-top' }, h('label', { class: 'check' }, pause, h('span', null, tr('暂停所有自动预热'))),
          h('span', { class: 'small muted', id: 'warmup-zone' }, tr('时间按本机时区：{zone}', { zone: st.zone }))),
        table([
          { title: tr('账号'), get: t => h('span', { title: t.account || t.binary || '' }, label(t)) },
          { title: tr('每日时间'), get: t => t.enabled && t.times.length ? t.times.join(' ') : '—' },
          { title: tr('窗口重置时'), get: t => t.enabled ? RESET[t.on_reset || ''] : '—' },
          { title: tr('窗口'), get: t => usageText(t.usage) },
          { title: tr('最近一次'), get: last },
          { title: '', get: t => h('span', { class: 'actions' },
            h('button', { type: 'button', class: 'small', id: 'warmup-edit-' + t.id, onclick: () => dialog(t) }, tr('设置')),
            h('button', { type: 'button', class: 'small', id: 'warmup-run-' + t.id, disabled: t.skip === 'not_installed' || null, onclick: e => runNow(e.currentTarget, t) }, tr('立即预热'))) },
        ], st.targets));
    }
    async function runNow(btn, t) {
      btn.disabled = true;
      btn.textContent = tr('预热中…');
      try { await api('POST', '/api/v1/warmup/run', { target: t.id }); err.say(''); }
      catch (e) { err.say(tr('{name} 预热失败：{error}', { name: label(t), error: message(e) })); }
      await reload();
    }
    function dialog(t) {
      const dErr = errorBox('warmup-dlg-error');
      const on = h('input', { type: 'checkbox', id: 'warmup-on', checked: t.enabled || null });
      const times = h('input', { id: 'warmup-times', value: (t.times || []).join(', '), placeholder: '06:00, 11:05' });
      const reset = h('select', { id: 'warmup-on-reset' }, Object.entries(RESET).map(([v, n]) => h('option', { value: v, selected: (t.on_reset || '') === v || null }, n)));
      const model = h('input', { id: 'warmup-model', value: t.model || '', placeholder: t.agent === 'claude' ? 'haiku' : tr('默认') });
      const local = t.kind === 'local';
      const set = local ? (st.settings[t.agent] || {}) : {};
      const bin = local ? h('input', { id: 'warmup-binary', value: set.binary || '', placeholder: t.binary || tr('自动查找') }) : null;
      const save = h('button', { type: 'submit', class: 'primary', id: 'warmup-save' }, tr('保存'));
      modal('dlg-warmup', tr('预热：{name}', { name: label(t) }), h('form', { onsubmit: e => {
        e.preventDefault();
        run(save, dErr, async () => {
          const list = times.value.split(/[\s,，]+/).filter(Boolean);
          if (local) {
            const body = {};
            body[t.agent] = { enabled: on.checked, times: list, on_reset: reset.value, model: model.value.trim(), binary: bin.value.trim() };
            st = await api('PUT', '/api/v1/warmup', body);
          } else {
            const off = !on.checked;
            st = await api('PUT', '/api/v1/warmup/providers/' + encodeURIComponent(t.name), off ? {} : { times: list, on_reset: reset.value, model: model.value.trim() });
          }
          closeModal(); render();
        }, e => message(e));
      } },
        h('label', { class: 'check' }, on, h('span', null, tr('开启预热'))),
        field(tr('每日时间'), times, tr('本机时间，可填多个，用逗号分隔。到点时 5 小时窗口没在运行就发一条极小的请求；错过 1 小时以上不补发。')),
        field(tr('窗口重置时预热'), reset, tr('窗口一重置就启动下一个，不浪费空闲时间。窗口已用完或在用超额额度时不发送。')),
        field(tr('模型'), model, tr('留空用最便宜的模型。')),
        bin ? field(tr('程序路径'), bin, tr('留空自动查找。')) : null,
        dErr.el, save));
    }
    async function reload() {
      try { st = await api('GET', '/api/v1/warmup'); err.say(''); render(); }
      catch (e) { err.say(message(e)); if (!st) panel.set('err', err.el); }
    }
    return { el: panel.el, reload };
  }

  // ---- Claude Code and Codex (internal/connect, internal/official) ----

  // The coding tools of this machine: whether their config points at the
  // local endpoint, with or without their own vendor login kept; the plans'
  // usage seen on the way, what happens when one runs out, and the Codex
  // Desktop unlock.
  function toolsView() {
    const panel = section('tools', tr('Claude Code 与 Codex'), { fill: true, hint: tr('连接后，工具的配置指向本机接口（修改前会备份）。保留官方登录时，模型菜单里官方模型后面会列出 ShareToken 的模型（st- 开头），每个请求按所选模型分流：官方模型用自己的订阅，st- 模型走 ShareToken 网络。工具需要本机客户端一直运行。') });
    const err = errorBox('tools-error');
    let cv = null, ov = null, auto = null;
    const NAMES = { claude: 'Claude Code', codex: 'Codex' };
    function state(tool) {
      const a = cv && cv.state[tool];
      if (!a) return tr('未连接');
      return a.options.subscription ? tr('已连接 · 保留官方登录') : tr('已连接 · 只用 ShareToken');
    }
    function toolRow(tool) {
      const a = cv.state[tool];
      const signed = tool === 'claude' ? cv.claude_signed_in : cv.codex_chatgpt;
      const sub = h('input', { type: 'checkbox', id: 'tools-sub-' + tool, checked: (a ? a.options.subscription : signed) || null });
      const openai = tool === 'codex' ? h('input', { type: 'checkbox', id: 'tools-openai-provider', checked: (a && a.options.openai_provider) || null }) : null;
      const go = h('button', { type: 'button', class: 'primary small', id: 'tools-connect-' + tool, onclick: () => run(go, err, async () => {
        cv = await api('POST', '/api/v1/connect', { tool, subscription: sub.checked, openai_provider: openai ? openai.checked : false });
        render();
      }, e => message(e)) }, a ? tr('重新连接') : tr('连接'));
      const off = a ? h('button', { type: 'button', class: 'small', id: 'tools-off-' + tool, onclick: () => confirmBox(
        tr('把 {name} 的配置恢复成连接前的样子？', { name: NAMES[tool] }), tr('断开'),
        async () => { cv = await api('POST', '/api/v1/connect', { tool, off: true }); }, async () => render(), e => message(e)) }, tr('断开')) : null;
      return h('div', { class: 'tool-row', id: 'tools-row-' + tool },
        h('div', null, h('strong', null, NAMES[tool]), ' ', h('span', { class: 'small ' + (a ? '' : 'muted'), id: 'tools-state-' + tool }, state(tool))),
        h('div', { class: 'small muted' }, (tool === 'claude' ? cv.claude_path : cv.codex_path) + ' · ' + (signed ? tr('已登录官方账号') : tr('未登录官方账号'))),
        h('label', { class: 'check' }, sub, h('span', null, tool === 'claude' ? tr('保留 Claude 订阅登录（共存）') : tr('保留 ChatGPT 登录（共存）'))),
        openai ? h('label', { class: 'check' }, openai, h('span', null, tr('用 Codex 内置的 OpenAI provider（以前的对话仍可见）'))) : null,
        h('div', { class: 'actions' }, go, off));
    }
    function officialBox() {
      if (!ov) return null;
      const s = ov.settings;
      const save = async (patch, el) => {
        if (el) el.disabled = true;
        try { ov = await api('PUT', '/api/v1/official', Object.assign({}, s, patch)); err.say(''); }
        catch (e) { err.say(message(e)); }
        render();
      };
      const fb = h('input', { type: 'checkbox', id: 'tools-fallback', checked: !s.claude_fallback_off || null, onchange: () => save({ claude_fallback_off: !fb.checked }, fb) });
      const fbModel = h('input', { id: 'tools-fallback-model', value: s.claude_fallback_model || '', placeholder: tr('自动（与所选模型最接近）') });
      const fbSave = h('button', { type: 'button', class: 'small', id: 'tools-fallback-model-save', onclick: () => save({ claude_fallback_model: fbModel.value.trim().replace(/^st-/, '') }, fbSave) }, tr('保存'));
      const guard = h('input', { id: 'tools-guard', type: 'number', min: 1, max: 100, value: s.codex_guard_percent || 95, disabled: s.codex_unlock || null });
      const gSave = h('button', { type: 'button', class: 'small', id: 'tools-guard-save', disabled: s.codex_unlock || null, onclick: () => {
        const v = Number(guard.value);
        if (!(v > 0 && v <= 100)) { err.say(tr('请输入 1 到 100')); return; }
        save({ codex_guard_percent: v }, gSave);
      } }, tr('保存'));
      const unlock = h('input', { type: 'checkbox', id: 'tools-unlock', checked: s.codex_unlock || null, onchange: async () => {
        unlock.disabled = true;
        try { ov = await api('POST', '/api/v1/official/unlock', { on: unlock.checked }); cv = await api('GET', '/api/v1/connect'); err.say(''); }
        catch (e) { err.say(message(e)); }
        render();
      } });
      const c = ov.claude || {};
      const held = c.held_until ? tr('额度已用完，{time} 前的请求由 ShareToken 处理', { time: new Date(c.held_until).toLocaleString(I18N.lang, { hour12: false }) }) : '';
      const falls = c.fallbacks ? tr('已转到 ShareToken {n} 次，最近一次用 {model}', { n: num(c.fallbacks), model: c.last_model }) : '';
      const u = ov.unlock || {};
      return h('div', { class: 'tools-official', id: 'tools-official' },
        h('h3', null, 'Claude Code'),
        h('div', { class: 'small', id: 'tools-claude-usage' }, tr('订阅用量：{text}', { text: usageText(c.usage) })),
        held ? h('div', { class: 'small warn-text', id: 'tools-claude-held' }, held) : null,
        falls ? h('div', { class: 'small muted', id: 'tools-claude-fallbacks' }, falls) : null,
        h('label', { class: 'check' }, fb, h('span', null, tr('订阅额度用完后自动转到 ShareToken 继续'))),
        field(tr('接手的 ShareToken 模型'), h('div', { class: 'endpoint' }, fbModel, fbSave)),
        h('h3', null, 'Codex'),
        (ov.codex || []).length ? (ov.codex || []).map(x => h('div', { class: 'small' }, tr('ChatGPT 订阅用量：{text}', { text: usageText(x.usage) }))) : h('div', { class: 'small muted' }, tr('还没有看到 ChatGPT 订阅的用量')),
        field(tr('额度用到这个百分比时暂停官方模型'), h('div', { class: 'endpoint' }, guard, gSave), tr('Codex Desktop 在订阅额度用完后会锁住输入框，连 ShareToken 的模型也发不出去。提前暂停官方模型，并提示换成 st- 模型。开启无锁模式后不再暂停。')),
        h('label', { class: 'check' }, unlock, h('span', null, tr('Codex Desktop 无锁模式（macOS / Windows）'))),
        h('div', { class: 'small muted hint' }, tr('额度用完后 Codex Desktop 不锁输入框，ShareToken 模型照常可用。打开时系统会请你信任一张只对 localhost 有效的证书，并占用本机 {port} 端口；打开后 Codex Desktop 需要客户端一直运行。需要退出并重新打开 Codex Desktop。', { port: u.port || 443 })),
        s.codex_unlock ? h('div', { class: 'small ' + (u.listening && u.trusted ? 'muted' : 'warn-text'), id: 'tools-unlock-state' },
          u.error || (u.listening ? (u.trusted ? tr('无锁模式运行中') : tr('证书未被信任')) : tr('未在监听'))) : null);
    }
    function render() {
      if (!cv) return;
      const anyConnected = !!(cv.state.claude || cv.state.codex);
      const autoBox = auto ? h('div', { class: 'tools-autostart', id: 'tools-autostart-row' },
        h('label', { class: 'check' }, h('input', { type: 'checkbox', id: 'tools-autostart', checked: auto.on || null, onchange: async e => {
          e.target.disabled = true;
          try { auto = await api('PUT', '/api/v1/autostart', { on: e.target.checked }); err.say(''); }
          catch (x) { err.say(message(x)); }
          render();
        } }), h('span', null, auto.app ? tr('登录系统时自动启动 ShareToken（窗口不打开，在托盘后台运行）') : tr('登录系统时自动启动客户端（连接的工具需要它一直运行）'))),
        h('div', { class: 'small ' + (!auto.on && anyConnected ? 'warn-text' : 'muted'), id: 'tools-autostart-note' }, auto.on
          ? tr('已开启：登录系统后 ShareToken 在后台运行，占用少量内存和网络；开启了共享时，也会在后台接单卖出算力。从托盘退出 ShareToken 后，已连接的工具同样无法使用，直到再次打开。')
          : anyConnected
            ? tr('未开启：电脑重启、注销，或从托盘退出 ShareToken 后，已连接的 Claude Code 和 Codex 会连不上本机接口，连它们自己的官方模型也用不了（请求报连接失败），直到再次打开 ShareToken。不想自动启动的话，不用 ShareToken 时请先断开工具（会恢复原配置）。')
            : tr('未开启：连接 Claude Code 或 Codex 后，它们所有的请求（包括官方模型）都经过 ShareToken；ShareToken 没有运行时这些工具无法使用。'))) : null;
      panel.set(JSON.stringify([cv, ov, auto]), err.el, toolRow('claude'), toolRow('codex'), autoBox, officialBox());
    }
    async function reload() {
      try {
        [cv, ov, auto] = await Promise.all([api('GET', '/api/v1/connect'), api('GET', '/api/v1/official'), api('GET', '/api/v1/autostart').catch(() => null)]);
        err.say(''); render();
      } catch (e) { err.say(message(e)); if (!cv) panel.set('err', err.el); }
    }
    return { el: panel.el, reload };
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
    const endpoint = section('endpoint', tr('本地模型接口'), { row: true, hint: tr('OpenAI 工具、Claude Code 和 Codex 都可以把 base URL 改成这个地址使用（Claude Code 用不带 /v1 的地址）。') });
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
      apiKeyNote.textContent = apiKey.required ? tr('工具把它填作 API key') : tr('未开启：本机任何程序都能用这个接口花你的 credit');
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
      loadBuy();
    }

    // Buying settings (PUT /api/v1/buy): applied to the running endpoint at
    // once and saved in config.json.
    let buyDirty = false;
    const nativeBox = h('input', { type: 'checkbox', id: 'buy-native', disabled: true, onchange: () => saveBuy(nativeBox) });
    const budgetInput = h('input', { type: 'text', id: 'buy-budget', class: 'narrow short', inputmode: 'decimal', 'aria-label': tr('每个请求的预算'),
      oninput: () => { buyDirty = true; }, onkeydown: e => { if (e.key === 'Enter') saveBuy(budgetSave); } });
    const budgetSave = h('button', { type: 'button', id: 'buy-budget-save', onclick: () => saveBuy(budgetSave) }, tr('保存'));
    const buyNote = h('span', { class: 'small', id: 'buy-note', role: 'status' });
    function drawBuy(b) {
      nativeBox.checked = !!b.require_native;
      nativeBox.disabled = false;
      if (!buyDirty && document.activeElement !== budgetInput) budgetInput.value = String(b.budget_micros / 1e6);
    }
    async function loadBuy() {
      try { drawBuy(await api('GET', '/api/v1/buy')); } catch (e) { /* shown again at the next try */ }
    }
    async function saveBuy(el) {
      const credits = Number(budgetInput.value.trim());
      if (!Number.isFinite(credits) || credits < 0.01 || credits > 1000) {
        buyNote.className = 'small warn-text';
        buyNote.textContent = tr('预算需要在 0.01 到 1000 credit 之间');
        return;
      }
      el.disabled = true;
      try {
        drawBuy(await api('PUT', '/api/v1/buy', { budget_micros: Math.round(credits * 1e6), require_native: nativeBox.checked }));
        buyDirty = false;
        buyNote.className = 'small ok';
        buyNote.textContent = tr('已保存');
      } catch (e) {
        buyNote.className = 'small warn-text';
        buyNote.textContent = message(e);
      }
      el.disabled = false;
    }

    // Crash reports and the log (GET/PUT /api/v1/diagnostics; internal/client/diag.go).
    const diagBox = h('input', { type: 'checkbox', id: 'diag-upload', onchange: async e => {
      e.target.disabled = true;
      try { showDiag(await api('PUT', '/api/v1/diagnostics', { upload: e.target.checked })); }
      catch (x) { diagNote.className = 'hint small warn-text'; diagNote.textContent = message(x); }
      e.target.disabled = false;
    } });
    const diagNote = h('span', { class: 'hint small muted', id: 'diag-note' });
    const diagShow = h('button', { type: 'button', id: 'diag-show', onclick: showReports }, tr('查看待发送内容'));
    const logsOpen = h('button', { type: 'button', id: 'logs-open', onclick: async () => {
      try { await api('POST', '/api/v1/logs/open'); } catch (x) { diagNote.className = 'hint small warn-text'; diagNote.textContent = message(x); }
    } }, tr('打开日志目录'));
    function showDiag(d) {
      diagBox.checked = !!d.upload;
      diagNote.className = 'hint small muted';
      const parts = [tr('程序崩溃或出错时，把脱敏后的错误信息（错误类型、调用栈、崩溃前的程序日志）随你的账户发给 ShareToken，帮助我们修复问题。不含对话内容、密钥、邮箱和 IP 地址。')];
      if (!d.chosen && !d.default_upload) parts.push(tr('你所在的地区（欧洲经济区）默认关闭。'));
      if (d.pending) parts.push(tr('待发送 {n} 份。', { n: d.pending }));
      parts.push(tr('日志目录：{dir}', { dir: d.log_dir }));
      diagNote.textContent = parts.join(' ');
    }
    async function loadDiag() { try { showDiag(await api('GET', '/api/v1/diagnostics')); } catch (x) { /* an older daemon: the row stays empty */ } }
    async function showReports() {
      let reps = [];
      try { reps = (await api('GET', '/api/v1/diagnostics/reports')).reports || []; } catch (x) { diagNote.textContent = message(x); return; }
      modal('dlg-reports', tr('待发送的诊断报告'),
        h('p', { class: 'small muted' }, reps.length ? tr('下面就是会发送的全部内容，已经脱敏。关闭上报会清空它们。') : tr('现在没有待发送的报告。')),
        reps.map(r => h('details', { class: 'report' }, h('summary', null, new Date(r.occurred_at * 1000).toLocaleString() + ' · ' + r.kind + ' · ' + r.message.split('\n')[0]),
          h('pre', { class: 'report-json' }, JSON.stringify(r, null, 2)))));
    }

    const settings = h('section', { class: 'panel', id: 'settings' }, h('div', { class: 'rows' },
      h('span', { class: 'k' }, tr('账户')), accountName, logoutBtn,
      h('span', { class: 'k' }, tr('API key')), h('label', { class: 'v check' }, apiKeyBox, h('span', null, tr('调用本地模型接口需要 API key'))), apiKeyCopy,
      h('span', { class: 'k' }), h('span', { class: 'v' }, apiKeyText, ' ', apiKeyNote), rotateBtn,
      h('span', { class: 'k' }, tr('语言')), h('span', { class: 'v' }), I18N.picker() || h('span'),
      h('span', { class: 'k' }, tr('版本')), h('span', { class: 'v' }, versionText, ' ', checkResult), checkBtn,
      h('span', { class: 'k' }, tr('反馈')), h('span', { class: 'v muted' }, tr('告诉我们你遇到的问题或建议。')),
      h('button', { type: 'button', id: 'settings-feedback', onclick: () => feedbackDialog() }, tr('提交反馈')),
      h('span', { class: 'k' }, tr('诊断')), h('label', { class: 'v check' }, diagBox, h('span', null, tr('发送异常诊断报告'))), diagShow,
      h('span', { class: 'k' }), diagNote, logsOpen));
    loadDiag();
    // How requests are bought (PUT /api/v1/buy).
    const buyPanel = h('section', { class: 'panel', id: 'buy-settings' }, h('div', { class: 'rows' },
      h('span', { class: 'k' }, tr('原生卖家')), h('label', { class: 'v check' }, nativeBox, h('span', null, tr('只用原生支持请求接口的卖家'))), h('span'),
      h('span', { class: 'k' }), h('span', { class: 'hint small muted', id: 'buy-native-hint' }, tr('卖家的模型服务器原生支持请求所用的接口时（如 Claude Code 用 Anthropic Messages，Codex 用 OpenAI Responses）原样转发，不做转换。不勾选时优先选这类卖家，没有空闲的就由其他卖家转换后提供；勾选后没有这类卖家时请求直接失败。')),
      h('span', { class: 'k' }, tr('每个请求的预算')), h('span', { class: 'v' }, budgetInput, ' ', h('span', { class: 'muted small' }, 'credit'), ' ', buyNote), budgetSave,
      h('span', { class: 'k' }), h('span', { class: 'hint small muted', id: 'buy-budget-hint' }, tr('每个请求先冻结这么多，结束后按实际用量扣费，多余的退回。编程助手的 prompt 很长，预算太小会返回 budget 错误。'))));
    // Direct tunnels (PUT /api/v1/tailnet; internal/client/tailnet.go): a
    // second way to the other side of a job, next to the relays. The switch
    // and the transport apply as they change, the two text fields with their
    // button. The settings are those of config.json; what the tunnels carry
    // right now is in the table below them, in the status bar and on the
    // overview.
    let tn = null, tnDirty = false;
    const bytes = n => { n = Number(n || 0); return n >= 1048576 ? (n / 1048576).toFixed(1) + ' MB' : n >= 1024 ? Math.round(n / 1024) + ' KB' : n + ' B'; };
    const tunnelPath = p => p === 'direct' ? tr('直连') : p && p.startsWith('derp:') ? tr('中转 {region}', { region: p.slice(5) }) : tr('建立中');
    // What the tunnels are doing, in a few words, and their latency.
    function tunnelSummary(t) {
      if (!t || !t.enabled) return { text: tr('未开启'), ms: 0, on: false, n: 0 };
      const list = t.tunnels || [];
      if (list.length === 0) return { text: tr('已开启，暂无连接'), ms: 0, on: true, n: 0 };
      const direct = list.filter(x => x.path === 'direct').length, relayed = list.filter(x => x.path && x.path.startsWith('derp:')).length;
      const protos = [...new Set(list.map(x => x.proto).filter(Boolean))].map(x => x.toUpperCase()).sort().join('+');
      const timed = list.filter(x => x.ms > 0);
      const how = direct === list.length ? tr('直连') : direct === 0 && relayed > 0 ? tr('中转') : direct === 0 ? tr('建立中') : tr('直连 {direct} · 中转 {relayed}', { direct: num(direct), relayed: num(list.length - direct) });
      return { text: (protos ? protos + ' ' : '') + how + ' × ' + num(list.length), ms: timed.length ? timed.reduce((a, x) => a + x.ms, 0) / timed.length : 0, on: true, n: list.length };
    }
    const tnNote = h('span', { class: 'small', id: 'tailnet-note', role: 'status' });
    async function putTailnet(body, el, text) {
      el.disabled = true;
      try {
        if (text) tnDirty = false;
        drawTailnet(await api('PUT', '/api/v1/tailnet', body));
        tnNote.className = 'small ok';
        tnNote.textContent = tr('已保存');
      } catch (e) {
        if (text) tnDirty = true;
        tnNote.className = 'small warn-text';
        tnNote.textContent = message(e);
        if (tn) drawTailnet(tn);
      }
      el.disabled = false;
    }
    function saveTailnet() {
      const wait = Number(tnWait.value.trim() || 0);
      if (!Number.isInteger(wait) || wait < 0 || wait > 10000) {
        tnNote.className = 'small warn-text';
        tnNote.textContent = tr('等待时间需要是 0 到 10000 之间的整数（毫秒）');
        return;
      }
      putTailnet({ wait_ms: wait, derp_map: tnMap.value.trim() }, tnSave, true);
    }
    const tnBox = h('input', { type: 'checkbox', id: 'tailnet-enabled', disabled: true, onchange: () => putTailnet({ enabled: tnBox.checked }, tnBox) });
    const tnProto = h('select', { id: 'tailnet-transport', class: 'narrow', 'aria-label': tr('传输协议'), onchange: () => putTailnet({ transport: tnProto.value }, tnProto) },
      h('option', { value: 'quic' }, tr('QUIC（默认）')), h('option', { value: 'tcp' }, 'TCP'));
    const tnWait = h('input', { type: 'text', id: 'tailnet-wait', class: 'narrow short', inputmode: 'numeric', 'aria-label': tr('等待隧道'),
      oninput: () => { tnDirty = true; }, onkeydown: e => { if (e.key === 'Enter') saveTailnet(); } });
    const tnMap = h('input', { type: 'text', id: 'tailnet-derp', class: 'narrow wide', spellcheck: 'false', placeholder: tr('默认：公共中转服务器'), 'aria-label': tr('中转服务器'),
      oninput: () => { tnDirty = true; }, onkeydown: e => { if (e.key === 'Enter') saveTailnet(); } });
    const tnSave = h('button', { type: 'button', id: 'tailnet-save', onclick: saveTailnet }, tr('保存'));
    const tnServe = h('span', { class: 'v', id: 'tailnet-serving' });
    // Shown only while the tunnels are on.
    const tnOn = [
      h('span', { class: 'k' }, tr('传输协议')), h('span', { class: 'v' }, tnProto), h('span'),
      h('span', { class: 'k' }), h('span', { class: 'hint small muted', id: 'tailnet-transport-hint' }, tr('隧道里的连接跑在哪种协议上。只影响本机发起的连接（买模型时）；共享模型时两种都接受。QUIC 在稳定的网络上传大请求更快；网络丢包或抖动较多（例如信号不好的 Wi-Fi）、又常发几 MB 的请求时，TCP 更稳。改动对之后新建的连接生效。')),
      h('span', { class: 'k' }, tr('等待隧道')), h('span', { class: 'v' }, tnWait, ' ', h('span', { class: 'muted small' }, tr('毫秒'))), h('span'),
      h('span', { class: 'k' }), h('span', { class: 'hint small muted', id: 'tailnet-wait-hint' }, tr('第一次请求某个卖家时，最多等这么久让隧道建好再发送。0 表示不等：这个请求先走中继，隧道建好后接管后面的请求。范围 0 到 10000。')),
      h('span', { class: 'k' }, tr('中转服务器')), h('span', { class: 'v' }, tnMap), tnSave,
      h('span', { class: 'k' }), h('span', { class: 'hint small muted', id: 'tailnet-derp-hint' }, tr('隧道刚建立和打不通直连时经它中转，它只看到密文。留空使用公共中转服务器（DERP）；也可以填自建列表的网址或文件路径。买卖双方要用同一组服务器。修改后现有隧道会重建。')),
      h('span', { class: 'k' }, tr('本机')), tnServe, h('span'),
    ];
    const tailnetPanel = h('section', { class: 'panel', id: 'tailnet-settings' }, h('div', { class: 'rows' },
      h('span', { class: 'k' }, tr('直连隧道')), h('label', { class: 'v check' }, tnBox, h('span', null, tr('和对方直接建立加密隧道，不经过中继'))), tnNote,
      h('span', { class: 'k' }), h('span', { class: 'hint small muted', id: 'tailnet-hint' }, tr('基于 Tailscale 的协议（WireGuard）：买卖双方都开启后，请求和回答在两台机器之间直接传输，不再经过中继；有一方没开或隧道建不起来时，照常走中继，请求不受影响。不需要 Tailscale 账号，不改动系统网络，也不需要管理员权限。隧道先经中转服务器接通，几秒后换成两端直连。')),
      tnOn));
    const tunnels = section('tunnels', tr('当前隧道'), { fill: true, hint: tr('每条隧道通向一个对方：买模型时到卖家，共享模型时来自买家。连接 10 分钟没有新请求就关闭，下次请求时重新建立。') });
    function drawTailnet(t) {
      tn = t;
      const s = t.settings || {};
      tnBox.checked = !!t.enabled;
      tnBox.disabled = false;
      if (document.activeElement !== tnProto) tnProto.value = t.proto === 'tcp' ? 'tcp' : 'quic';
      if (!tnDirty && document.activeElement !== tnWait && document.activeElement !== tnMap) {
        tnWait.value = String(s.wait_ms || 0);
        tnMap.value = s.derp_map || '';
      }
      for (const el of tnOn) el.hidden = !t.enabled;
      tnServe.textContent = t.serving ? tr('买家可以经隧道到达本机（中转区域 {region}）', { region: t.region || '—' })
        : t.error ? tr('隧道暂不可用，买家走中继：{error}', { error: t.error })
        : current.state !== 'running' ? tr('连接网络后生效') : tr('没有在共享模型：只在买模型时使用隧道');
      tnServe.title = tnServe.textContent;
      const list = t.enabled ? (t.tunnels || []) : [];
      tunnels.el.hidden = !t.enabled;
      tunnels.set(list, list.length === 0 ? h('div', { class: 'empty', id: 'tunnels-empty' }, tr('还没有隧道。和同样开启了直连隧道的对方有请求往来时才会建立。')) : table([
        { title: tr('方向'), get: x => x.dir === 'in' ? tr('来自买家') : tr('到卖家') },
        { title: tr('对方'), get: x => x.peer ? h('span', { class: 'mono', title: x.peer }, shortPeer(x.peer)) : '—' },
        { title: tr('路径'), get: x => h('span', { class: 'badge' + (x.path === 'direct' ? ' accent' : ''), title: x.endpoint || null }, tunnelPath(x.path)) },
        { title: tr('协议'), get: x => x.proto ? x.proto.toUpperCase() : '—' },
        { title: tr('延迟'), num: true, get: x => rtt(x.ms) },
        { title: tr('连接'), num: true, get: x => num(x.conns) },
        { title: tr('流量'), num: true, get: x => '↓ ' + bytes(x.rx_bytes) + '  ↑ ' + bytes(x.tx_bytes) },
      ], list));
      drawTunnelStat();
    }
    async function loadTailnet() {
      try { drawTailnet(await api('GET', '/api/v1/tailnet')); } catch (e) { /* shown again at the next try */ }
    }
    // The status bar says what kind of path the tunnels give right now and
    // how fast it answers; a click opens their settings.
    const tunnelStat = h('button', { type: 'button', class: 'link', id: 'status-tunnel', onclick: () => showView('settings', 'network') });
    let lastTunnelStat = null;
    function drawTunnelStat() {
      const sum = tunnelSummary(tn);
      const key = sum.text + '|' + Math.round(sum.ms);
      if (key === lastTunnelStat) return;
      lastTunnelStat = key;
      tunnelStat.className = 'link' + (sum.on ? '' : ' off');
      tunnelStat.replaceChildren(...[h('span', null, tr('隧道')), h('span', { id: 'status-tunnel-text' }, sum.text), sum.ms ? rtt(sum.ms) : null].filter(Boolean));
      if (lastStatus) renderNetwork(lastStatus);
    }
    let lastStatus = null;

    let market = [];
    const pairing = pairingView();
    const partners = partnersView();
    const share = shareView(() => market);
    const warm = warmupView();
    const tools = toolsView();

    // name, label, panels, or tabs of panels.
    const VIEWS = [
      ['overview', tr('概览'), [tiles, endpoint.el, h('div', { class: 'cols' }, network.el, models.el)]],
      ['share', tr('共享'), null, [['state', tr('状态'), share.panes.state], ['providers', tr('厂商 API'), share.panes.providers], ['local', tr('本机模型'), share.panes.local]]],
      ['pairing', tr('配对'), null, [['partners', tr('共享伙伴'), [partners.partners, partners.settings]], ['sellers', tr('我的卖家'), [pairing.sellers]], ['buyers', tr('我的买家'), [pairing.buyers]]]],
      ['activity', tr('记录'), null, [['all', tr('全部'), [jobs.el]], ['sold', tr('卖出'), [share.sold]]]],
      ['settings', tr('设置'), null, [['general', tr('通用'), [settings]], ['buy', tr('调用'), [buyPanel]], ['network', tr('网络'), [tailnetPanel, tunnels.el]], ['devices', tr('我的设备'), [devices.el]], ['warmup', tr('预热'), [warm.el]], ['tools', tr('工具接入'), [tools.el]]]],
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
      h('footer', { class: 'statusbar' }, pill, relayStat, tunnelStat, h('span', { class: 'spacer' }), who, versionLabel)));
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
      lastStatus = st;
      const c = st.coordinator;
      const tun = tunnelSummary(tn);
      const q = c.link && c.link.samples ? c.link : null;
      // Under 10 ms a whole number would say 0.
      const ms1 = v => { v = Number(v || 0); return v > 0 && v < 10 ? Math.round(v * 10) / 10 : Math.round(v); };
      const link = q ? [ms1(q.avg_ms), ms1(q.jitter_ms), Math.round(q.loss_pct || 0)] : null;
      const cur = (st.relays || []).find(r => r.current);
      // The live pings of the relay in use say more than the last survey of all relays.
      setRelayStat(cur, link && link[0] ? link[0] : cur && !cur.error ? cur.rtt_ms : 0);
      network.set([st.relays, c.connected, c.via, st.warnings, link, tun.text, Math.round(tun.ms)],
        h('div', { class: 'kv' },
          h('span', { class: 'pill ' + (c.connected ? 'ok' : 'bad'), id: 'coord-line' }, c.connected ? tr('协调器已连接') : tr('协调器未连接')),
          tun.on ? h('span', { class: 'small', id: 'tunnel-line' }, tr('直连隧道：{state}', { state: tun.text }), tun.ms ? [' ', rtt(tun.ms)] : null) : null,
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
      lastStatus = null;
    }

    function renderModels(ms) {
      market = ms || [];
      models.set(ms, !ms || ms.length === 0 ? h('div', { class: 'empty', id: 'models-empty' }, tr('网络上暂时没有在线的模型')) : table([
        { title: tr('模型'), get: m => m.model },
        { title: tr('节点'), num: true, get: m => m.shared_nodes ? [num(m.nodes), ' ', h('span', { class: 'pill ok model-shared', title: tr('伙伴的节点：免费') }, tr('伙伴 {n}', { n: num(m.shared_nodes) }))] : num(m.nodes) },
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
        { title: 'Credit', num: true, get: j => j.shared ? h('span', { class: 'pill ok job-shared' }, tr('伙伴共享')) : j.role === 'sell' ? '+' + credit(j.earned_micros, 6) : '-' + credit(j.cost_micros, 6) },
      ], js));
    }

    // An invite link the app was opened with (sharetoken://, the shell): shown
    // once, and accepted only when the user says so; any web page can open
    // such a link.
    let invitesShown = '';
    function inviteError(e) {
      if (e.code === 'bad_code') return tr('邀请已失效：链接不对，或者已过期、被撤销或用完。请联系对方重新生成。');
      if (e.code === 'own_code') return tr('这是你自己的邀请链接，请把它发给要邀请的人。');
      if (e.code === 'limit') return tr('已达到数量上限，请先解除或撤销一些');
      if (e.code === 'throttled') return tr('尝试次数太多，请几分钟后再试');
      if (e.code === 'codes_paused') return tr('配对码暂时停用：全站输错的次数太多。请改用邀请链接，或一小时后再试');
      return message(e);
    }
    async function openInvite(inv) {
      showView('pairing', inv.kind === 's' ? 'partners' : 'sellers');
      const err = errorBox('invite-error');
      const body = h('div', { id: 'invite-body' }, h('p', { class: 'small muted' }, tr('正在读取邀请…')));
      const forget = () => api('DELETE', '/api/v1/invite/pending').catch(() => {});
      const okBtn = h('button', { type: 'button', class: 'primary', id: 'invite-accept', disabled: true, onclick: () => run(okBtn, err, async () => {
        const r = await api('POST', '/api/v1/invite/accept', { link: inv.link });
        closeModal();
        const text = r.pending ? tr('已发送，等待 {name} 确认', { name: r.username })
          : r.kind === 's' ? tr('已和 {name} 成为共享伙伴', { name: r.username }) : tr('已和卖家 {name} 配对', { name: r.username });
        await (r.kind === 's' ? partners : pairing).note(text, !!r.pending);
      }, inviteError) }, tr('接受邀请'));
      const dismiss = h('button', { type: 'button', id: 'invite-dismiss', onclick: () => { closeModal(); forget(); } }, tr('忽略'));
      modal('dlg-invite', inv.kind === 's' ? tr('共享伙伴邀请') : tr('配对邀请'), body, err.el, h('div', { class: 'modal-actions' }, dismiss, okBtn));
      if (inv.other_portal) {
        body.replaceChildren(h('div', { class: 'notice warn', id: 'invite-other-portal' }, tr('这个邀请来自 {host}，但这个客户端连接的是 {portal}，不能在这里接受。', { host: inv.host, portal: portalBase })));
        dismiss.textContent = tr('关闭');
        return;
      }
      try {
        const p = await api('GET', '/api/v1/invite/preview?link=' + encodeURIComponent(inv.link));
        const name = '\u2068' + p.inviter + '\u2069';
        body.replaceChildren(...[
          h('p', { id: 'invite-who' }, h('strong', null, inv.kind === 's' ? tr('{name} 邀请你成为共享伙伴', { name }) : tr('{name} 邀请你配对共享', { name }))),
          p.label ? h('p', { class: 'small', id: 'invite-label' }, tr('备注：{label}', { label: p.label })) : null,
          h('p', { class: 'small muted' }, inv.kind === 's'
            ? tr('成为共享伙伴后，你们双方都可以免费使用对方的节点，不扣 credit。伙伴的节点不可用时，再到网络上付费购买。随时可以解除。')
            : tr('配对后，你的账户只向配对的卖家购买，使用对方共享的 Claude、Codex 等模型额度。随时可以解除。')),
          p.approval ? h('p', { class: 'small notice', id: 'invite-approval' }, tr('对方开启了确认：你接受后，需要对方确认才生效。')) : null,
          h('p', { class: 'small muted' }, tr('有效期至 {time}', { time: time(p.expires_at) })),
          h('p', { class: 'small muted', id: 'invite-origin' }, tr('这个邀请是从浏览器里的链接打开的。只在你认识对方时接受。'))].filter(Boolean));
        okBtn.disabled = false;
      } catch (e) {
        body.replaceChildren(h('div', { class: 'notice bad', id: 'invite-bad' }, inviteError(e)));
        dismiss.textContent = tr('关闭');
      }
    }

    function renderSession(s) {
      current = s;
      portalBase = s.portal || portalBase;
      if (s.invite && s.invite.token && s.invite.token !== invitesShown) {
        invitesShown = s.invite.token;
        openInvite(s.invite);
      }
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
          warm.reload();
          tools.reload();
          try {
            const [me, j, sj] = await Promise.all([api('GET', '/api/v1/account'), api('GET', '/api/v1/jobs?limit=10'), api('GET', '/api/v1/jobs?role=sell&limit=10')]);
            if (stopped) return;
            renderAccount(me);
            renderJobs(j.jobs);
            share.account(me, sj.jobs);
            setBanner('');
            await pairing.reload();
            await partners.reload();
          } catch (e) {
            if (e.status === 401) { boot(); return; }
            setBanner(tr('暂时连不上门户，下面是上次获取的账户信息（{error}）', { error: message(e) }));
          }
        }
        await loadTailnet();
        if (stopped) return;
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
    // The shell brings the window forward when it is opened with a link: look at once.
    window.addEventListener('focus', () => { if (!stopped) refresh(); });
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
