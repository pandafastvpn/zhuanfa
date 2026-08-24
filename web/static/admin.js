// ---------------- 管理后台逻辑 ----------------

let USERS = [];
let GROUPS = [];
let dashboardTimer = null;
let selectedInterface = '';

async function initAdmin() {
  const me = await onloadGuard();
  if (!me || me.user.role !== 'admin') return;

  document.getElementById('logoutBtn').addEventListener('click', async () => {
    await api('POST', '/api/logout');
    location.href = '/login';
  });

  // 导航切换
  const navs = document.querySelectorAll('.sidebar nav a');
  navs.forEach(n => n.addEventListener('click', () => {
    navs.forEach(x => x.classList.remove('active'));
    n.classList.add('active');
    document.querySelectorAll('.content > section').forEach(s => s.classList.add('hidden'));
    document.getElementById('page-' + n.dataset.page).classList.remove('hidden');
    if (n.dataset.page !== 'dashboard') stopDashboardMonitor();
    loadPage(n.dataset.page);
  }));

  await loadPage('dashboard');
}

async function loadPage(page) {
  try {
    if (page === 'dashboard') await loadDashboard();
    else if (page === 'users') await loadUsers();
    else if (page === 'groups') await loadGroups();
    else if (page === 'ports') await loadPorts();
    else if (page === 'records') await loadRecords();
    else if (page === 'settings') await loadSettings();
  } catch (err) {
    toast(err.message, 'err');
  }
}

// ---------------- 仪表盘 ----------------

async function loadDashboard() {
  const d = await api('GET', '/api/admin/stats');
  const today = d.today || {};
  const cards = [
    { label: '用户数', value: d.users, sub: '用户组 ' + d.groups, cls: '' },
    { label: '端口规则', value: d.ports, sub: '全量端口规则', cls: '' },
    { label: '今日流量', value: fmtBytes(today.bytes), sub: '连接 ' + (today.connects || 0) + ' 次', cls: 'green' },
    { label: '今日拒绝', value: today.denies || 0, sub: '非白名单协议/配额', cls: 'red' },
    { label: '累计流量', value: fmtBytes(d.total_bytes), sub: '全组合计', cls: 'orange' },
    { label: 'realm 状态', value: d.realm.running ? '运行中' : '未运行', sub: d.realm.mode + ' 模式', cls: d.realm.running ? 'green' : 'red' },
  ];
  document.getElementById('dashCards').innerHTML = cards.map(c =>
    '<div class="stat-card"><div class="label">' + c.label + '</div><div class="value ' + c.cls + '">' +
    c.value + '</div><div class="sub">' + c.sub + '</div></div>').join('');

  barChart(document.getElementById('dashChart'), d.last7 || []);
  startDashboardMonitor();

  const tbody = document.querySelector('#dashRecent tbody');
  if (!(d.recent || []).length) {
    tbody.innerHTML = '<tr><td colspan="8" class="empty">暂无记录</td></tr>';
    return;
  }
  tbody.innerHTML = (d.recent || []).map(r =>
    '<tr><td>' + fmtTime(r.time) + '</td><td>' + esc(r.username || '-') + '</td><td>' + r.port +
    '</td><td>' + (r.proto === 'udp' ? 'UDP' : 'TCP') + '</td><td>' + protoBadge(r.protocol) +
    '</td><td class="mono">' + esc(r.source) + '</td><td>' + actionBadge(r.action) +
    '</td><td class="mono">' + esc(r.target || '-') + '</td></tr>').join('');
}

function fmtDuration(seconds) {
  seconds = Math.max(0, Math.floor(Number(seconds || 0)));
  const d = Math.floor(seconds / 86400); seconds %= 86400;
  const h = Math.floor(seconds / 3600); seconds %= 3600;
  const m = Math.floor(seconds / 60);
  return (d ? d + ' 天 ' : '') + h + ' 小时 ' + m + ' 分钟';
}

function pct(used, total) { return total ? Math.min(100, Math.round(used * 100 / total)) : 0; }

