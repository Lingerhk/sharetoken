// The page of an invite link: https://<portal>/p#<token> (a pairing code) or
// /s#<token> (a share invite). It shows who invites, signs the visitor in or
// up, accepts the invite on that account, and offers the desktop app: to
// download, or, when it is installed, to open the link in it
// (sharetoken://p/<token>). The token is after the "#", so the server never
// sees the address; the page sends it only to /api/v1/invite.
'use strict';
(async () => {
  await I18N.ready;
  const tr = I18N.t;
  const { api, h, clear, add, toast, state } = ST;
  const app = ST.$('app');

  const kind = location.pathname.replace(/\/+$/, '') === '/s' ? 's' : 'p';
  const token = decodeURIComponent(location.hash.replace(/^#\/?/, '')).trim();
  const link = location.origin + '/' + kind + '#' + token;
  const appLink = 'sharetoken://' + kind + '/' + encodeURIComponent(token) + '?h=' + encodeURIComponent(location.host);
  const ua = navigator.userAgent;
  const inChatApp = /MicroMessenger|\bQQ\/|WeiBo|DingTalk|Lark|Feishu/i.test(ua);
  const mobile = /Android|iPhone|iPad|iPod|Mobile|HarmonyOS/i.test(ua) && !/Windows NT|Macintosh/.test(ua);
  const os = /Windows/.test(ua) ? 'windows' : /Mac OS X|Macintosh/.test(ua) && !mobile ? 'macos' : /Linux|X11/.test(ua) && !/Android/.test(ua) ? 'linux' : '';
  const OS_NAMES = { macos: 'macOS', windows: 'Windows', linux: 'Linux' };
  const ARCHES = { macos: { arm64: 'Apple Silicon', amd64: 'Intel' }, windows: { amd64: 'x64', arm64: 'ARM64' }, linux: { amd64: 'x86-64', arm64: 'ARM64' } };

  const pair = kind === 'p';
  const who = name => h('b', { class: 'invite-name' }, name);

  function frame(...children) {
    clear(app).appendChild(ST.authFrame(h('div', { class: 'card login invite', id: 'invite' }, children)));
  }

  function errorText(e) {
    return ({
      bad_code: tr('邀请已失效：链接不对，或者已过期、被撤销或用完。请联系对方重新生成。'),
      own_code: tr('这是你自己的邀请链接，请把它发给要邀请的人。'),
      limit: tr('已达到数量上限：{msg}', { msg: e.message }),
      throttled: tr('尝试次数过多，请几分钟后再试'),
      links_paused: tr('邀请链接暂时无法预览：全站输错的次数太多。请一小时后再试'),
      codes_paused: tr('配对码暂时停用：全站输错的次数太多。请改用邀请链接，或一小时后再试'),
      rate_limited: tr('请求太频繁，请稍后再试'),
      wrong_kind: tr('这个链接的类型不对'),
    })[e.code] || e.message;
  }

  // ---- downloads ----
  let downloads = null;
  async function loadDownloads() {
    if (downloads) return downloads;
    try { downloads = (await api('GET', '/api/v1/downloads')).packages.filter(p => p.kind === 'desktop'); }
    catch (e) { downloads = []; }
    return downloads;
  }
  const size = n => tr('{n} MB', { n: (n / 1048576).toLocaleString(I18N.lang, { maximumFractionDigits: 1 }) });
  function packageLinks(list, osName) {
    return Object.keys(ARCHES[osName]).map(arch => list.find(p => p.os === osName && p.arch === arch)).filter(Boolean)
      .map(p => h('a', { class: 'button-link' + (osName === os ? ' primary' : ''), href: p.url, download: p.file, title: p.file, 'data-os': osName },
        ST.icon('download', 16), h('span', null, OS_NAMES[osName] + ' · ' + ARCHES[osName][p.arch]), h('small', null, size(p.size))));
  }
  async function downloadBox() {
    const box = h('div', { class: 'invite-download', id: 'invite-download' });
    if (mobile) {
      add(box, h('p', { class: 'muted' }, tr('ShareToken 目前只有电脑版。请在电脑上打开这个链接，下载并安装客户端。')),
        h('div', { class: 'row' }, h('button', { type: 'button', id: 'invite-copy-link', onclick: () => ST.copy(link) }, tr('复制链接'))));
      return box;
    }
    const list = await loadDownloads();
    if (!list.length) {
      add(box, h('p', { class: 'muted' }, tr('暂时没有可下载的安装包，请稍后再来，或查看文档。')), h('a', { href: '/docs/desktop' }, tr('桌面客户端文档')));
      return box;
    }
    const mine = os ? packageLinks(list, os) : [];
    const others = Object.keys(OS_NAMES).filter(o => o !== os).flatMap(o => packageLinks(list, o));
    add(box, mine.length ? h('div', { class: 'dl-row', id: 'invite-dl-here' }, mine) : null,
      others.length ? h('details', { class: 'dl-more' }, h('summary', null, mine.length ? tr('其他系统') : tr('选择你的系统')), h('div', { class: 'dl-row' }, others)) : null,
      h('p', { class: 'muted small' }, h('a', { href: '/docs/desktop' + (os ? '?os=' + os : '') }, tr('安装指南'))));
    return box;
  }

  // ---- opening the installed app ----
  function openInApp() {
    let left = false;
    const away = () => { left = true; };
    window.addEventListener('blur', away, { once: true });
    document.addEventListener('visibilitychange', away, { once: true });
    location.href = appLink;
    setTimeout(() => {
      const hint = ST.$('invite-open-hint');
      if (hint && !left) hint.hidden = false;
    }, 2500);
  }

  // ---- the steps after accepting ----
  async function done(r) {
    const name = r.username;
    const title = r.pending ? tr('已发送，等待对方确认')
      : pair ? tr('已与卖家配对') : tr('已成为共享伙伴');
    const lead = r.pending
      ? h('p', null, tr('{name} 确认后，配对就会生效。你可以先安装客户端。', { name: '\u2068' + name + '\u2069' }))
      : pair ? h('p', null, tr('你的账户现在向 {name} 购买。安装客户端并用同一个邮箱登录，就能直接使用。', { name: '\u2068' + name + '\u2069' }))
        : h('p', null, tr('你和 {name} 现在可以免费使用对方的节点。安装客户端并用同一个邮箱登录，就能直接使用。', { name: '\u2068' + name + '\u2069' }));
    frame(h('div', { class: 'invite-done ' + (r.pending ? 'warn' : 'ok'), id: 'invite-result', 'data-pending': r.pending ? '1' : '0' }, ST.icon(r.pending ? 'clock' : 'checkCircle', 28), h('h1', null, title)),
      lead,
      h('ol', { class: 'invite-steps' },
        h('li', null, tr('下载并安装 ShareToken 客户端')),
        h('li', null, tr('用 {email} 登录客户端', { email: state.me ? state.me.user.username : '' })),
        h('li', null, pair ? tr('在 Claude Code、Codex 等工具里使用共享的模型') : tr('客户端会优先使用伙伴的节点，不扣费'))),
      await downloadBox(),
      mobile ? null : h('p', { class: 'muted small' }, tr('已经安装了客户端？'), ' ', h('a', { href: appLink, id: 'invite-open-done' }, tr('在 ShareToken 中打开'))),
      h('p', { class: 'muted small' }, h('a', { href: '/app/#/pairing' }, tr('在网页控制台查看配对'))));
  }

  async function accept(btn, err) {
    btn.disabled = true; err.textContent = '';
    try {
      const r = await api('POST', '/api/v1/invite/accept', { link });
      await done(r);
    } catch (e) {
      btn.disabled = false;
      if (e.status === 401) { await signIn(); return; }
      err.textContent = errorText(e);
    }
  }

  async function signIn() {
    await ST.renderLogin(app, { title: tr('登录或注册后接受邀请') });
    await ST.loadMe();
    await show(true);
  }

  // ---- the invite ----
  async function show(acceptNow) {
    if (!/^[0-9A-Za-z]{8}([0-9A-Za-z]{4})?$/.test(token)) {
      frame(h('h1', null, tr('邀请链接不完整')), h('p', { class: 'muted', id: 'invite-error' }, tr('链接缺了一部分。请复制完整的链接重新打开，或者让对方重新发一次。')),
        h('p', null, h('a', { href: '/' }, tr('← 返回官网'))));
      return;
    }
    let p;
    try { p = await api('GET', '/api/v1/invite/' + kind + '/' + token); }
    catch (e) {
      frame(h('h1', null, tr('邀请不可用')), h('p', { class: 'muted', id: 'invite-error' }, errorText(e)),
        h('p', null, h('a', { href: '/' }, tr('了解 ShareToken')), ' · ', h('a', { href: '/#download' }, tr('下载客户端'))));
      return;
    }
    const err = h('div', { class: 'error', id: 'invite-accept-error', role: 'alert' });
    const btn = h('button', { class: 'primary', type: 'button', id: 'invite-accept', onclick: () => state.me ? accept(btn, err) : signIn() },
      state.me ? tr('接受邀请') : tr('登录并接受邀请'));
    const name = '\u2068' + p.inviter + '\u2069';
    frame(
      h('div', { class: 'invite-head' }, ST.chip(pair ? 'swap' : 'users', 'accent', 26),
        h('h1', { id: 'invite-title' }, pair ? tr('{name} 邀请你配对共享', { name }) : tr('{name} 邀请你成为共享伙伴', { name }))),
      p.label ? h('p', { class: 'invite-label', id: 'invite-label' }, tr('备注：{label}', { label: p.label })) : null,
      h('p', { class: 'muted' }, pair
        ? tr('配对后，你的账户只向配对的卖家购买，使用对方共享的 Claude、Codex 等模型额度。随时可以解除。')
        : tr('成为共享伙伴后，你们双方都可以免费使用对方的节点，不扣 credit。伙伴的节点不可用时，再到网络上付费购买。随时可以解除。')),
      p.approval ? h('p', { class: 'notice small', id: 'invite-approval' }, tr('对方开启了确认：你接受后，需要对方确认才生效。')) : null,
      h('p', { class: 'muted small', id: 'invite-expiry' }, tr('有效期至 {time}', { time: ST.time(p.expires_at) })),
      inChatApp ? h('div', { class: 'notice warn', id: 'invite-inapp' }, tr('当前在聊天软件里打开。下载和打开客户端可能被拦截，请点右上角「…」，选择「在浏览器中打开」。')) : null,
      h('div', { class: 'invite-actions' }, btn,
        mobile ? null : h('button', { type: 'button', id: 'invite-open', onclick: openInApp }, ST.icon('desktop', 16), h('span', null, tr('在 ShareToken 中打开')))),
      err,
      h('p', { class: 'muted small', id: 'invite-open-hint', hidden: true }, tr('没有反应？可能还没有安装客户端。可以先在网页上接受邀请，再下载客户端。')),
      state.me ? h('p', { class: 'muted small', id: 'invite-who' }, tr('当前账户：{name}', { name: state.me.user.username }), ' · ',
        h('a', { href: '#', onclick: e => { e.preventDefault(); ST.api('POST', '/api/v1/web/logout').catch(() => {}).then(() => location.reload()); } }, tr('换个账户'))) : null,
      h('details', { class: 'invite-later' }, h('summary', null, tr('还没有客户端？')),
        h('p', { class: 'muted small' }, tr('可以先在这里接受邀请（配对记在账户上），再下载客户端，用同一个邮箱登录。也可以装好客户端后，把这个链接粘贴到客户端的「输入配对码」里。')),
        await downloadBox(),
        h('div', { class: 'row' }, h('button', { type: 'button', class: 'small', id: 'invite-copy', onclick: () => ST.copy(link) }, tr('复制链接')))));
    if (acceptNow && state.me) await accept(btn, err);
  }

  await ST.loadInfo().catch(() => {});
  await ST.loadMe().catch(() => null);
  window.addEventListener('hashchange', () => location.reload());
  try { await show(false); }
  catch (e) { toast(e.message, 'bad'); }
})();
