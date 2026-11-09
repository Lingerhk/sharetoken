// The user portal: balance, bills, jobs, API tokens, devices, network, account.
'use strict';
(async () => {
  await I18N.ready;
  const tr = I18N.t, parts = I18N.parts;
  const { api, h, clear, add, credit, signed, num, time, ago, peer, status, badge, toast, fail, modal, confirm, secret, table, kv, field, input, state, pageTitle, section, tile } = ST;
  const app = ST.$('app');

  await ST.loadInfo().catch(fail);
  while (!(await ST.loadMe())) {
    await ST.renderLogin(app, { title: 'sharetoken', allowRegister: state.info && state.info.registration_open });
  }
  const me = () => state.me;

  // ---- frame ----
  const balance = h('span');
  const showBalance = d => { balance.textContent = credit(d.balance.available_micros) + ' credit'; };
  const balancePill = h('a', { class: 'balance-pill', id: 'top-balance', href: '#/ledger', title: tr('可用余额') }, ST.icon('creditCard', 16), balance);
  showBalance(me());
  const { nav, main } = ST.shell(app, {
    title: tr('用户门户'), role: me().user.is_admin ? tr('管理员') : tr('普通用户'), extra: balancePill,
    items: [
      { route: 'home', icon: 'grid', label: tr('总览') }, { route: 'ledger', icon: 'creditCard', label: tr('账单') }, { route: 'jobs', icon: 'bolt', label: tr('作业') },
      { route: 'tokens', icon: 'key', label: 'API Token' }, { route: 'devices', icon: 'desktop', label: tr('设备') }, { route: 'network', icon: 'globe', label: tr('网络') },
      { route: 'account', icon: 'user', label: tr('账户') }, { route: 'feedback', icon: 'chat', label: tr('反馈') },
    ],
    links: [{ href: '/docs/', icon: 'book', label: tr('文档') }, me().user.is_admin ? { href: '/admin/', icon: 'shield', label: tr('管理控制台') } : null].filter(Boolean),
    menu: [{ href: '#/account', icon: 'user', label: tr('账户') }],
  });
  // The balance moves while a page stays open; state.me is left alone so a lapsed session cannot pull it from under a page.
  window.addEventListener('hashchange', () => { api('GET', '/api/v1/me').then(showBalance).catch(() => {}); });

  const moreButton = (fn) => h('div', { class: 'row end' }, h('button', { onclick: fn }, tr('加载更多')));

  // ---- pages ----
  async function home(m) {
    const d = await ST.loadMe();
    const b = d.balance, u = d.usage;
    add(m, pageTitle(tr('总览')),
      h('div', { class: 'tiles' },
        tile(tr('可用余额'), credit(b.available_micros) + ' credit', tr('账户 {account}', { account: b.account }), 'creditCard', 'ok'),
        tile(tr('冻结中'), credit(b.held_micros) + ' credit', tr('进行中作业的预留额'), 'lock', 'warn'),
        tile(tr('买入'), tr('{n} 单', { n: num(u.bought.jobs) }), tr('花费 {credit}，{tokens} tokens', { credit: credit(u.bought.micros), tokens: num(u.bought.prompt_tokens + u.bought.completion_tokens) }) + (u.bought.pending_jobs ? ' · ' + tr('进行中：{n}', { n: num(u.bought.pending_jobs) }) : ''), 'trendingUp', 'blue'),
        tile(tr('卖出'), tr('{n} 单', { n: num(u.sold.jobs) }), tr('净收入 {credit}，{tokens} tokens', { credit: credit(u.sold.micros), tokens: num(u.sold.prompt_tokens + u.sold.completion_tokens) }) + (u.sold.pending_jobs ? ' · ' + tr('进行中：{n}', { n: num(u.sold.pending_jobs) }) : ''), 'dollar', 'violet'),
        tile(tr('设备'), num(d.devices.length), I18N.list(d.devices.map(x => x.name)) || tr('尚未绑定设备'), 'desktop')));
    const [jobs, led] = await Promise.all([api('GET', '/api/v1/jobs?limit=8'), api('GET', '/api/v1/ledger?limit=8')]);
    add(m, section('bolt', tr('最近作业'), h('small', null, h('a', { href: '#/jobs' }, tr('全部')))), h('div', { class: 'card' }, jobsTable(jobs.jobs)),
      section('creditCard', tr('最近账单'), h('small', null, h('a', { href: '#/ledger' }, tr('全部')))), h('div', { class: 'card' }, ledgerTable(led.entries)));
    if (d.devices.length === 0) add(m, h('div', { class: 'card muted' }, parts('还没有绑定设备。在你的机器上运行 {command} 即可把该设备绑定到此账户。', { command: h('code', null, 'sharetoken login -user ' + me().user.username) })));
  }

  function ledgerTable(entries) {
    return table([
      { title: tr('时间'), cell: e => time(e.at) },
      { title: tr('摘要'), cell: e => e.memo },
      { title: tr('作业'), cell: e => e.job_id ? peer(e.job_id) : '' },
      { title: tr('金额'), num: true, cell: e => h('span', { class: e.amount_micros > 0 ? 'ok' : 'bad' }, signed(e.amount_micros)) },
    ], entries, tr('还没有账单记录'));
  }
  async function ledger(m) {
    add(m, pageTitle(tr('账单'), h('small', null, tr('正数入账、负数出账，单位 credit'))));
    const card = h('div', { class: 'card' }); add(m, card);
    let all = [];
    async function load() {
      const before = all.length ? all[all.length - 1].seq : 0;
      const r = await api('GET', '/api/v1/ledger?limit=50' + (before ? '&before=' + before : ''));
      all = all.concat(r.entries);
      add(clear(card), ledgerTable(all), r.entries.length === 50 ? moreButton(load) : null);
    }
    await load();
  }

  function jobsTable(jobs) {
    return table([
      { title: tr('时间'), cell: j => time(j.created_at) },
      { title: tr('角色'), cell: j => badge(j.role === 'buy' ? tr('买入') : tr('卖出'), j.role === 'buy' ? 'accent' : 'ok') },
      { title: tr('模型'), cell: j => j.model },
      { title: tr('状态'), cell: j => [status(j.status), j.dispute_outcome ? h('span', { class: 'muted' }, ' ' + tr('裁决：{outcome}', { outcome: j.dispute_outcome })) : null] },
      { title: tr('对方'), cell: j => peer(j.role === 'buy' ? j.node : j.buyer) },
      { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
      { title: tr('费用'), num: true, cell: j => credit(j.cost_micros) },
      { title: tr('收入'), num: true, cell: j => j.role === 'sell' && j.earned_micros ? h('span', { class: 'ok' }, '+' + credit(j.earned_micros)) : '' },
    ], jobs, tr('还没有作业'));
  }
  async function jobs(m) {
    const role = h('select', { onchange: () => { all = []; load(); } }, h('option', { value: '' }, tr('全部')), h('option', { value: 'buy' }, tr('买入')), h('option', { value: 'sell' }, tr('卖出')));
    add(m, pageTitle(tr('作业')), h('div', { class: 'toolbar' }, h('span', { class: 'muted' }, tr('角色')), role));
    const card = h('div', { class: 'card' }); add(m, card);
    let all = [];
    async function load() {
      const before = all.length ? all[all.length - 1].created_at : 0;
      const r = await api('GET', '/api/v1/jobs?limit=50&role=' + role.value + (before ? '&before=' + before : ''));
      all = all.concat(r.jobs);
      add(clear(card), jobsTable(all), r.jobs.length === 50 ? moreButton(load) : null);
    }
    await load();
  }

  async function tokens(m) {
    add(m, pageTitle('API Token', h('small', null, tr('用于 sharetoken 客户端和脚本；浏览器会话也在这里'))));
    const list = h('div', { class: 'card' });
    const name = input({ type: 'text', placeholder: tr('例如 laptop、ci'), maxlength: 64, required: true });
    const ttl = h('select', null, h('option', { value: '0' }, tr('永不过期')), h('option', { value: '24' }, tr('1 天')), h('option', { value: '168' }, tr('7 天')), h('option', { value: '720' }, tr('30 天')), h('option', { value: '2160' }, tr('90 天')), h('option', { value: '8760' }, tr('1 年')));
    const form = h('form', {
      class: 'form', onsubmit: async e => {
        e.preventDefault();
        try {
          const r = await api('POST', '/api/v1/tokens', { name: name.value.trim(), ttl_hours: Number(ttl.value) });
          name.value = '';
          await modal({ title: tr('新 token 已创建'), cancel: false, okText: tr('我已保存'), body: h('div', null, h('p', null, tr('它只显示这一次，请立即复制保存：')), secret(r.token),
            h('p', { class: 'muted' }, tr('用法：sharetoken 客户端 config.json 的 token 字段，或 HTTP 头 Authorization: Bearer …'))) });
          await load();
        } catch (ex) { fail(ex); }
      }
    }, h('div', { class: 'row' }, field(tr('名称'), name), field(tr('有效期'), ttl)), h('div', null, h('button', { class: 'primary', type: 'submit' }, tr('创建 token'))));
    add(m, section('plus', tr('新建')), h('div', { class: 'card' }, form), section('key', tr('现有 token')), list);
    async function load() {
      const r = await api('GET', '/api/v1/tokens');
      add(clear(list), table([
        { title: tr('名称'), cell: t => [t.name, t.id === r.current ? [' ', badge(tr('当前会话'), 'accent')] : null] },
        { title: tr('前缀'), cell: t => h('code', null, t.prefix + '…') },
        { title: tr('创建'), cell: t => time(t.created_at) },
        { title: tr('最近使用'), cell: t => ago(t.last_used_at) },
        { title: tr('过期'), cell: t => t.expires_at ? time(t.expires_at) : tr('永不') },
        { title: '', cell: t => h('button', { class: 'small danger', onclick: async () => {
          if (!await confirm(tr('吊销 token'), t.id === r.current ? tr('吊销「{name}」后，使用它的客户端会立即掉线，包括当前浏览器会话。', { name: t.name }) : tr('吊销「{name}」后，使用它的客户端会立即掉线。', { name: t.name }), tr('吊销'))) return;
          try { await api('DELETE', '/api/v1/tokens/' + t.id); toast(tr('已吊销')); if (t.id === r.current) location.reload(); else await load(); } catch (ex) { fail(ex); }
        } }, tr('吊销')) },
      ], r.tokens));
    }
    await load();
  }

  async function devices(m) {
    add(m, pageTitle(tr('设备'), h('small', null, tr('绑定到此账户的 peer；它们花你的 credit、把收入记到你名下'))));
    const list = h('div', { class: 'card' }); add(m, list,
      h('div', { class: 'card muted' }, parts('绑定新设备需要设备私钥签名，所以要在那台机器上运行 {command}。解绑后协调器会立即拒绝该设备。', { command: h('code', null, 'sharetoken login -user ' + me().user.username) })));
    async function load() {
      const r = await api('GET', '/api/v1/devices');
      add(clear(list), table([
        { title: tr('名称'), cell: d => d.name },
        { title: 'Peer ID', cell: d => h('code', { class: 'mono' }, d.peer) },
        { title: tr('绑定时间'), cell: d => time(d.created_at) },
        { title: tr('最近活动'), cell: d => ago(d.last_seen_at) },
        { title: '', cell: d => h('span', { class: 'row' },
          h('button', { class: 'small', onclick: async () => {
            const nm = input({ type: 'text', value: d.name, maxlength: 64 });
            if (!await modal({ title: tr('重命名设备'), body: field(tr('名称'), nm) })) return;
            try { await api('PATCH', '/api/v1/devices/' + encodeURIComponent(d.peer), { name: nm.value.trim() }); toast(tr('已重命名')); await load(); } catch (ex) { fail(ex); }
          } }, tr('重命名')),
          h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm(tr('解绑设备'), tr('解绑「{name}」后它不能再用你的账户买卖；重新绑定需在该设备上再次登录。', { name: d.name }), tr('解绑'))) return;
            try { await api('DELETE', '/api/v1/devices/' + encodeURIComponent(d.peer)); toast(tr('已解绑')); await load(); } catch (ex) { fail(ex); }
          } }, tr('解绑'))) },
      ], r.devices, tr('还没有绑定设备')));
    }
    await load();
  }

  async function network(m) {
    add(m, pageTitle(tr('网络')));
    let n;
    try { n = await api('GET', '/api/v1/network'); }
    catch (e) { add(m, h('div', { class: 'card warn' }, e.code === 'not_ready' ? tr('协调器尚未启动，暂无网络信息。') : e.message)); return; }
    add(m, h('div', { class: 'card' }, kv([[tr('协调器'), h('code', { class: 'mono' }, n.coordinator)], [tr('中继凭证公钥'), n.pass_key ? h('code', { class: 'mono' }, n.pass_key) : h('span', { class: 'muted' }, tr('未启用（开放中继）'))]])),
      section('server', tr('中继'), h('small', null, tr('客户端按自己测得的延迟自动选择；「延迟」是协调器到该中继的往返时间，每 30 秒更新；「可达」表示协调器在该中继上有预约'))),
      h('div', { class: 'card' }, table([
        { title: tr('名称'), cell: r => r.name || '—' }, { title: tr('地区'), cell: r => r.region || '—' },
        { title: tr('地址'), cell: r => h('div', null, r.addr.split(',').map(a => h('div', null, h('code', { class: 'mono' }, a)))) },
        { title: tr('延迟'), num: true, cell: r => ST.rtt(r.rtt_ms) },
        { title: tr('协调器'), cell: r => badge(r.ok ? tr('可达') : tr('不可达'), r.ok ? 'ok' : 'bad') },
      ], n.relays, tr('还没有登记中继'))));
  }

  async function account(m) {
    const u = me().user;
    const mfa = u.mfa_enrolled_at ? [badge(tr('已启用'), 'ok'), ' ' + tr('绑定于 {time}', { time: time(u.mfa_enrolled_at) })] : (u.is_admin ? badge(tr('未启用'), 'warn') : h('span', { class: 'muted' }, tr('未启用（仅管理员需要）')));
    add(m, pageTitle(tr('账户')), h('div', { class: 'card' }, kv([[tr('用户名'), u.username], [tr('用户 ID'), String(u.id)], [tr('注册时间'), time(u.created_at)], [tr('角色'), u.is_admin ? tr('管理员') : tr('普通用户')], [tr('两步验证'), h('span', null, mfa)]]),
      state.info && state.info.support_email ? h('p', { class: 'muted', id: 'support-email' }, parts('有问题或建议？发邮件到 {email}。', { email: h('a', { href: 'mailto:' + state.info.support_email }, state.info.support_email) })) : null,
      u.is_admin ? h('p', { class: 'muted' }, tr('管理员登录必须通过验证器 App 的两步验证。更换手机时请让其他管理员在控制台重置，或在服务器上执行 portal mfa-reset。')) : null));
    const oldP = input({ type: 'password', autocomplete: 'current-password', required: true });
    const newP = input({ type: 'password', autocomplete: 'new-password', minlength: 8, maxlength: 256, required: true, placeholder: tr('8-256 位') });
    const newP2 = input({ type: 'password', autocomplete: 'new-password', required: true });
    const revoke = h('input', { type: 'checkbox' });
    const err = h('div', { class: 'error' });
    const form = h('form', {
      class: 'form', onsubmit: async e => {
        e.preventDefault(); err.textContent = '';
        if (newP.value !== newP2.value) { err.textContent = tr('两次输入的新密码不一致'); return; }
        try {
          const r = await api('POST', '/api/v1/password', { old_password: oldP.value, new_password: newP.value, revoke_others: revoke.checked });
          oldP.value = newP.value = newP2.value = '';
          toast(r.tokens_revoked ? tr('密码已修改，已吊销其他会话：{n}', { n: r.tokens_revoked }) : tr('密码已修改'));
        } catch (ex) { err.textContent = ex.code === 'bad_credentials' ? tr('当前密码错误') : ex.message; }
      }
    }, field(tr('当前密码'), oldP), field(tr('新密码'), newP), field(tr('重复新密码'), newP2),
      h('label', { class: 'check' }, revoke, tr('同时吊销其他所有 token 和会话（本浏览器除外）')), err, h('div', null, h('button', { class: 'primary', type: 'submit' }, tr('修改密码'))));
    add(m, section('lock', tr('修改密码')), h('div', { class: 'card' }, form));
  }

  const FEEDBACK_KIND = { bug: () => tr('问题'), idea: () => tr('建议'), other: () => tr('其他') };

  async function feedback(m) {
    const kind = h('select', { id: 'feedback-kind' }, Object.entries(FEEDBACK_KIND).map(([k, label]) => h('option', { value: k }, label())));
    const text = h('textarea', { id: 'feedback-text', rows: 6, maxlength: 4000, dir: 'auto', class: 'wide' });
    const err = h('div', { class: 'error', id: 'feedback-error' });
    const list = h('div', { class: 'card', id: 'feedback-list' });
    const form = h('form', {
      class: 'form', id: 'feedback-form', onsubmit: async e => {
        e.preventDefault(); err.textContent = '';
        if (!text.value.trim()) { err.textContent = tr('请填写反馈内容'); text.focus(); return; }
        try {
          await api('POST', '/api/v1/feedback', { category: kind.value, message: text.value, source: 'portal', lang: I18N.lang });
          text.value = '';
          toast(tr('反馈已提交，谢谢！'), 'ok');
          await load();
        } catch (ex) { err.textContent = ex.code === 'throttled' ? tr('提交太频繁，请稍后再试') : ex.message; }
      }
    }, field(tr('类型'), kind), field(tr('内容'), text), err, h('div', null, h('button', { class: 'primary', type: 'submit', id: 'feedback-send' }, tr('提交反馈'))));
    add(m, pageTitle(tr('反馈')), h('div', { class: 'card' }, h('p', { class: 'muted' }, tr('告诉我们你遇到的问题或建议。请不要填写密码、token 或 prompt 内容。我们可能会通过账户邮箱回复你。')), form),
      section('chat', tr('我的反馈')), list);
    async function load() {
      const r = await api('GET', '/api/v1/feedback');
      add(clear(list), table([
        { title: tr('时间'), cell: f => time(f.created_at) },
        { title: tr('类型'), cell: f => (FEEDBACK_KIND[f.category] || (() => f.category))() },
        { title: tr('内容'), cell: f => h('div', { class: 'feedback-text', dir: 'auto' }, f.message) },
        { title: tr('状态'), cell: f => f.status === 'done' ? badge(tr('已处理'), 'ok') : badge(tr('已收到')) },
      ], r.feedback, tr('还没有提交过反馈')));
    }
    await load();
  }

  const go = ST.router({ home, ledger, jobs, tokens, devices, network, account, feedback }, nav, main, 'home');
  await go();
})();