function monitorRow(label, used, total, suffix) {
  const p = pct(used, total);
  return '<div class="monitor-row"><div><b>' + label + '</b><span>' + esc(fmtBytes(used)) + ' / ' + esc(fmtBytes(total)) + (suffix || '') + '</span></div><strong>' + p + '%</strong><div class="progress"><div style="width:' + p + '%"></div></div></div>';
}

async function refreshDashboardMonitor() {
  const d = await api('GET', '/api/admin/system-status' + (selectedInterface ? '?interface=' + encodeURIComponent(selectedInterface) : ''));
  const select = document.getElementById('netInterface');
  if (!select) return;
  const names = Object.keys(d.interfaces || {}).sort();
  if (select.options.length <= 1) {
    names.forEach(name => { const o = document.createElement('option'); o.value = name; o.textContent = name; select.appendChild(o); });
    select.value = selectedInterface;
    select.onchange = () => { selectedInterface = select.value; refreshDashboardMonitor().catch(e => toast(e.message, 'err')); };
  }
  document.getElementById('netUpload').textContent = fmtBytes(d.tx_bps) + '/s';
  document.getElementById('netDownload').textContent = fmtBytes(d.rx_bps) + '/s';
  document.getElementById('systemStatus').innerHTML =
    '<div class="system-cpu"><b>CPU</b><strong>' + Number(d.cpu_percent || 0).toFixed(1) + '%</strong><span>' + d.cpu_cores + ' 核</span></div>' +
    monitorRow('内存', d.mem_used, d.mem_total) +
    monitorRow('交换分区', d.swap_used, d.swap_total) +
    monitorRow('存储', d.disk_used, d.disk_total) +
    '<div class="system-meta"><span>TCP 连接 <b>' + d.tcp_connections + '</b></span><span>UDP 连接 <b>' + d.udp_connections + '</b></span><span>服务器运行 <b>' + fmtDuration(d.uptime_seconds) + '</b></span><span>面板运行 <b>' + fmtDuration(d.panel_uptime_seconds) + '</b></span></div>';
}

function startDashboardMonitor() {
  if (dashboardTimer) return;
  refreshDashboardMonitor().catch(e => toast(e.message, 'err'));
  dashboardTimer = setInterval(() => refreshDashboardMonitor().catch(() => {}), 2000);
}
function stopDashboardMonitor() { if (dashboardTimer) { clearInterval(dashboardTimer); dashboardTimer = null; } }

// ---------------- 用户管理 ----------------

async function loadUsers() {
  USERS = await api('GET', '/api/admin/users');
  GROUPS = await api('GET', '/api/admin/groups');
  const gmap = {};
  GROUPS.forEach(g => gmap[g.id] = g.name);
  const tbody = document.querySelector('#usersTable tbody');
  tbody.innerHTML = USERS.map(u => {
    const st = u.enabled ? '<span class="badge green">正常</span>' : '<span class="badge red">禁用</span>';
    const ranges = (u.ranges || []).map(r => r.start + '-' + r.end).join('、') || '<span class="muted">无</span>';
    const expire = u.expire_at ? fmtDate(u.expire_at) : '永久';
    return '<tr><td>' + u.id + '</td><td>' + esc(u.username) + '</td><td>' +
      (u.role === 'admin' ? '<span class="badge orange">管理员</span>' : '<span class="badge blue">用户</span>') +
      '</td><td>' + esc(gmap[u.group_id] || '-') + '</td><td>' + st + '</td><td class="mono">' + ranges +
      '</td><td>' + fmtBytes(u.total_bytes) + '</td><td>' + expire + '</td><td>' +
      '<button class="btn sm secondary" data-edit="' + u.id + '">编辑</button> ' +
      '<button class="btn sm secondary" data-reset="' + u.id + '">重置流量</button> ' +
      '<button class="btn sm secondary" data-pwd="' + u.id + '">改密</button> '
      '<button class="btn sm danger" data-del="' + u.id + '">删除</button></td></tr>';
  }).join('') || '<tr><td colspan="9" class="empty">暂无用户</td></tr>';

  document.getElementById('addUserBtn').onclick = () => userModal(null);
  tbody.querySelectorAll('[data-edit]').forEach(b => b.onclick = () => userModal(USERS.find(u => u.id == b.dataset.edit)));
  tbody.querySelectorAll('[data-reset]').forEach(b => b.onclick = () => {
    const u = USERS.find(x => x.id == b.dataset.reset);
    confirmModal('重置用户流量', '确定清零用户「' + u.username + '」及其所有端口的流量？', async () => {
      await api('POST', '/api/admin/users/' + u.id + '/reset');
      toast('用户流量已重置');
      loadUsers();
    }, '确认重置');
  });
  tbody.querySelectorAll('[data-pwd]').forEach(b => b.onclick = () => pwdModal(USERS.find(u => u.id == b.dataset.pwd)));
  tbody.querySelectorAll('[data-del]').forEach(b => b.onclick = () => {
    const u = USERS.find(x => x.id == b.dataset.del);
    confirmModal('删除用户', '确定删除用户「' + u.username + '」？其所有端口规则将一并删除。', async () => {
      await api('DELETE', '/api/admin/users/' + u.id);
      toast('已删除');
      loadUsers();
    });
  });
}

