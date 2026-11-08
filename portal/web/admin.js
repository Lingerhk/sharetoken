// The operator console: platform overview, users, relays, jobs, nodes, audits, disputes, ledger.
'use strict';
(async () => {
  const { api, h, clear, add, credit, signed, num, time, ago, peer, status, badge, toast, fail, modal, confirm, secret, table, kv, field, input, state } = ST;
  const app = ST.$('app');

  await ST.loadInfo().catch(fail);
  while (!(await ST.loadMe())) await ST.renderLogin(app, { title: '管理控制台', allowRegister: false });
  const me = () => state.me;
  if (!me().user.is_admin) {
    add(clear(app), h('div', { class: 'card login' }, h('h1', null, '管理控制台'), h('p', null, '账户 ', h('b', null, me().user.username), ' 不是管理员。'),
      h('div', { class: 'row' }, h('a', { href: '/app/' }, '返回用户门户'), h('button', { class: 'small', onclick: ST.logout }, '退出'))));
    return;
  }

  const mailBadge = h('span', { class: 'nav-count', id: 'mail-unread', hidden: true });
  const nav = h('nav', { class: 'sidenav' },
    h('a', { href: '#/overview' }, '总览'), h('a', { href: '#/users' }, '用户'), h('a', { href: '#/relays' }, '中继'), h('a', { href: '#/jobs' }, '作业'),
    h('a', { href: '#/nodes' }, '节点'), h('a', { href: '#/audits' }, '审计'), h('a', { href: '#/disputes' }, '争议'), h('a', { href: '#/ledger' }, '账本'), h('a', { href: '#/mail' }, '邮件', mailBadge));
  const main = h('main');
  add(clear(app), 
    h('header', { class: 'topbar' }, h('span', { class: 'brand' }, 'sharetoken', h('small', null, '管理控制台')), h('span', { class: 'spacer' }),
      h('a', { href: '/app/' }, '用户门户'), h('span', { class: 'who' }, me().user.username), h('button', { class: 'small', onclick: ST.logout }, '退出')),
    h('div', { class: 'layout' }, nav, main));

  const tile = (label, value, sub, cls) => h('div', { class: 'tile' }, h('div', { class: 'label' }, label), h('div', { class: 'value ' + (cls || '') }, value), sub ? h('div', { class: 'sub' }, sub) : null);
  const moreButton = fn => h('div', { class: 'row end' }, h('button', { onclick: fn }, '加载更多'));
  const userLink = name => name ? h('a', { href: '#/users/' + encodeURIComponent(name) }, name) : null;
  const acct = (a, user) => user ? [userLink(user), ' ', h('span', { class: 'muted mono' }, a)] : h('code', { class: 'mono' }, a);
  function act(path, body, okMsg, reload) {
    return async () => { try { await api('POST', path, body); toast(okMsg || '已完成'); await reload(); } catch (ex) { fail(ex); } };
  }

  // ---- overview ----
  async function overview(m) {
    const o = await api('GET', '/api/v1/admin/overview');
    const jobs = o.jobs || {};
    add(m, h('h1', null, '平台总览'),
      h('div', { class: 'tiles' },
        tile('用户', num(o.users), o.disabled_users ? o.disabled_users + ' 个已禁用' : '无禁用'),
        tile('设备', num(o.devices)),
        tile('流通 credit', credit(o.circulating_micros), '已发放到网络中的额度'),
        tile('平台手续费', credit(o.platform_fees_micros), '费率 ' + (o.fee_bps / 100).toFixed(2) + '%'),
        tile('冻结中', credit(o.open_holds_micros), '进行中作业的预留额'),
        tile('账本', o.books_balance ? '平衡' : '不平衡', o.books_error || '从流水回放各账户一致', o.books_balance ? 'ok' : 'bad'),
        tile('中继', o.relays_ok + ' / ' + o.relays_enabled, '可达 / 启用（共 ' + o.relays + ' 个）', o.relays_enabled && o.relays_ok === 0 ? 'bad' : ''),
        tile('作业', num(Object.values(jobs).reduce((a, b) => a + b, 0)), Object.entries(jobs).map(([k, v]) => (ST.STATUS[k] ? ST.STATUS[k][0] : k) + ' ' + v).join('，') || '无', jobs.disputed ? 'warn' : '')),
      h('div', { class: 'card' }, kv([['协调器', o.coordinator ? h('code', { class: 'mono' }, o.coordinator) : h('span', { class: 'warn' }, '尚未启动')],
        ['注册', state.info.registration_open ? '开放（赠送 ' + credit(state.info.signup_credit_micros) + ' credit）' : '关闭，仅管理员创建用户'],
        ['中继凭证', state.info.passes_enabled ? '已启用' : '未启用（开放中继）'], ['浏览器会话有效期', state.info.session_hours + ' 小时']])));
    const a = await api('GET', '/api/v1/admin/accounts');
    add(m, h('h2', null, '账户余额', h('small', null, 'external 为负数表示已发放的 credit')),
      h('div', { class: 'card' }, table([
        { title: '账户', cell: r => acct(r.account, r.user) },
        { title: '余额', num: true, cell: r => h('span', { class: r.balance_micros < 0 ? 'bad' : '' }, credit(r.balance_micros)) },
        { title: '冻结', num: true, cell: r => credit(r.held_micros) },
      ], a.accounts)));
  }

  // ---- users ----
  async function users(m, rest) {
    if (rest && rest[0]) return userDetail(m, decodeURIComponent(rest[0]));
    const q = input({ type: 'search', placeholder: '搜索用户名' });
    const list = h('div', { class: 'card' });
    const total = h('span', { class: 'muted' });
    const createBtn = h('button', { class: 'primary', onclick: createUser }, '新建用户');
    add(m, h('h1', null, '用户'), h('div', { class: 'toolbar' }, q, total, h('span', { class: 'spacer' }), createBtn), list);
    let offset = 0, rows = [];
    async function load(more) {
      if (!more) { offset = 0; rows = []; }
      const r = await api('GET', '/api/v1/admin/users/search?limit=50&offset=' + offset + '&q=' + encodeURIComponent(q.value.trim()));
      rows = rows.concat(r.users); offset = rows.length;
      total.textContent = '共 ' + r.total + ' 个';
      add(clear(list), table([
        { title: '用户名', cell: u => [userLink(u.username), ' ', u.is_admin ? badge('管理员', 'accent') : null, ' ', u.disabled ? badge('已禁用', 'bad') : null] },
        { title: 'ID', num: true, cell: u => String(u.id) },
        { title: '余额', num: true, cell: u => credit(u.available_micros) },
        { title: '冻结', num: true, cell: u => credit(u.held_micros) },
        { title: '设备', num: true, cell: u => String(u.devices) },
        { title: 'Token', num: true, cell: u => String(u.tokens) },
        { title: '注册', cell: u => time(u.created_at) },
      ], rows, '没有匹配的用户'), rows.length < r.total ? moreButton(() => load(true)) : null);
    }
    let timer; q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => load(false), 250); });
    async function createUser() {
      const name = input({ type: 'text', placeholder: '3-32 位小写字母、数字、_ . -', autocomplete: 'off' });
      const pass = input({ type: 'password', placeholder: '8-256 位', autocomplete: 'new-password' });
      const admin = h('input', { type: 'checkbox' });
      if (!await modal({ title: '新建用户', okText: '创建', body: h('div', { class: 'form' }, field('用户名', name), field('初始密码', pass), h('label', { class: 'check' }, admin, '管理员')) })) return;
      try { const u = await api('POST', '/api/v1/admin/users', { username: name.value.trim(), password: pass.value, is_admin: admin.checked }); toast('已创建 ' + u.username); location.hash = '#/users/' + encodeURIComponent(u.username); }
      catch (ex) { fail(ex); }
    }
    await load(false);
  }

  async function userDetail(m, name) {
    const head = h('h1'); const body = h('div'); add(m, head, body);
    async function load() {
      const d = await api('GET', '/api/v1/admin/users/' + encodeURIComponent(name));
      const u = d.user, isMe = u.id === me().user.id;
      add(clear(head), h('a', { href: '#/users' }, '用户'), ' / ', u.username, ' ', u.is_admin ? badge('管理员', 'accent') : null, ' ', u.disabled ? badge('已禁用', 'bad') : null, ' ',
        u.mfa_enrolled_at ? badge('两步验证', 'ok') : (u.is_admin ? badge('未绑定两步验证', 'warn') : null));
      const actions = h('div', { class: 'row' },
        h('button', { class: 'primary', onclick: creditDialog }, '发放 / 扣减 credit'),
        h('button', { onclick: passwordDialog }, '重置密码'),
        h('button', { disabled: !u.mfa_enrolled_at, onclick: async () => {
          if (!await confirm('重置两步验证', '清除 ' + u.username + ' 的验证器绑定并吊销其全部 token 和会话；下次登录时重新绑定。' + (isMe ? '这会立即登出你自己。' : ''), '重置')) return;
          await act('/api/v1/admin/users/' + encodeURIComponent(name) + '/mfa/reset', undefined, '已重置两步验证', isMe ? () => location.reload() : load)();
        } }, '重置两步验证'),
        h('button', { disabled: isMe, onclick: act('/api/v1/admin/users/' + encodeURIComponent(name) + '/admin', { is_admin: !u.is_admin }, u.is_admin ? '已撤销管理员' : '已设为管理员', load) }, u.is_admin ? '撤销管理员' : '设为管理员'),
        h('button', { class: u.disabled ? '' : 'danger', disabled: isMe, onclick: async () => {
          if (!u.disabled && !await confirm('禁用用户', '禁用后 ' + u.username + ' 无法登录，其 token 立即失效，设备被协调器拒绝。', '禁用')) return;
          await act('/api/v1/admin/users/' + encodeURIComponent(name) + (u.disabled ? '/enable' : '/disable'), undefined, u.disabled ? '已启用' : '已禁用', load)();
        } }, u.disabled ? '启用' : '禁用'),
        h('button', { class: 'danger', onclick: async () => {
          if (!await confirm('吊销全部 token', '该用户的所有 token 和浏览器会话都会失效，设备需要重新登录。', '全部吊销')) return;
          await act('/api/v1/admin/users/' + encodeURIComponent(name) + '/tokens/revoke', undefined, '已吊销', load)();
        } }, '吊销全部 token'));
      const b = d.balance, us = d.usage;
      add(clear(body), 
        h('div', { class: 'tiles' },
          tile('可用余额', credit(b.available_micros), b.account), tile('冻结', credit(b.held_micros)),
          tile('买入', us.bought.jobs + ' 单', '花费 ' + credit(us.bought.micros)), tile('卖出', us.sold.jobs + ' 单', '净收入 ' + credit(us.sold.micros)),
          tile('注册', time(u.created_at), 'ID ' + u.id, 'small')),
        h('div', { class: 'card' }, actions),
        h('h2', null, '设备'), h('div', { class: 'card' }, table([
          { title: '名称', cell: x => x.name }, { title: 'Peer ID', cell: x => h('code', { class: 'mono' }, x.peer) },
          { title: '绑定', cell: x => time(x.created_at) }, { title: '最近活动', cell: x => ago(x.last_seen_at) },
          { title: '', cell: x => h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('解绑设备', '解绑 ' + x.name + '（' + ST.short(x.peer, 8) + '）后协调器立即拒绝它。', '解绑')) return;
            try { await api('DELETE', '/api/v1/admin/users/' + encodeURIComponent(name) + '/devices/' + encodeURIComponent(x.peer)); toast('已解绑'); await load(); } catch (ex) { fail(ex); }
          } }, '解绑') },
        ], d.devices, '没有设备')),
        h('h2', null, 'Token'), h('div', { class: 'card' }, table([
          { title: '名称', cell: t => t.name }, { title: '前缀', cell: t => h('code', null, t.prefix + '…') }, { title: '创建', cell: t => time(t.created_at) },
          { title: '最近使用', cell: t => ago(t.last_used_at) }, { title: '过期', cell: t => t.expires_at ? time(t.expires_at) : '永不' },
          { title: '', cell: t => h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('吊销 token', '吊销「' + t.name + '」。', '吊销')) return;
            try { await api('DELETE', '/api/v1/admin/users/' + encodeURIComponent(name) + '/tokens/' + t.id); toast('已吊销'); await load(); } catch (ex) { fail(ex); }
          } }, '吊销') },
        ], d.tokens, '没有有效 token')),
        h('h2', null, '账单'), h('div', { class: 'card' }, await ledgerOf('/api/v1/admin/users/' + encodeURIComponent(name) + '/ledger')),
        h('h2', null, '作业'), h('div', { class: 'card' }, await jobsOf('/api/v1/admin/users/' + encodeURIComponent(name) + '/jobs')));

      async function creditDialog() {
        const amount = input({ type: 'number', step: 'any', placeholder: '正数发放，负数扣减（单位 credit）' });
        const memo = input({ type: 'text', maxlength: 128, placeholder: '例如 充值、纠错' });
        if (!await modal({ title: '调整 ' + u.username + ' 的 credit', okText: '提交', body: h('div', { class: 'form' }, field('金额（credit）', amount), field('备注', memo), h('p', { class: 'muted' }, '当前可用 ' + credit(b.available_micros) + '；扣减不能超过可用余额。')) })) return;
        const micros = Math.round(Number(amount.value) * 1e6);
        if (!micros) { toast('金额无效', 'bad'); return; }
        try { const r = await api('POST', '/api/v1/admin/users/' + encodeURIComponent(name) + '/credit', { micros, memo: memo.value.trim() }); toast('已调整，可用余额 ' + credit(r.available_micros)); await load(); }
        catch (ex) { fail(ex); }
      }
      async function passwordDialog() {
        const pass = input({ type: 'password', placeholder: '8-256 位', autocomplete: 'new-password' });
        const revoke = h('input', { type: 'checkbox', checked: true });
        if (!await modal({ title: '重置 ' + u.username + ' 的密码', okText: '重置', body: h('div', { class: 'form' }, field('新密码', pass), h('label', { class: 'check' }, revoke, '同时吊销该用户的全部 token 和会话')) })) return;
        try { const r = await api('POST', '/api/v1/admin/users/' + encodeURIComponent(name) + '/password', { new_password: pass.value, revoke_tokens: revoke.checked }); toast('密码已重置' + (r.tokens_revoked ? '，吊销 ' + r.tokens_revoked + ' 个 token' : '')); await load(); }
        catch (ex) { fail(ex); }
      }
    }
    await load();
  }

  function ledgerTable(entries) {
    return table([
      { title: '时间', cell: e => time(e.at) }, { title: '摘要', cell: e => e.memo }, { title: '作业', cell: e => e.job_id ? peer(e.job_id) : '' },
      { title: '金额', num: true, cell: e => h('span', { class: e.amount_micros > 0 ? 'ok' : 'bad' }, signed(e.amount_micros)) },
    ], entries, '没有账单记录');
  }
  async function ledgerOf(path) {
    const box = h('div'); let all = [];
    async function load() {
      const before = all.length ? all[all.length - 1].seq : 0;
      const r = await api('GET', path + '?limit=30' + (before ? '&before=' + before : ''));
      all = all.concat(r.entries); add(clear(box), ledgerTable(all), r.entries.length === 30 ? moreButton(load) : null);
    }
    await load(); return box;
  }
  async function jobsOf(path) {
    const box = h('div'); let all = [];
    async function load() {
      const before = all.length ? all[all.length - 1].created_at : 0;
      const r = await api('GET', path + '?limit=30' + (before ? '&before=' + before : ''));
      all = all.concat(r.jobs);
      add(clear(box), table([
        { title: '时间', cell: j => time(j.created_at) }, { title: '角色', cell: j => badge(j.role === 'buy' ? '买入' : '卖出', j.role === 'buy' ? 'accent' : 'ok') },
        { title: '模型', cell: j => j.model }, { title: '状态', cell: j => status(j.status) }, { title: '作业', cell: j => peer(j.job_id) },
        { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
        { title: '费用', num: true, cell: j => credit(j.cost_micros) }, { title: '手续费', num: true, cell: j => credit(j.fee_micros) },
      ], all, '没有作业'), r.jobs.length === 30 ? moreButton(load) : null);
    }
    await load(); return box;
  }

  // ---- relays ----
  async function relays(m) {
    const list = h('div', { class: 'card' });
    add(m, h('h1', null, '中继', h('small', null, '协调器每 15 秒同步一次登记表，并回写是否可达')), h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: addRelay }, '登记中继')), list);
    async function load() {
      const r = await api('GET', '/api/v1/admin/relays');
      add(clear(list), table([
        { title: 'ID', num: true, cell: x => String(x.id) },
        { title: '名称 / 地区', cell: x => [x.name || '—', ' ', h('span', { class: 'muted' }, x.region || '')] },
        { title: '地址', cell: x => h('div', null, x.addr.split(',').map(a => h('div', null, h('code', { class: 'mono' }, a)))) },
        { title: '状态', cell: x => [badge(x.enabled ? '启用' : '停用', x.enabled ? 'ok' : ''), ' ', badge(x.coordinator_ok ? '协调器可达' : '不可达', x.coordinator_ok ? 'ok' : 'bad')] },
        { title: '延迟', num: true, cell: x => ST.rtt(x.rtt_ms) },
        { title: '检查', cell: x => [ago(x.checked_at), x.detail ? h('div', { class: 'muted' }, x.detail) : null] },
        { title: '', cell: x => h('span', { class: 'row' },
          h('button', { class: 'small', onclick: async () => {
            const nm = input({ type: 'text', value: x.name || '', maxlength: 64 }), rg = input({ type: 'text', value: x.region || '', maxlength: 64 });
            if (!await modal({ title: '编辑中继 #' + x.id, body: h('div', { class: 'form' }, field('名称', nm), field('地区', rg)) })) return;
            try { await api('PATCH', '/api/v1/admin/relays/' + x.id, { name: nm.value, region: rg.value }); toast('已保存'); await load(); } catch (ex) { fail(ex); }
          } }, '编辑'),
          h('button', { class: 'small', onclick: act('/api/v1/admin/relays/' + x.id + (x.enabled ? '/disable' : '/enable'), undefined, x.enabled ? '已停用' : '已启用', load) }, x.enabled ? '停用' : '启用'),
          h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('删除中继', '删除 #' + x.id + ' 后用户将不再拿到它；协调器会放弃其预约。', '删除')) return;
            try { await api('DELETE', '/api/v1/admin/relays/' + x.id); toast('已删除'); await load(); } catch (ex) { fail(ex); }
          } }, '删除')) },
      ], r.relays, '还没有登记中继'));
    }
    async function addRelay() {
      const addr = h('textarea', { rows: 3, placeholder: '/ip4/203.0.113.7/tcp/8091/p2p/12D3Koo…,/ip6/2001:db8::7/tcp/8091/p2p/12D3Koo…' });
      const nm = input({ type: 'text', maxlength: 64, placeholder: '例如 relay-sh' }), rg = input({ type: 'text', maxlength: 64, placeholder: '例如 cn-east' });
      if (!await modal({ title: '登记中继', okText: '登记', body: h('div', { class: 'form' }, field('多地址（中继启动时打印；同一 peer 的多个地址用逗号连接）', addr), h('div', { class: 'row' }, field('名称', nm), field('地区', rg))) })) return;
      try { await api('POST', '/api/v1/admin/relays', { addr: addr.value.trim(), name: nm.value.trim(), region: rg.value.trim() }); toast('已登记'); await load(); } catch (ex) { fail(ex); }
    }
    await load();
  }

  // ---- jobs ----
  async function jobs(m) {
    const st = h('select', { onchange: () => load(false) }, h('option', { value: '' }, '全部状态'), Object.entries(ST.STATUS).map(([k, v]) => h('option', { value: k }, v[0])));
    const user = input({ type: 'search', placeholder: '按用户名筛选' });
    const list = h('div', { class: 'card' });
    add(m, h('h1', null, '作业', h('small', null, '协调器保留期内的作业（默认 30 天）')), h('div', { class: 'toolbar' }, st, user), list);
    let all = [];
    async function load(more) {
      if (!more) all = [];
      const before = all.length ? all[all.length - 1].created_at : 0;
      let r;
      try { r = await api('GET', '/api/v1/admin/jobs?limit=50&status=' + st.value + '&user=' + encodeURIComponent(user.value.trim()) + (before ? '&before=' + before : '')); }
      catch (ex) { add(clear(list), h('div', { class: 'empty' }, ex.code === 'not_found' ? '没有这个用户' : ex.message)); return; }
      all = all.concat(r.jobs);
      add(clear(list), table([
        { title: '时间', cell: j => time(j.created_at) }, { title: '作业', cell: j => peer(j.job_id) }, { title: '模型', cell: j => j.model },
        { title: '状态', cell: j => [status(j.status), j.dispute_outcome ? h('div', { class: 'muted' }, '裁决 ' + j.dispute_outcome) : null] },
        { title: '买家', cell: j => [peer(j.buyer), h('div', { class: 'muted mono' }, j.buyer_acct)] },
        { title: '节点', cell: j => [peer(j.node), h('div', { class: 'muted mono' }, j.node_acct)] },
        { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
        { title: '费用', num: true, cell: j => credit(j.cost_micros) }, { title: '手续费', num: true, cell: j => credit(j.fee_micros) },
        { title: '完成', cell: j => time(j.done_at) },
      ], all, '没有作业'), r.jobs.length === 50 ? moreButton(() => load(true)) : null);
    }
    let timer; user.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => load(false), 250); });
    await load(false);
  }

  // ---- nodes / audits / disputes ----
  async function nodes(m) {
    const r = await api('GET', '/api/v1/admin/nodes');
    const now = Date.now() * 1e6;
    add(m, h('h1', null, '节点', h('small', null, '卖家设备的信誉记录；连续两次审计不符会被暂停撮合')), h('div', { class: 'card' }, table([
      { title: '节点', cell: n => [peer(n.node), n.user ? h('div', null, userLink(n.user)) : h('div', { class: 'muted' }, '未绑定')] },
      { title: '信誉', num: true, cell: n => String(n.reputation) },
      { title: '成功 / 失败', num: true, cell: n => n.successes + ' / ' + n.failures },
      { title: '争议败诉', num: true, cell: n => String(n.disputes_lost) },
      { title: '审计 通过 / 不符', num: true, cell: n => n.audits_passed + ' / ' + n.audits_failed + (n.audit_fail_streak ? '（连续 ' + n.audit_fail_streak + '）' : '') },
      { title: '状态', cell: n => n.suspended_until > now ? badge('暂停至 ' + time(n.suspended_until), 'bad') : badge('正常', 'ok') },
    ], r.nodes, '还没有节点记录')));
  }
  async function audits(m) {
    const r = await api('GET', '/api/v1/admin/audits?limit=200');
    const V = { match: ['一致', 'ok'], mismatch: ['不符', 'bad'], inconclusive: ['无法判定', 'warn'] };
    add(m, h('h1', null, '审计', h('small', null, '平台抽检：用参考模型复算节点的回答')), h('div', { class: 'card' }, table([
      { title: '时间', cell: a => time(a.at) }, { title: '节点', cell: a => peer(a.node) }, { title: '模型', cell: a => a.model },
      { title: '结论', cell: a => badge((V[a.verdict] || [a.verdict])[0], (V[a.verdict] || [])[1]) },
      { title: '相似度', num: true, cell: a => a.similarity.toFixed(2) }, { title: '详情', cell: a => a.detail }, { title: '作业', cell: a => peer(a.job_id) },
    ], r.audits, '还没有审计记录')));
  }
  async function disputes(m) {
    const list = h('div', { class: 'card' });
    add(m, h('h1', null, '争议', h('small', null, '证据无法自动裁决的单需要人工裁决；判给节点即结算，判给买家即退款并扣节点信誉')), list);
    async function load() {
      const r = await api('GET', '/api/v1/admin/disputes?limit=200');
      add(clear(list), table([
        { title: '时间', cell: d => time(d.created_at) }, { title: '作业', cell: d => peer(d.job_id) }, { title: '模型', cell: d => d.model },
        { title: '状态', cell: d => status(d.status) },
        { title: '买家 / 节点', cell: d => [peer(d.buyer), ' → ', peer(d.node)] },
        { title: '金额', num: true, cell: d => credit(d.cost) },
        { title: '原因', cell: d => [d.reason, d.outcome ? h('div', { class: 'muted' }, '裁决：' + d.outcome + (d.detail ? '，' + d.detail : '')) : null] },
        { title: '', cell: d => d.status !== 'disputed' ? '' : h('span', { class: 'row' },
          h('button', { class: 'small', onclick: async () => {
            if (!await confirm('判给节点', '作业 ' + ST.short(d.job_id, 8) + '：按原价 ' + credit(d.cost) + ' 结算给节点。', '付款给节点', false)) return;
            try { await api('POST', '/api/v1/admin/disputes/' + encodeURIComponent(d.job_id) + '/resolve', { decision: 'pay' }); toast('已结算'); await load(); } catch (ex) { fail(ex); }
          } }, '判给节点'),
          h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('判给买家', '作业 ' + ST.short(d.job_id, 8) + '：全额退款给买家，节点记一次争议败诉。', '退款')) return;
            try { await api('POST', '/api/v1/admin/disputes/' + encodeURIComponent(d.job_id) + '/resolve', { decision: 'refund' }); toast('已退款'); await load(); } catch (ex) { fail(ex); }
          } }, '退款给买家')) },
      ], r.disputes, '没有争议'));
    }
    await load();
  }

  // ---- ledger ----
  async function ledger(m) {
    const account = input({ type: 'search', placeholder: '按账户筛选，如 user:3、platform:fees' });
    const list = h('div', { class: 'card' });
    add(m, h('h1', null, '账本', h('small', null, '复式流水：每笔从一个账户转到另一个账户')), h('div', { class: 'toolbar' }, account), list);
    let all = [];
    async function load(more) {
      if (!more) all = [];
      const before = all.length ? all[all.length - 1].seq : 0;
      const r = await api('GET', '/api/v1/admin/ledger?limit=50&account=' + encodeURIComponent(account.value.trim()) + (before ? '&before=' + before : ''));
      all = all.concat(r.entries);
      add(clear(list), table([
        { title: '#', num: true, cell: e => String(e.seq) }, { title: '时间', cell: e => time(e.at) },
        { title: '从', cell: e => acct(e.from, e.from_user) }, { title: '到', cell: e => acct(e.to, e.to_user) },
        { title: '金额', num: true, cell: e => credit(e.amount) }, { title: '摘要', cell: e => e.memo }, { title: '作业', cell: e => e.job_id ? peer(e.job_id) : '' },
      ], all, '没有流水'), r.entries.length === 50 ? moreButton(() => load(true)) : null);
    }
    let timer; account.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => load(false), 250); });
    await load(false);
  }

  // ---- mail ----
  // Mail tables keep Unix seconds; ST.time and ST.ago take nanoseconds.
  const secTime = s => time(s * 1e9), secAgo = s => ago(s * 1e9);
  const OUT_STATUS = { queued: ['排队中', 'accent'], sending: ['发送中', 'accent'], sent: ['对方已接受', 'ok'], failed: ['失败', 'bad'], cancelled: ['已取消', ''] };
  const OUT_KIND = { verify: '验证码', notice: '通知', reply: '回复', compose: '手写', test: '测试' };
  const size = n => n < 1024 ? n + ' B' : n < 1 << 20 ? (n / 1024).toFixed(1) + ' KB' : n < 1 << 30 ? (n / (1 << 20)).toFixed(1) + ' MB' : (n / (1 << 30)).toFixed(2) + ' GB';
  function setUnread(n) { mailBadge.hidden = !n; mailBadge.textContent = n ? String(n) : ''; }
  async function refreshUnread() { try { setUnread((await api('GET', '/api/v1/admin/mail/status')).overview.unread); } catch (e) { /* the page itself reports errors */ } }

  // Mail is written by strangers: bytes from it are only ever saved to a file,
  // and its text only goes through textContent (ST.h).
  async function saveFile(path, name) {
    try {
      const r = await fetch(path, { headers: { 'X-Sharetoken-Web': '1' } });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const url = URL.createObjectURL(await r.blob());
      const a = h('a', { href: url, download: name });
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (ex) { fail(ex); }
  }

  async function mail(m, rest) {
    const sub = rest[0] || 'inbox';
    const tab = (key, label) => h('a', { href: '#/mail/' + key, class: 'tab-link' + (sub === key ? ' active' : '') }, label);
    add(m, h('h1', null, '邮件'),
      h('div', { class: 'toolbar' }, h('div', { class: 'tab-links' }, tab('inbox', '收件箱'), tab('archived', '已归档'), tab('bounces', '退信'), tab('outbox', '发件箱'), tab('status', '服务状态')),
        h('span', { class: 'spacer' }), h('button', { class: 'primary', id: 'mail-compose', onclick: () => composeMail() }, '写邮件')));
    const body = h('div'); add(m, body);
    if (sub === 'outbox') { await (rest[1] ? outMessage(body, rest[1]) : outbox(body)); return; }
    if (sub === 'status') { await mailStatus(body); return; }
    await (rest[1] ? inMessage(body, rest[1], sub) : inbox(body, sub));
    refreshUnread();
  }

  async function composeMail(to, subject) {
    const toI = input({ type: 'email', id: 'mail-to', value: to || '', placeholder: 'user@example.com' });
    const subj = input({ type: 'text', id: 'mail-subject', value: subject || '', maxlength: 200 });
    const text = h('textarea', { id: 'mail-text', rows: 12, class: 'wide' });
    const from = h('select', { id: 'mail-from' }, h('option', { value: 'support' }, '客服地址（可回复）'), h('option', { value: 'team' }, '团队地址'));
    if (!await modal({ title: '写邮件', okText: '发送', body: h('div', { class: 'form' }, field('收件人', toI), field('发件人', from), field('主题', subj), field('正文', text)) })) return;
    try {
      const r = await api('POST', '/api/v1/admin/mail/send', { to: toI.value.trim(), subject: subj.value, text: text.value, from: from.value });
      toast('已加入发送队列');
      location.hash = '#/mail/outbox/' + r.outbox_id;
    } catch (ex) { fail(ex); }
  }

  async function inbox(root, sub) {
    const q = input({ type: 'search', id: 'mail-search', placeholder: '搜索发件人、主题' });
    const unread = h('input', { type: 'checkbox', id: 'mail-unread-only' });
    const list = h('div', { class: 'card' });
    add(root, h('div', { class: 'toolbar' }, q, sub === 'inbox' ? h('label', { class: 'check' }, unread, '只看未读') : null), list);
    let rows = [];
    async function load(more) {
      if (!more) rows = [];
      const before = rows.length ? rows[rows.length - 1].id : 0;
      const qs = 'limit=50&q=' + encodeURIComponent(q.value.trim()) + (before ? '&before=' + before : '') + (unread.checked ? '&unread=1' : '')
        + (sub === 'archived' ? '&status=archived' : sub === 'bounces' ? '&status=all&bounces=only' : '');
      const r = await api('GET', '/api/v1/admin/mail/inbox?' + qs);
      rows = rows.concat(r.messages);
      add(clear(list), table([
        { title: '发件人', cell: x => h('span', { class: x.read_at ? '' : 'strong' }, x.from || x.mail_from || '（未知）') },
        { title: '主题', cell: x => h('a', { href: '#/mail/' + sub + '/' + x.id, class: x.read_at ? '' : 'strong' }, x.subject || '（无主题）') },
        { title: '收件地址', cell: x => h('span', { class: 'muted' }, x.mailbox) },
        { title: '发件人验证', cell: x => authBadge(x) },
        { title: '状态', cell: x => x.replied_at ? badge('已回复', 'ok') : x.read_at ? '' : badge('未读', 'accent') },
        { title: '大小', num: true, cell: x => size(x.size) },
        { title: '收到', cell: x => secTime(x.received_at) },
      ], rows, sub === 'bounces' ? '没有退信' : '没有邮件'), r.messages.length === 50 ? moreButton(() => load(true)) : null);
    }
    let timer; q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => load(false), 250); });
    unread.addEventListener('change', () => load(false));
    await load(false);
  }

  const authBadge = x => x.is_bounce ? badge('退信', 'warn') : x.verified ? badge('已验证', 'ok') : badge('未验证', 'warn');

  async function inMessage(root, id, sub) {
    const base = '/api/v1/admin/mail/inbox/' + encodeURIComponent(id);
    const d = await api('GET', base);
    const x = d.message, p = d.parsed;
    if (!x.read_at) await api('POST', base + '/read', { read: true }).catch(() => {});
    const back = () => { location.hash = '#/mail/' + sub; };
    const archived = x.status === 'archived';
    const actions = h('div', { class: 'row' },
      h('button', { class: 'primary', id: 'mail-reply', disabled: !d.reply_to || x.is_bounce, onclick: reply }, '回复'),
      h('button', { id: 'mail-archive', onclick: async () => {
        try { await api('POST', base + '/status', { status: archived ? 'inbox' : 'archived' }); toast(archived ? '已移回收件箱' : '已归档'); back(); } catch (ex) { fail(ex); }
      } }, archived ? '移回收件箱' : '归档'),
      h('button', { onclick: async () => { try { await api('POST', base + '/read', { read: false }); toast('已标为未读'); back(); } catch (ex) { fail(ex); } } }, '标为未读'),
      h('button', { onclick: () => saveFile(base + '/raw', 'message-' + x.id + '.eml') }, '下载原文'),
      h('button', { class: 'danger', onclick: async () => {
        if (!await confirm('删除邮件', '永久删除这封邮件，无法恢复。', '删除')) return;
        try { await api('DELETE', base); toast('已删除'); back(); } catch (ex) { fail(ex); }
      } }, '删除'));
    add(root, h('p', null, h('a', { href: '#/mail/' + sub }, '← 返回')),
      x.verified || x.is_bounce ? null : h('div', { class: 'card warn-card', id: 'mail-unverified' }, '这封邮件的发件人没有通过 SPF / DKIM 验证，发件人地址可能是伪造的。不要轻信其中的身份声明，也不要按邮件内容直接操作账户或打开附件。'),
      h('div', { class: 'card' }, h('h2', { class: 'mail-subject' }, p.subject || x.subject || '（无主题）'),
        kv([['发件人', [p.from || x.from || '（未知）', ' ', authBadge(x)]], ['回复到', d.reply_to || '（无可用地址）'], ['收件地址', x.mailbox], ['收到', secTime(x.received_at)],
          ['来源', x.remote_ip + '（' + x.helo + '），' + (x.tls ? 'TLS' : '明文') + '，SPF ' + x.spf + '，DKIM ' + x.dkim], ['信封发件人', x.mail_from || '（空）']]),
        actions),
      h('div', { class: 'card' }, p.from_html ? h('p', { class: 'muted' }, '这封邮件只有 HTML 版本，这里显示其中的文字；HTML 不会被加载。') : null,
        p.malformed ? h('p', { class: 'warn' }, '邮件结构有问题，内容可能不完整，可以下载原文查看。') : null,
        h('div', { class: 'mail-body', id: 'mail-body' }, p.text || '（没有文字内容）')),
      p.attachments.length ? h('div', { class: 'card' }, h('h3', null, '附件', h('small', null, '来自外部，打开前请确认来源')), table([
        { title: '文件', cell: a => a.filename || '（无文件名）' }, { title: '声明的类型', cell: a => h('span', { class: 'muted' }, a.type) }, { title: '大小', num: true, cell: a => size(a.size) },
        { title: '', cell: a => h('button', { class: 'small', onclick: () => saveFile(base + '/attachments/' + a.index, a.filename || 'attachment-' + a.index) }, '下载') },
      ], p.attachments)) : null);
    async function reply() {
      const quoted = '\n\n在 ' + secTime(x.received_at) + '，' + (p.from || x.from) + ' 写道：\n' + (p.text || '').split('\n').slice(0, 40).map(l => '> ' + l).join('\n');
      const text = h('textarea', { id: 'reply-text', rows: 14, class: 'wide' });
      text.value = quoted;
      const body = h('div', { class: 'form' }, h('p', { class: 'muted' }, '发给 ', h('b', null, d.reply_to), '，从客服地址发出'), field('正文', text));
      if (!await modal({ title: '回复', okText: '发送', body })) return;
      try {
        const r = await api('POST', base + '/reply', { text: text.value });
        toast('回复已加入发送队列'); location.hash = '#/mail/outbox/' + r.outbox_id;
      } catch (ex) { fail(ex); }
    }
    refreshUnread();
  }

  async function outbox(root) {
    const q = input({ type: 'search', id: 'out-search', placeholder: '搜索收件人、主题' });
    const st = h('select', { id: 'out-status' }, h('option', { value: '' }, '全部状态'), Object.entries(OUT_STATUS).map(([k, v]) => h('option', { value: k }, v[0])));
    const kind = h('select', { id: 'out-kind' }, h('option', { value: '' }, '全部类型'), Object.entries(OUT_KIND).map(([k, v]) => h('option', { value: k }, v)));
    const list = h('div', { class: 'card' });
    add(root, h('div', { class: 'toolbar' }, q, st, kind), list);
    let rows = [];
    async function load(more) {
      if (!more) rows = [];
      const before = rows.length ? rows[rows.length - 1].id : 0;
      const r = await api('GET', '/api/v1/admin/mail/outbox?limit=50&q=' + encodeURIComponent(q.value.trim()) + '&status=' + st.value + '&kind=' + kind.value + (before ? '&before=' + before : ''));
      rows = rows.concat(r.messages);
      add(clear(list), table([
        { title: '收件人', cell: x => x.to },
        { title: '主题', cell: x => h('a', { href: '#/mail/outbox/' + x.id }, x.sensitive ? '（含验证码，不显示）' : x.subject || '（无主题）') },
        { title: '类型', cell: x => OUT_KIND[x.kind] || x.kind },
        { title: '状态', cell: x => outBadge(x) },
        { title: '尝试', num: true, cell: x => String(x.attempts) },
        { title: '创建', cell: x => secTime(x.created_at) },
      ], rows, '没有发出的邮件'), r.messages.length === 50 ? moreButton(() => load(true)) : null);
    }
    let timer; q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => load(false), 250); });
    st.addEventListener('change', () => load(false)); kind.addEventListener('change', () => load(false));
    await load(false);
  }

  const outBadge = x => { const s = OUT_STATUS[x.status] || [x.status, '']; return badge(s[0], s[1]); };

  async function outMessage(root, id) {
    const base = '/api/v1/admin/mail/outbox/' + encodeURIComponent(id);
    const box = h('div'); add(root, h('p', null, h('a', { href: '#/mail/outbox' }, '← 返回')), box);
    async function load() {
      const x = (await api('GET', base)).message;
      add(clear(box), h('div', { class: 'card' }, h('h2', { class: 'mail-subject' }, x.subject || '（无主题）'),
        kv([['状态', outBadge(x)], ['收件人', x.to], ['发件人', x.from], ['类型', OUT_KIND[x.kind] || x.kind], ['创建', secTime(x.created_at)],
          ['完成', x.finished_at ? secTime(x.finished_at) : '—'], ['尝试次数', String(x.attempts)],
          ['下次尝试', x.status === 'queued' && x.next_attempt_at ? secTime(x.next_attempt_at) : '—'], ['接收服务器', x.remote || '—'], ['Message-ID', x.message_id || '—'],
          ['操作人', x.created_by || '系统'], ['最后错误', x.last_error ? h('span', { class: 'bad', id: 'out-error' }, x.last_error) : '—']]),
        h('div', { class: 'row' },
          h('button', { id: 'out-retry', disabled: x.sensitive || !['failed', 'cancelled'].includes(x.status), onclick: act(base + '/retry', undefined, '已重新排队', load) }, '重新发送'),
          h('button', { id: 'out-cancel', disabled: x.status !== 'queued', onclick: act(base + '/cancel', undefined, '已取消', load) }, '取消发送'))),
        h('div', { class: 'card' }, h('div', { class: 'mail-body' }, x.text || '（无内容）')));
    }
    await load();
  }

  async function mailStatus(root) {
    const d = await api('GET', '/api/v1/admin/mail/status');
    const o = d.overview, srv = o.server, out = o.outbox || {};
    setUnread(o.unread);
    add(root, h('div', { class: 'tiles' },
      tile('邮件服务', o.online ? '运行中' : '离线', o.last_seen ? '上次报告 ' + secAgo(o.last_seen) : '从未报告', o.online ? 'ok' : 'bad'),
      tile('未读', num(o.unread), '收件箱 ' + num(o.inbox) + '，已归档 ' + num(o.archived)),
      tile('待发送', num((out.queued || 0) + (out.sending || 0)), '', (out.queued || 0) > 20 ? 'warn' : ''),
      tile('对方已接受', num(out.sent || 0), '不代表已进入收件箱；失败 ' + num(out.failed || 0), out.failed ? 'warn' : ''),
      tile('退信', num(o.bounces), '', o.bounces ? 'warn' : ''),
      tile('24 小时验证码', num(o.codes_last_day)),
      tile('邮件占用', size(o.inbox_bytes))),
      h('div', { class: 'card' }, kv([
        ['发件地址', d.from || h('span', { class: 'bad' }, '门户未配置 mail.from')], ['客服地址', d.support || h('span', { class: 'warn' }, '未配置')],
        ['注册', !d.registration_open ? '关闭，仅管理员创建用户' : d.email_verification ? '开放，需要邮箱验证码' : h('span', { class: 'warn' }, '开放，不验证邮箱')],
        ['发信方式', srv ? srv.delivery : '—'], ['主机名', srv ? srv.hostname : '—'], ['DKIM 选择器', srv ? srv.dkim_selector : '—'],
        ['收信', srv && srv.smtp_listen ? srv.smtp_listen + '：' + (srv.mailboxes || []).join('、') : '未启用'],
        ['SMTP 计数', srv ? '连接 ' + srv.counters.connections + '，接收 ' + srv.counters.accepted + '，拒绝 ' + srv.counters.rejected : '—'],
        ['启动于', srv ? secTime(srv.started_at) : '—']])),
      srv && srv.dns ? [h('h2', null, 'DNS 检查', h('small', null, srv.dns_checked_at ? secAgo(srv.dns_checked_at) + '检查' : '')), h('div', { class: 'card' }, table([
        { title: '记录', cell: c => c.name }, { title: '结果', cell: c => c.ok ? badge('正常', 'ok') : badge('异常', 'bad') }, { title: '详情', cell: c => h('span', { class: 'mono wrap' }, c.detail) },
      ], srv.dns))] : null);
  }

  const go = ST.router({ overview, users, relays, jobs, nodes, audits, disputes, ledger, mail }, nav, main, 'overview');
  refreshUnread();
  await go();
})();
