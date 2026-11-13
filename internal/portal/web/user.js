// The user portal: balance, bills, jobs, API tokens, devices, network, account.
'use strict';
(async () => {
  await I18N.ready;
  const tr = I18N.t, parts = I18N.parts;
  const { api, h, clear, add, credit, signed, num, time, ago, peer, status, badge, toast, fail, modal, confirm, secret, table, kv, field, input, state, pageTitle, section, tile } = ST;
  const app = ST.$('app');

  await ST.loadInfo().catch(fail);
  while (!(await ST.loadMe())) await ST.renderLogin(app, { title: 'ShareToken' });
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
      { route: 'tokens', icon: 'key', label: 'API Token' }, { route: 'devices', icon: 'desktop', label: tr('设备') }, { route: 'pairing', icon: 'swap', label: tr('配对') }, { route: 'network', icon: 'globe', label: tr('网络') },
      { route: 'account', icon: 'user', label: tr('账户') }, { route: 'feedback', icon: 'chat', label: tr('反馈') },
    ],
    links: [{ href: '/docs/', icon: 'book', label: tr('文档') }, me().user.is_admin ? { href: '/admin/', icon: 'shield', label: tr('管理控制台') } : null].filter(Boolean),
    menu: [{ href: '#/account', icon: 'user', label: tr('账户') }],
  });
  // The balance moves while a page stays open; state.me is left alone so a lapsed session cannot pull it from under a page.
  window.addEventListener('hashchange', () => { api('GET', '/api/v1/me').then(showBalance).catch(() => {}); });

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

  const ledgerColumns = () => [
    { title: tr('时间'), cell: e => time(e.at) },
    { title: tr('摘要'), cell: e => e.memo },
    { title: tr('作业'), cell: e => e.job_id ? peer(e.job_id) : '' },
    { title: tr('金额'), num: true, cell: e => h('span', { class: e.amount_micros > 0 ? 'ok' : 'bad' }, signed(e.amount_micros)) },
  ];
  const ledgerTable = entries => table(ledgerColumns(), entries, tr('还没有账单记录'));
  async function ledger(m) {
    add(m, pageTitle(tr('账单'), h('small', null, tr('正数入账、负数出账，单位 credit'))));
    const list = ST.listView({ path: '/api/v1/ledger', key: 'entries', columns: ledgerColumns(), empty: tr('还没有账单记录') });
    add(m, list.el);
    await list.reload();
  }

  const jobsColumns = () => [
      { title: tr('时间'), cell: j => time(j.created_at) },
      { title: tr('角色'), cell: j => badge(j.role === 'buy' ? tr('买入') : tr('卖出'), j.role === 'buy' ? 'accent' : 'ok') },
      { title: tr('模型'), cell: j => j.model },
      { title: tr('状态'), cell: j => [status(j.status), j.dispute_outcome ? h('span', { class: 'muted' }, ' ' + tr('裁决：{outcome}', { outcome: j.dispute_outcome })) : null] },
      { title: tr('对方'), cell: j => peer(j.role === 'buy' ? j.node : j.buyer) },
      { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
      { title: tr('费用'), num: true, cell: j => credit(j.cost_micros) },
      { title: tr('收入'), num: true, cell: j => j.role === 'sell' && j.earned_micros ? h('span', { class: 'ok' }, '+' + credit(j.earned_micros)) : '' },
  ];
  const jobsTable = rows => table(jobsColumns(), rows, tr('还没有作业'));
  async function jobs(m) {
    const role = h('select', { id: 'jobs-role', onchange: () => list.reload() }, h('option', { value: '' }, tr('全部角色')), h('option', { value: 'buy' }, tr('买入')), h('option', { value: 'sell' }, tr('卖出')));
    const list = ST.listView({ path: '/api/v1/jobs', key: 'jobs', columns: jobsColumns(), empty: tr('还没有作业'), params: () => ({ role: role.value }), tools: role });
    add(m, pageTitle(tr('作业')), list.el);
    await list.reload();
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
    const mfa = u.mfa_enrolled_at ? [badge(tr('已启用'), 'ok'), ' ' + tr('绑定于 {time}', { time: time(u.mfa_enrolled_at) })] : badge(tr('未启用'), u.is_admin ? 'warn' : '');
    add(m, pageTitle(tr('账户')), h('div', { class: 'card' }, kv([[tr('邮箱'), u.username], [tr('用户 ID'), String(u.id)], [tr('注册时间'), time(u.created_at)], [tr('角色'), u.is_admin ? tr('管理员') : tr('普通用户')], [tr('两步验证'), h('span', { id: 'mfa-state' }, mfa)]]),
      h('p', { class: 'muted' }, tr('登录不需要密码：每次用发到这个邮箱的验证码登录。')),
      state.info && state.info.support_email ? h('p', { class: 'muted', id: 'support-email' }, parts('有问题或建议？发邮件到 {email}。', { email: h('a', { href: 'mailto:' + state.info.support_email }, state.info.support_email) })) : null));
    const box = h('div', { class: 'card', id: 'mfa-card' });
    add(m, section('lock', tr('两步验证')), box);
    const reload = async () => { await ST.loadMe(); clear(m); await account(m); };
    if (u.mfa_enrolled_at) {
      if (u.is_admin) {
        add(box, h('p', { class: 'muted' }, tr('管理员必须保留两步验证。更换手机时请让其他管理员在控制台重置，或在服务器上执行 portal mfa-reset。')));
        return;
      }
      const code = ST.codeInput('mfa-off-code'), err = h('div', { class: 'error', role: 'alert' });
      add(box, h('form', {
        class: 'form', id: 'mfa-off-form', onsubmit: async e => {
          e.preventDefault(); err.textContent = '';
          try { await api('POST', '/api/v1/account/mfa/disable', { code: code.value.trim() }); toast(tr('两步验证已关闭'), 'ok'); await reload(); }
          catch (ex) { err.textContent = ST.authError(ex); }
        }
      }, h('p', { class: 'muted' }, tr('登录时除了邮箱验证码，还要输入验证器 App 的验证码。关闭时请输入验证器 App 当前显示的验证码。')),
        field(tr('验证码'), code), err, h('div', null, h('button', { class: 'danger', type: 'submit' }, tr('关闭两步验证')))));
      return;
    }
    const start = h('button', { class: 'primary', type: 'button', id: 'mfa-on', onclick: async () => {
      let setup;
      try { setup = await api('POST', '/api/v1/account/mfa/setup', {}); } catch (ex) { fail(ex); return; }
      const code = ST.codeInput('mfa-on-code'), err = h('div', { class: 'error', role: 'alert' });
      add(clear(box), h('form', {
        class: 'form', id: 'mfa-on-form', onsubmit: async e => {
          e.preventDefault(); err.textContent = '';
          try { await api('POST', '/api/v1/account/mfa/enable', { challenge: setup.challenge, code: code.value.trim() }); toast(tr('两步验证已启用'), 'ok'); await reload(); }
          catch (ex) { err.textContent = ex.code === 'challenge_expired' ? tr('设置已超时，请重新开始') : ST.authError(ex); }
        }
      }, h('p', { class: 'muted' }, tr('用 Google Authenticator、Microsoft Authenticator、1Password 等扫描二维码，或手动输入密钥，然后填写 App 显示的 6 位验证码完成绑定。')),
        ST.mfaQR(setup), field(tr('验证码'), code), err,
        h('div', { class: 'row' }, h('button', { class: 'primary', type: 'submit' }, tr('完成绑定')), h('button', { type: 'button', onclick: reload }, tr('取消')))));
      code.focus();
    } }, tr('开启两步验证'));
    add(box, h('p', { class: 'muted' }, u.is_admin ? tr('管理员必须启用两步验证，下次登录时会要求绑定。') : tr('开启后，登录时除了邮箱验证码，还要输入验证器 App（如 Google Authenticator）的验证码。邮箱被盗时账户依然安全。')), h('div', null, start));
  }

  // Pairing: as a buyer, the sellers this account buys from; as a seller, its
  // codes, its buyers and private mode. The coordinator applies it.
  async function pairing(m) {
    const pairErr = ex => ({
      bad_code: tr('配对码不对，或者已经过期、被撤销或用完'),
      own_code: tr('这是你自己的配对码，请把它发给要配对的买家'),
      limit: tr('已达到数量上限，请先解除或撤销一些'),
      throttled: tr('尝试次数太多，请几分钟后再试'),
    })[ex.code] || ex.message;
    const bad = ex => toast(pairErr(ex), 'bad');
    add(m, pageTitle(tr('配对'), h('small', null, tr('卖家发出配对码，买家兑换后只向和它配对的卖家购买'))));
    const buying = h('div', { id: 'pair-buying' }), selling = h('div', { id: 'pair-selling' });
    add(m, buying, selling);

    const code = input({ type: 'text', id: 'pair-code', maxlength: 64, placeholder: 'XXXX-XXXX-XXXX-XXXX', class: 'mono', autocapitalize: 'characters', spellcheck: 'false', required: true });
    const joinForm = h('form', { class: 'form', id: 'pair-join-form', onsubmit: async e => {
      e.preventDefault();
      try {
        const seller = await api('POST', '/api/v1/pairing/redeem', { code: code.value.trim() });
        code.value = '';
        toast(tr('已和卖家 {name} 配对', { name: seller.username }));
        await load();
      } catch (ex) { bad(ex); }
    } }, h('div', { class: 'row' }, field(tr('配对码'), code)), h('div', null, h('button', { class: 'primary', type: 'submit', id: 'pair-join' }, tr('配对'))));

    const label = input({ type: 'text', id: 'pair-label', maxlength: 64, dir: 'auto' });
    const ttl = h('select', { id: 'pair-ttl' }, h('option', { value: '24' }, tr('1 天')), h('option', { value: '168' }, tr('7 天')), h('option', { value: '720' }, tr('30 天')));
    const uses = input({ type: 'number', id: 'pair-uses', min: 1, max: 1000, step: 1, placeholder: tr('不限') });
    const codeForm = h('form', { class: 'form', id: 'pair-code-form', onsubmit: async e => {
      e.preventDefault();
      try {
        const c = await api('POST', '/api/v1/pairing/codes', { label: label.value.trim(), ttl_hours: Number(ttl.value), max_uses: Number(uses.value) || 0 });
        label.value = uses.value = '';
        await load();
        await modal({ title: tr('配对码已生成'), cancel: false, okText: tr('完成'), body: h('div', { id: 'pair-new' },
          h('p', null, tr('把它发给要配对的买家。拿到它的人都能和你配对，请只发给你信任的人。')), secret(c.code),
          h('p', { class: 'muted' }, tr('到期时间：{time}', { time: time(c.expires_at) }))) });
      } catch (ex) { bad(ex); }
    } }, h('div', { class: 'row' }, field(tr('备注（配对的买家也会看到，可不填）'), label), field(tr('有效期'), ttl), field(tr('可配对的买家数'), uses)),
      h('div', null, h('button', { class: 'primary', type: 'submit', id: 'pair-new-code' }, tr('生成配对码'))));

    const who = u => [u.username, u.disabled ? [' ', badge(tr('已停用'), 'bad')] : null];
    const end = (cls, text, title, question, path) => h('button', { class: 'small danger ' + cls, onclick: async () => {
      if (!await confirm(title, question, text)) return;
      try { await api('DELETE', path); await load(); } catch (ex) { bad(ex); }
    } }, text);

    async function load() {
      const v = await api('GET', '/api/v1/pairing');
      add(clear(buying),
        section('swap', tr('配对的卖家'), h('small', null, tr('最多同时配对 {n} 个卖家', { n: num(v.max_sellers) }))),
        h('div', { class: 'card' }, joinForm),
        v.restricted ? h('div', { class: 'card warn', id: 'pair-restricted' }, tr('这个账户现在只向下面的卖家购买。全部解除后，恢复为向整个网络购买。')) : null,
        h('div', { class: 'card', id: 'pair-sellers' }, table([
          { title: tr('卖家'), cell: who },
          { title: tr('备注'), cell: u => u.label || '—' },
          { title: tr('配对时间'), cell: u => time(u.since) },
          { title: '', cell: u => end('pair-leave', tr('解除'), tr('解除配对'), tr('解除和卖家 {name} 的配对？之后这个账户不再向他购买，除非重新配对。', { name: u.username }), '/api/v1/pairing/sellers/' + u.id) },
        ], v.sellers, tr('还没有配对卖家，可以向网络上所有卖家购买'))));

      const priv = h('input', { type: 'checkbox', id: 'pair-private', checked: v.private, onchange: async () => {
        priv.disabled = true;
        try { await api('PUT', '/api/v1/pairing/private', { enabled: priv.checked }); toast(priv.checked ? tr('私有模式已开启') : tr('私有模式已关闭')); }
        catch (ex) { bad(ex); }
        await load();
      } });
      add(clear(selling),
        section('users', tr('我的买家'), h('small', null, tr('生成配对码发给你的买家'))),
        h('div', { class: 'card' }, h('label', { class: 'check' }, priv, h('span', null, h('b', null, tr('私有模式')), ' ',
          h('span', { class: 'muted' }, tr('只为和你配对的买家服务。开启后，其他买家看不到你的模型，也不能向你购买。'))))),
        h('div', { class: 'card' }, codeForm),
        h('div', { class: 'card', id: 'pair-codes' }, table([
          { title: tr('配对码'), cell: c => secret(c.code) },
          { title: tr('备注'), cell: c => c.label || '—' },
          { title: tr('已配对'), num: true, cell: c => c.max_uses ? num(c.used) + ' / ' + num(c.max_uses) : tr('{n}（不限）', { n: num(c.used) }) },
          { title: tr('到期时间'), cell: c => time(c.expires_at) },
          { title: '', cell: c => end('pair-revoke', tr('撤销'), tr('撤销配对码'), tr('撤销配对码 {code}？它不能再用来配对，已经配对的买家不受影响。', { code: c.code }), '/api/v1/pairing/codes/' + c.id) },
        ], v.codes, tr('没有有效的配对码'))),
        h('div', { class: 'card', id: 'pair-buyers' }, table([
          { title: tr('买家'), cell: who },
          { title: tr('备注'), cell: u => u.label || '—' },
          { title: tr('配对时间'), cell: u => time(u.since) },
          { title: '', cell: u => end('pair-remove', tr('移除'), tr('移除买家'), tr('移除买家 {name}？如果这是他唯一配对的卖家，他会恢复为向整个网络购买。', { name: u.username }), '/api/v1/pairing/buyers/' + u.id) },
        ], v.buyers, tr('还没有买家和你配对'))));
    }
    await load();
  }

  const FEEDBACK_KIND = { bug: () => tr('问题'), idea: () => tr('建议'), other: () => tr('其他') };

  async function feedback(m) {
    const kind = h('select', { id: 'feedback-kind' }, Object.entries(FEEDBACK_KIND).map(([k, label]) => h('option', { value: k }, label())));
    const text = h('textarea', { id: 'feedback-text', rows: 6, maxlength: 4000, dir: 'auto', class: 'wide' });
    const err = h('div', { class: 'error', id: 'feedback-error' });
    const list = ST.listView({ path: '/api/v1/feedback', key: 'feedback', dates: false, csv: false, empty: tr('还没有提交过反馈'), columns: [
      { title: tr('时间'), cell: f => time(f.created_at) },
      { title: tr('类型'), cell: f => (FEEDBACK_KIND[f.category] || (() => f.category))() },
      { title: tr('内容'), cell: f => h('div', { class: 'feedback-text', dir: 'auto' }, f.message) },
      { title: tr('状态'), cell: f => f.status === 'done' ? badge(tr('已处理'), 'ok') : badge(tr('已收到')) },
    ] });
    list.el.id = 'feedback-list';
    const form = h('form', {
      class: 'form', id: 'feedback-form', onsubmit: async e => {
        e.preventDefault(); err.textContent = '';
        if (!text.value.trim()) { err.textContent = tr('请填写反馈内容'); text.focus(); return; }
        try {
          await api('POST', '/api/v1/feedback', { category: kind.value, message: text.value, source: 'portal', lang: I18N.lang });
          text.value = '';
          toast(tr('反馈已提交，谢谢！'), 'ok');
          await list.reload();
        } catch (ex) { err.textContent = ex.code === 'throttled' ? tr('提交太频繁，请稍后再试') : ex.message; }
      }
    }, field(tr('类型'), kind), field(tr('内容'), text), err, h('div', null, h('button', { class: 'primary', type: 'submit', id: 'feedback-send' }, tr('提交反馈'))));
    add(m, pageTitle(tr('反馈')), h('div', { class: 'card' }, h('p', { class: 'muted' }, tr('告诉我们你遇到的问题或建议。请不要填写验证码、token 或 prompt 内容。我们可能会通过账户邮箱回复你。')), form),
      section('chat', tr('我的反馈')), list.el);
    await list.reload();
  }

  const go = ST.router({ home, ledger, jobs, tokens, devices, pairing, network, account, feedback }, nav, main, 'home');
  await go();
})();