function rangeInputsHTML(ranges) {
  const rows = (ranges && ranges.length ? ranges : [{ start: 1000, end: 2000 }]).map((r, i) =>
    '<div class="flex" data-rangerow>' +
    '<input type="number" class="rg-start" value="' + r.start + '" placeholder="起始端口" style="flex:1">' +
    '<input type="number" class="rg-end" value="' + r.end + '" placeholder="结束端口" style="flex:1">' +
    (i > 0 ? '<button type="button" class="btn sm danger" data-rm>删除</button>' : '') +
    '</div>').join('');
  return '<div id="rangeRows">' + rows + '</div>' +
    '<button type="button" class="btn sm secondary" id="addRangeBtn" style="margin-top:6px">+ 添加端口段</button>';
}

function userModal(u) {
  const isNew = !u;
  u = u || { group_id: GROUPS.length ? GROUPS[0].id : 0, enabled: true, role: 'user', ranges: [] };
  const groupOpts = GROUPS.map(g => '<option value="' + g.id + '"' + (g.id == u.group_id ? ' selected' : '') + '>' + esc(g.name) + '</option>').join('');
  const mask = openModal(isNew ? '添加用户' : '编辑用户「' + u.username + '」',
    '<div class="form-row"><label>用户名</label><input type="text" id="uName" value="' + esc(u.username || '') + '"></div>' +
    '<div class="form-row"><label>' + (isNew ? '密码' : '新密码（留空则不修改）') + '</label><input type="password" id="uPwd"></div>' +
    '<div class="form-row"><label>角色</label><select id="uRole">' +
    '<option value="user"' + (u.role !== 'admin' ? ' selected' : '') + '>普通用户</option>' +
    '<option value="admin"' + (u.role === 'admin' ? ' selected' : '') + '>管理员</option></select></div>' +
    '<div class="form-row"><label>用户组</label><select id="uGroup">' + groupOpts + '</select></div>' +
    '<div class="form-row"><label>状态</label><select id="uEnabled">' +
    '<option value="true"' + (u.enabled ? ' selected' : '') + '>启用</option>' +
    '<option value="false"' + (!u.enabled ? ' selected' : '') + '>禁用</option></select></div>' +
    '<div class="form-row"><label>过期时间（留空为永久，格式 2026-12-31）</label><input type="text" id="uExpire" placeholder="2026-12-31" value="' +
    (u.expire_at ? fmtDate(u.expire_at) : '') + '"></div>' +
    '<div class="form-row"><label>分配的端口段（用户可使用这些端口建立转发规则）</label>' + rangeInputsHTML(u.ranges) + '</div>' +
    '<div class="form-row"><label>备注</label><input type="text" id="uNote" value="' + esc(u.note || '') + '"></div>',
    async (body) => {
      const ranges = [];
      body.querySelectorAll('[data-rangerow]').forEach(r => {
        const s = parseInt(r.querySelector('.rg-start').value);
        const e = parseInt(r.querySelector('.rg-end').value);
        if (!isNaN(s) && !isNaN(e) && s > 0 && e >= s) ranges.push({ start: s, end: e });
      });
      const expireStr = body.querySelector('#uExpire').value.trim();
      let expireAt = 0;
      if (expireStr) {
        const t = Date.parse(expireStr);
        if (isNaN(t)) throw new Error('过期时间格式错误，应为 2026-12-31');
        expireAt = Math.floor(t / 1000);
      }
      const payload = {
        username: body.querySelector('#uName').value.trim(),
        password: body.querySelector('#uPwd').value,
        role: body.querySelector('#uRole').value,
        group_id: parseInt(body.querySelector('#uGroup').value),
        enabled: body.querySelector('#uEnabled').value === 'true',
        expire_at: expireAt,
        ranges,
        note: body.querySelector('#uNote').value.trim(),
      };
      if (isNew) {
        await api('POST', '/api/admin/users', payload);
        toast('用户已创建');
      } else {
        await api('PUT', '/api/admin/users/' + u.id, payload);
        toast('已保存');
      }
      loadUsers();
    }, isNew ? '创建' : '保存');

  const mb = mask.querySelector('.modal-body');
  mb.querySelector('#addRangeBtn').onclick = () => {
    const rows = mb.querySelector('#rangeRows');
    const div = document.createElement('div');
    div.className = 'flex';
    div.setAttribute('data-rangerow', '');
    div.style.marginTop = '6px';
    div.innerHTML = '<input type="number" class="rg-start" placeholder="起始端口" style="flex:1">' +
      '<input type="number" class="rg-end" placeholder="结束端口" style="flex:1">' +
      '<button type="button" class="btn sm danger" data-rm>删除</button>';
    div.querySelector('[data-rm]').onclick = () => div.remove();
    rows.appendChild(div);
  };
  mb.querySelectorAll('[data-rm]').forEach(b => b.onclick = () => b.closest('[data-rangerow]').remove());
}

