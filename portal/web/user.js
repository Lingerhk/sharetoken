// The user portal: balance, bills, jobs, API tokens, devices, network, account.
'use strict';
(async () => {
  const { api, h, clear, add, credit, signed, num, time, ago, peer, status, badge, toast, fail, modal, confirm, secret, table, kv, field, input, state } = ST;
  const app = ST.$('app');

  await ST.loadInfo().catch(fail);
  while (!(await ST.loadMe())) {
    await ST.renderLogin(app, { title: 'sharetoken', allowRegister: state.info && state.info.registration_open });
  }
  const me = () => state.me;

  // ---- frame ----
  const nav = h('nav', { class: 'sidenav' },
    h('a', { href: '#/home' }, '总览'), h('a', { href: '#/ledger' }, '账单'), h('a', { href: '#/jobs' }, '作业'),
    h('a', { href: '#/tokens' }, 'API Token'), h('a', { href: '#/devices' }, '设备'), h('a', { href: '#/network' }, '网络'),
    h('a', { href: '#/account' }, '账户'));
  const main = h('main');
  add(clear(app), 
    h('header', { class: 'topbar' }, h('span', { class: 'brand' }, 'sharetoken', h('small', null, '用户门户')), h('span', { class: 'spacer' }),
      me().user.is_admin ? h('a', { href: '/admin/' }, '管理控制台') : null,
      h('span', { class: 'who' }, me().user.username), h('button', { class: 'small', onclick: ST.logout }, '退出')),
    h('div', { class: 'layout' }, nav, main));

  const moreButton = (fn) => h('div', { class: 'row end' }, h('button', { onclick: fn }, '加载更多'));

  // ---- pages ----
  async function home(m) {
    const d = await ST.loadMe();
    const b = d.balance, u = d.usage;
    add(m, h('h1', null, '总览'),
      h('div', { class: 'tiles' },
        tile('可用余额', credit(b.available_micros) + ' credit', '账户 ' + b.account),
        tile('冻结中', credit(b.held_micros) + ' credit', '进行中作业的预留额'),
        tile('买入', num(u.bought.jobs) + ' 单', '花费 ' + credit(u.bought.micros) + '，' + num(u.bought.prompt_tokens + u.bought.completion_tokens) + ' tokens' + (u.bought.pending_jobs ? '，' + u.bought.pending_jobs + ' 单进行中' : '')),
        tile('卖出', num(u.sold.jobs) + ' 单', '净收入 ' + credit(u.sold.micros) + '，' + num(u.sold.prompt_tokens + u.sold.completion_tokens) + ' tokens' + (u.sold.pending_jobs ? '，' + u.sold.pending_jobs + ' 单进行中' : '')),
        tile('设备', num(d.devices.length) + ' 台', d.devices.map(x => x.name).join('、') || '尚未绑定设备')));
    const [jobs, led] = await Promise.all([api('GET', '/api/v1/jobs?limit=8'), api('GET', '/api/v1/ledger?limit=8')]);
    add(m, h('h2', null, '最近作业', h('small', null, h('a', { href: '#/jobs' }, '全部'))), h('div', { class: 'card' }, jobsTable(jobs.jobs)),
      h('h2', null, '最近账单', h('small', null, h('a', { href: '#/ledger' }, '全部'))), h('div', { class: 'card' }, ledgerTable(led.entries)));
    if (d.devices.length === 0) add(m, h('div', { class: 'card muted' }, '还没有绑定设备。在你的机器上运行 ', h('code', null, 'sharetoken login -portal ' + location.origin + ' -user ' + me().user.username), ' 即可把该设备绑定到此账户。'));
  }
  function tile(label, value, sub) { return h('div', { class: 'tile' }, h('div', { class: 'label' }, label), h('div', { class: 'value' }, value), sub ? h('div', { class: 'sub' }, sub) : null); }

  function ledgerTable(entries) {
    return table([
      { title: '时间', cell: e => time(e.at) },
      { title: '摘要', cell: e => e.memo },
      { title: '作业', cell: e => e.job_id ? peer(e.job_id) : '' },
      { title: '金额', num: true, cell: e => h('span', { class: e.amount_micros > 0 ? 'ok' : 'bad' }, signed(e.amount_micros)) },
    ], entries, '还没有账单记录');
  }
  async function ledger(m) {
    add(m, h('h1', null, '账单', h('small', null, '正数入账、负数出账，单位 credit')));
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
      { title: '时间', cell: j => time(j.created_at) },
      { title: '角色', cell: j => badge(j.role === 'buy' ? '买入' : '卖出', j.role === 'buy' ? 'accent' : 'ok') },
      { title: '模型', cell: j => j.model },
      { title: '状态', cell: j => [status(j.status), j.dispute_outcome ? h('span', { class: 'muted' }, ' 裁决：' + j.dispute_outcome) : null] },
      { title: '对方', cell: j => peer(j.role === 'buy' ? j.node : j.buyer) },
      { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
      { title: '费用', num: true, cell: j => credit(j.cost_micros) },
      { title: '收入', num: true, cell: j => j.role === 'sell' && j.earned_micros ? h('span', { class: 'ok' }, '+' + credit(j.earned_micros)) : '' },
    ], jobs, '还没有作业');
  }
  async function jobs(m) {
    const role = h('select', { onchange: () => { all = []; load(); } }, h('option', { value: '' }, '全部'), h('option', { value: 'buy' }, '买入'), h('option', { value: 'sell' }, '卖出'));
    add(m, h('h1', null, '作业'), h('div', { class: 'toolbar' }, h('span', { class: 'muted' }, '角色'), role));
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
    add(m, h('h1', null, 'API Token', h('small', null, '用于 sharetoken 客户端和脚本；浏览器会话也在这里')));
    const list = h('div', { class: 'card' });
    const name = input({ type: 'text', placeholder: '例如 laptop、ci', maxlength: 64, required: true });
    const ttl = h('select', null, h('option', { value: '0' }, '永不过期'), h('option', { value: '24' }, '1 天'), h('option', { value: '168' }, '7 天'), h('option', { value: '720' }, '30 天'), h('option', { value: '2160' }, '90 天'), h('option', { value: '8760' }, '1 年'));
    const form = h('form', {
      class: 'form', onsubmit: async e => {
        e.preventDefault();
        try {
          const r = await api('POST', '/api/v1/tokens', { name: name.value.trim(), ttl_hours: Number(ttl.value) });
          name.value = '';
          await modal({ title: '新 token 已创建', cancel: false, okText: '我已保存', body: h('div', null, h('p', null, '它只显示这一次，请立即复制保存：'), secret(r.token),
            h('p', { class: 'muted' }, '用法：sharetoken 客户端 config.json 的 token 字段，或 HTTP 头 Authorization: Bearer …')) });
          await load();
        } catch (ex) { fail(ex); }
      }
    }, h('div', { class: 'row' }, field('名称', name), field('有效期', ttl)), h('div', null, h('button', { class: 'primary', type: 'submit' }, '创建 token')));
    add(m, h('h2', null, '新建'), h('div', { class: 'card' }, form), h('h2', null, '现有 token'), list);
    async function load() {
      const r = await api('GET', '/api/v1/tokens');
      add(clear(list), table([
        { title: '名称', cell: t => [t.name, t.id === r.current ? [' ', badge('当前会话', 'accent')] : null] },
        { title: '前缀', cell: t => h('code', null, t.prefix + '…') },
        { title: '创建', cell: t => time(t.created_at) },
        { title: '最近使用', cell: t => ago(t.last_used_at) },
        { title: '过期', cell: t => t.expires_at ? time(t.expires_at) : '永不' },
        { title: '', cell: t => h('button', { class: 'small danger', onclick: async () => {
          if (!await confirm('吊销 token', '吊销「' + t.name + '」后，使用它的客户端会立即掉线' + (t.id === r.current ? '，包括当前浏览器会话' : '') + '。', '吊销')) return;
          try { await api('DELETE', '/api/v1/tokens/' + t.id); toast('已吊销'); if (t.id === r.current) location.reload(); else await load(); } catch (ex) { fail(ex); }
        } }, '吊销') },
      ], r.tokens));
    }
    await load();
  }

  async function devices(m) {
    add(m, h('h1', null, '设备', h('small', null, '绑定到此账户的 peer；它们花你的 credit、把收入记到你名下')));
    const list = h('div', { class: 'card' }); add(m, list,
      h('div', { class: 'card muted' }, '绑定新设备需要设备私钥签名，所以要在那台机器上运行 ', h('code', null, 'sharetoken login -portal ' + location.origin + ' -user ' + me().user.username), '。解绑后协调器会立即拒绝该设备。'));
    async function load() {
      const r = await api('GET', '/api/v1/devices');
      add(clear(list), table([
        { title: '名称', cell: d => d.name },
        { title: 'Peer ID', cell: d => h('code', { class: 'mono' }, d.peer) },
        { title: '绑定时间', cell: d => time(d.created_at) },
        { title: '最近活动', cell: d => ago(d.last_seen_at) },
        { title: '', cell: d => h('span', { class: 'row' },
          h('button', { class: 'small', onclick: async () => {
            const nm = input({ type: 'text', value: d.name, maxlength: 64 });
            if (!await modal({ title: '重命名设备', body: field('名称', nm) })) return;
            try { await api('PATCH', '/api/v1/devices/' + encodeURIComponent(d.peer), { name: nm.value.trim() }); toast('已重命名'); await load(); } catch (ex) { fail(ex); }
          } }, '重命名'),
          h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('解绑设备', '解绑「' + d.name + '」后它不能再用你的账户买卖；重新绑定需在该设备上再次登录。', '解绑')) return;
            try { await api('DELETE', '/api/v1/devices/' + encodeURIComponent(d.peer)); toast('已解绑'); await load(); } catch (ex) { fail(ex); }
          } }, '解绑')) },
      ], r.devices, '还没有绑定设备'));
    }
    await load();
  }

  async function network(m) {
    add(m, h('h1', null, '网络'));
    let n;
    try { n = await api('GET', '/api/v1/network'); }
    catch (e) { add(m, h('div', { class: 'card warn' }, e.code === 'not_ready' ? '协调器尚未启动，暂无网络信息。' : e.message)); return; }
    add(m, h('div', { class: 'card' }, kv([['协调器', h('code', { class: 'mono' }, n.coordinator)], ['中继凭证公钥', n.pass_key ? h('code', { class: 'mono' }, n.pass_key) : h('span', { class: 'muted' }, '未启用（开放中继）')]])),
      h('h2', null, '中继', h('small', null, '客户端按自己测得的延迟自动选择；「延迟」是协调器到该中继的往返时间，每 30 秒更新；「可达」表示协调器在该中继上有预约')),
      h('div', { class: 'card' }, table([
        { title: '名称', cell: r => r.name || '—' }, { title: '地区', cell: r => r.region || '—' },
        { title: '地址', cell: r => h('div', null, r.addr.split(',').map(a => h('div', null, h('code', { class: 'mono' }, a)))) },
        { title: '延迟', num: true, cell: r => ST.rtt(r.rtt_ms) },
        { title: '协调器', cell: r => badge(r.ok ? '可达' : '不可达', r.ok ? 'ok' : 'bad') },
      ], n.relays, '还没有登记中继')));
  }

  async function account(m) {
    const u = me().user;
    const mfa = u.mfa_enrolled_at ? [badge('已启用', 'ok'), ' 绑定于 ' + time(u.mfa_enrolled_at)] : (u.is_admin ? badge('未启用', 'warn') : h('span', { class: 'muted' }, '未启用（仅管理员需要）'));
    add(m, h('h1', null, '账户'), h('div', { class: 'card' }, kv([['用户名', u.username], ['用户 ID', String(u.id)], ['注册时间', time(u.created_at)], ['角色', u.is_admin ? '管理员' : '普通用户'], ['两步验证', h('span', null, mfa)]]),
      u.is_admin ? h('p', { class: 'muted' }, '管理员登录必须通过验证器 App 的两步验证。更换手机时请让其他管理员在控制台重置，或在服务器上执行 portal mfa-reset。') : null));
    const oldP = input({ type: 'password', autocomplete: 'current-password', required: true });
    const newP = input({ type: 'password', autocomplete: 'new-password', minlength: 8, maxlength: 256, required: true, placeholder: '8-256 位' });
    const newP2 = input({ type: 'password', autocomplete: 'new-password', required: true });
    const revoke = h('input', { type: 'checkbox' });
    const err = h('div', { class: 'error' });
    const form = h('form', {
      class: 'form', onsubmit: async e => {
        e.preventDefault(); err.textContent = '';
        if (newP.value !== newP2.value) { err.textContent = '两次输入的新密码不一致'; return; }
        try {
          const r = await api('POST', '/api/v1/password', { old_password: oldP.value, new_password: newP.value, revoke_others: revoke.checked });
          oldP.value = newP.value = newP2.value = '';
          toast('密码已修改' + (r.tokens_revoked ? '，已吊销 ' + r.tokens_revoked + ' 个其他会话' : ''));
        } catch (ex) { err.textContent = ex.code === 'bad_credentials' ? '当前密码错误' : ex.message; }
      }
    }, field('当前密码', oldP), field('新密码', newP), field('重复新密码', newP2),
      h('label', { class: 'check' }, revoke, '同时吊销其他所有 token 和会话（本浏览器除外）'), err, h('div', null, h('button', { class: 'primary', type: 'submit' }, '修改密码')));
    add(m, h('h2', null, '修改密码'), h('div', { class: 'card' }, form));
  }

  const go = ST.router({ home, ledger, jobs, tokens, devices, network, account }, nav, main, 'home');
  await go();
})();