function pwdModal(u) {
  openModal('修改密码 - ' + u.username,
    '<div class="form-row"><label>新密码（至少 6 位）</label><input type="password" id="pwdNew"></div>' +
    '<div class="form-row"><label>确认新密码</label><input type="password" id="pwdNew2"></div>',
    async (body) => {
      const p = body.querySelector('#pwdNew').value;
      const p2 = body.querySelector('#pwdNew2').value;
      if (p.length < 6) throw new Error('密码至少 6 位');
      if (p !== p2) throw new Error('两次密码不一致');
      await api('POST', '/api/admin/users/' + u.id + '/password', { password: p });
      toast('密码已修改');
    }, '修改密码');
}

// ---------------- 用户组 ----------------

async function loadGroups() {
  GROUPS = await api('GET', '/api/admin/groups');
  const users = await api('GET', '/api/admin/users');
  const tbody = document.querySelector('#groupsTable tbody');
  tbody.innerHTML = GROUPS.map(g => {
    const cnt = users.filter(u => u.group_id === g.id).length;
    const st = g.enabled ? '<span class="badge green">启用</span>' : '<span class="badge red">停用</span>';
    const used = g.total_bytes;
    const quota = g.quota_gb;
    const over = quota > 0 && used >= quota * 1024 * 1024 * 1024;
    const pct = quota > 0 ? Math.min(100, Math.round(used / (quota * 1024 * 1024 * 1024) * 100)) : 0;
    return '<tr><td>' + g.id + '</td><td>' + esc(g.name) + '</td><td>' + fmtMbps(g.bandwidth_mbps) +
      '</td><td>' + fmtQuota(g.quota_gb) + '</td><td>' + fmtBytes(used) + ' ' +
      (quota > 0 ? '<div class="progress ' + (over ? 'over' : '') + '"><div style="width:' + pct + '%"></div></div>' : '') +
      '</td><td>' + cnt + '</td><td>' + st + '</td><td>' +
      '<button class="btn sm secondary" data-edit="' + g.id + '">编辑</button> ' +
      '<button class="btn sm secondary" data-reset="' + g.id + '">重置流量</button> ' +
      '<button class="btn sm danger" data-del="' + g.id + '">删除</button></td></tr>';
  }).join('') || '<tr><td colspan="8" class="empty">暂无用户组</td></tr>';

  document.getElementById('addGroupBtn').onclick = () => groupModal(null);
  tbody.querySelectorAll('[data-edit]').forEach(b => b.onclick = () => groupModal(GROUPS.find(g => g.id == b.dataset.edit)));
  tbody.querySelectorAll('[data-reset]').forEach(b => b.onclick = () => {
    confirmModal('重置流量', '确定清零该组的全部流量统计？', async () => {
      await api('POST', '/api/admin/groups/' + b.dataset.reset + '/reset');
      toast('已重置');
      loadGroups();
    }, '确认重置');
  });
  tbody.querySelectorAll('[data-del]').forEach(b => b.onclick = () => {
    const g = GROUPS.find(x => x.id == b.dataset.del);
    confirmModal('删除用户组', '确定删除用户组「' + g.name + '」？', async () => {
      await api('DELETE', '/api/admin/groups/' + g.id);
      toast('已删除');
      loadGroups();
    });
  });
}

function groupModal(g) {
  g = g || { bandwidth_mbps: 0, quota_gb: 0, enabled: true };
  openModal(g.id ? '编辑用户组' : '添加用户组',
    '<div class="form-row"><label>组名</label><input type="text" id="gName" value="' + esc(g.name || '') + '"></div>' +
    '<div class="form-row"><label>每用户宽带峰值（Mbps，0 表示不限）</label><input type="number" id="gBw" value="' + (g.bandwidth_mbps || 0) + '" min="0"></div>' +
    '<div class="form-row"><label>总流量配额（GB，0 表示不限）</label><input type="number" id="gQuota" value="' + (g.quota_gb || 0) + '" min="0"></div>' +
    '<div class="form-row"><label>状态</label><select id="gEnabled">' +
    '<option value="true"' + (g.enabled ? ' selected' : '') + '>启用</option>' +
    '<option value="false"' + (!g.enabled ? ' selected' : '') + '>停用</option></select></div>',
    async (body) => {
      const payload = {
        name: body.querySelector('#gName').value.trim(),
        bandwidth_mbps: parseInt(body.querySelector('#gBw').value) || 0,
        quota_gb: parseInt(body.querySelector('#gQuota').value) || 0,
        enabled: body.querySelector('#gEnabled').value === 'true',
      };
      if (!payload.name) throw new Error('组名不能为空');
      if (g.id) {
        await api('PUT', '/api/admin/groups/' + g.id, payload);
        toast('已保存');
      } else {
        await api('POST', '/api/admin/groups', payload);
        toast('已创建');
      }
      loadGroups();
    }, g.id ? '保存' : '创建');
}

// ---------------- 端口管理 ----------------

async function loadPorts() {
  const d = await api('GET', '/api/admin/ports');
  USERS = d.users;
  const umap = {};
  USERS.forEach(u => umap[u.id] = u.username);
  const filter = document.getElementById('portUserFilter');
  const prev = filter.value;
  filter.innerHTML = '<option value="">全部用户</option>' +
    USERS.map(u => '<option value="' + u.id + '">' + esc(u.username) + '</option>').join('');
  filter.value = prev;
  filter.onchange = renderPorts;

  window.__portsData = d.ports;
  renderPorts();
}

function renderPorts() {
  const filter = document.getElementById('portUserFilter');
  const uid = filter.value ? parseInt(filter.value) : 0;
  const umap = {};
  USERS.forEach(u => umap[u.id] = u.username);
  const list = window.__portsData.filter(p => !uid || p.user_id === uid);
  const tbody = document.querySelector('#portsTable tbody');
  tbody.innerHTML = list.map(p =>
    '<tr><td class="mono" style="font-weight:600">' + p.port + '</td><td>' + esc(umap[p.user_id] || '-') +
    '</td><td>' + typeBadge(p) + '</td><td>' + allowedLabel(p.allowed) +
    '</td><td class="mono">' + esc(p.target) + '</td><td>' +
    (p.enabled ? '<span class="badge green">启用</span>' : '<span class="badge gray">停用</span>') +
    '</td><td>' + fmtBytes(p.total_bytes) + '</td><td>' + p.total_conns + '</td><td>' + fmtDate(p.created_at) +
    '</td><td><button class="btn sm secondary" data-edit="' + p.id + '">编辑</button> ' +
    '<button class="btn sm danger" data-del="' + p.id + '">删除</button></td></tr>').join('') ||
    '<tr><td colspan="10" class="empty">暂无端口规则</td></tr>';

  document.getElementById('addPortBtn').onclick = () => portModal(null);
  tbody.querySelectorAll('[data-edit]').forEach(b => b.onclick = () => portModal(window.__portsData.find(p => p.id == b.dataset.edit)));
  tbody.querySelectorAll('[data-del]').forEach(b => b.onclick = () => {
    const p = window.__portsData.find(x => x.id == b.dataset.del);
    confirmModal('删除端口规则', '确定删除端口 ' + p.port + ' 的转发规则？', async () => {
      await api('DELETE', '/api/admin/ports/' + p.id);
      toast('已删除');
      loadPorts();
    });
  });
}

function portModal(p) {
  p = p || { user_id: USERS.length ? USERS[0].id : 0, tcp: true, udp: false, allowed: 'auto', enabled: true };
  const uOpts = USERS.map(u => '<option value="' + u.id + '"' + (u.id == p.user_id ? ' selected' : '') + '>' +
    esc(u.username) + '</option>').join('');
  openModal(p.id ? '编辑端口 ' + p.port : '添加端口规则',
    '<div class="form-row"><label>所属用户</label><select id="pUser">' + uOpts + '</select></div>' +
    '<div class="form-row"><label>端口号（必须在该用户分配的端口段内）</label><input type="number" id="pPort" value="' + (p.port || '') + '" min="1" max="65535" ' + (p.id ? 'disabled' : '') + '></div>' +
    '<div class="form-row"><label>监听类型</label><div class="checkbox-row">' +
    '<label><input type="checkbox" id="pTcp"' + (p.tcp ? ' checked' : '') + '> TCP</label>' +
    '<label><input type="checkbox" id="pUdp"' + (p.udp ? ' checked' : '') + '> UDP</label></div></div>' +
    '<div class="form-row"><label>协议白名单</label><select id="pAllowed">' +
    '<option value="auto"' + (p.allowed === 'auto' ? ' selected' : '') + '>自动识别（三种均可）</option>' +
    '<option value="socks5"' + (p.allowed === 'socks5' ? ' selected' : '') + '>仅 SOCKS5</option>' +
    '<option value="wireguard"' + (p.allowed === 'wireguard' ? ' selected' : '') + '>仅 WireGuard</option>' +
    '<option value="openvpn"' + (p.allowed === 'openvpn' ? ' selected' : '') + '>仅 OpenVPN</option></select></div>' +
    '<div class="form-row"><label>目标地址（转发到哪台服务器，格式 ip:端口）</label><input type="text" id="pTarget" value="' + esc(p.target || '') + '" placeholder="例如 1.2.3.4:51820"></div>' +
    '<div class="form-row"><label>状态</label><select id="pEnabled">' +
    '<option value="true"' + (p.enabled ? ' selected' : '') + '>启用</option>' +
    '<option value="false"' + (!p.enabled ? ' selected' : '') + '>停用</option></select></div>',
    async (body) => {
      const tcp = body.querySelector('#pTcp').checked;
      const udp = body.querySelector('#pUdp').checked;
      if (!tcp && !udp) throw new Error('至少选择 TCP 或 UDP');
      const payload = {
        user_id: parseInt(body.querySelector('#pUser').value),
        port: parseInt(body.querySelector('#pPort').value),
        tcp,
        udp,
        allowed: body.querySelector('#pAllowed').value,
        target: body.querySelector('#pTarget').value.trim(),
        enabled: body.querySelector('#pEnabled').value === 'true',
      };
      if (!payload.port) throw new Error('端口号必填');
      if (p.id) {
        await api('PUT', '/api/admin/ports/' + p.id, payload);
        toast('已保存');
      } else {
        await api('POST', '/api/admin/ports', payload);
        toast('已创建');
      }
      loadPorts();
    }, p.id ? '保存' : '创建');
}

// ---------------- 协议/端口记录 ----------------

async function loadRecords() {
  const query = () => {
    const q = new URLSearchParams();
    const port = document.getElementById('recPort').value.trim();
    const proto = document.getElementById('recProto').value;
    const protocol = document.getElementById('recProtocol').value;
    const action = document.getElementById('recAction').value;
    if (port) q.set('port', port);
    if (proto) q.set('proto', proto);
    if (protocol) q.set('protocol', protocol);
    if (action) q.set('action', action);
    q.set('limit', '200');
    return q.toString();
  };

  const [stats, records] = await Promise.all([
    api('GET', '/api/admin/records/stats'),
    api('GET', '/api/admin/records?' + query()),
  ]);

  const byAction = stats.by_action || {};
  const byProto = stats.by_protocol || {};
  const statCards = [
    { label: '全部允许', value: byAction.allow || 0, cls: 'green' },
    { label: '全部拒绝', value: Object.keys(byAction).filter(k => k.startsWith('deny')).reduce((s, k) => s + (byAction[k] || 0), 0), cls: 'red' },
    { label: 'SOCKS5 允许/拒绝', value: (byProto['socks5/allow'] || 0) + ' / ' + (byProto['socks5/deny_protocol'] || 0), cls: '' },
    { label: 'WireGuard 允许/拒绝', value: (byProto['wireguard/allow'] || 0) + ' / ' + (byProto['wireguard/deny_protocol'] || 0), cls: '' },
    { label: 'OpenVPN 允许/拒绝', value: (byProto['openvpn/allow'] || 0) + ' / ' + (byProto['openvpn/deny_protocol'] || 0), cls: '' },
  ];
  document.getElementById('recStats').innerHTML = statCards.map(c =>
    '<div class="stat-card"><div class="label">' + c.label + '</div><div class="value ' + c.cls + '">' + c.value + '</div></div>').join('');

  const tbody = document.querySelector('#recordsTable tbody');
  tbody.innerHTML = records.map(r =>
    '<tr><td>' + fmtTime(r.time) + '</td><td>' + esc(r.username || '-') + '</td><td>' + r.port +
    '</td><td>' + (r.proto === 'udp' ? 'UDP' : 'TCP') + '</td><td>' + protoBadge(r.protocol) +
    '</td><td class="mono">' + esc(r.source) + '</td><td>' + actionBadge(r.action) +
    '</td><td class="mono">' + esc(r.target || '-') + '</td></tr>').join('') ||
    '<tr><td colspan="8" class="empty">无符合条件的记录</td></tr>';

  document.getElementById('recSearch').onclick = loadRecords;
}

// ---------------- 系统设置 ----------------

async function loadSettings() {
  const st = await api('GET', '/api/admin/settings');
  document.getElementById('setAllowRegister').value = String(st.allow_register);
  const groups = await api('GET', '/api/admin/groups');
  document.getElementById('setDefaultGroup').innerHTML = groups.map(g =>
    '<option value="' + g.id + '"' + (g.id == st.default_group_id ? ' selected' : '') + '>' + esc(g.name) + '</option>').join('');
  document.getElementById('setForwardMode').value = st.forward_mode;
  document.getElementById('setRealmBin').value = st.realm_bin;
  document.getElementById('setRecordsLimit').value = st.records_limit;

  // realm 状态
  const stats = await api('GET', '/api/admin/stats');
  document.getElementById('realmStatus').innerHTML =
    'realm: ' + (stats.realm.running ? '<span class="badge green">运行中</span>' : '<span class="badge red">未运行</span>') +
    ' &nbsp;转发模式: ' + (stats.realm.mode === 'realm' ? 'realm' : '直连');

  document.getElementById('saveSettingsBtn').onclick = async () => {
    const payload = {
      allow_register: document.getElementById('setAllowRegister').value === 'true',
      default_group_id: parseInt(document.getElementById('setDefaultGroup').value),
      forward_mode: document.getElementById('setForwardMode').value,
      realm_bin: document.getElementById('setRealmBin').value.trim(),
      records_limit: parseInt(document.getElementById('setRecordsLimit').value) || 3000,
    };
    await api('PUT', '/api/admin/settings', payload);
    toast('设置已保存，转发配置已重新加载');
    loadSettings();
  };
  document.getElementById('realmReloadBtn').onclick = async () => {
    await api('POST', '/api/admin/realm/reload');
    toast('realm 已重新加载');
    loadSettings();
  };
}

initAdmin();
