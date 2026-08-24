// ---------------- 用户中心逻辑 ----------------

const selectedPortIDs = new Set();
let portsPage = 1;
let portsPageSize = '20';

async function initUser() {
  const me = await onloadGuard();
  if (!me) return;
  if (me.user.role === 'admin') {
    location.href = '/admin';
    return;
  }

  document.getElementById('userName').textContent = '你好，' + me.user.username;
  document.getElementById('logoutBtn').addEventListener('click', async () => {
    await api('POST', '/api/logout');
    location.href = '/login';
  });

  const navs = document.querySelectorAll('.sidebar nav a');
  navs.forEach(n => n.addEventListener('click', () => {
    navs.forEach(x => x.classList.remove('active'));
    n.classList.add('active');
    document.querySelectorAll('.content > section').forEach(s => s.classList.add('hidden'));
    document.getElementById('page-' + n.dataset.page).classList.remove('hidden');
    loadPage(n.dataset.page);
  }));

  await loadPage('overview');
}

async function loadPage(page) {
  try {
    if (page === 'overview') await loadOverview();
    else if (page === 'ports') await loadMyPorts();
    else if (page === 'records') await loadMyRecords();
    else if (page === 'password') loadPassword();
  } catch (err) {
    toast(err.message, 'err');
  }
}

// ---------------- 概览 ----------------

async function loadOverview() {
  const d = await api('GET', '/api/user/usage');
  const g = d.group || {};
  const quota = g.quota_gb || 0;
  const used = d.user.total_bytes || 0;
  const over = quota > 0 && used >= quota * 1024 * 1024 * 1024;
  const pct = quota > 0 ? Math.min(100, Math.round(used / (quota * 1024 * 1024 * 1024) * 100)) : 0;
  const cards = [
    { label: '我的累计流量', value: fmtBytes(d.user.total_bytes), cls: 'green' },
    { label: '我的流量配额', value: fmtQuota(g.quota_gb), sub: '已用 ' + pct + '%', cls: over ? 'red' : '' },
    { label: '我的宽带限速', value: fmtMbps(g.bandwidth_mbps), sub: '当前用户独享', cls: '' },
    { label: '我的端口数', value: (d.ports || []).length + '/' + (d.allocated_ports || 0), sub: '已使用 / 已分配', cls: '' },
    { label: '用户组', value: esc(g.name || '-'), sub: '账户 ' + esc(d.user.username), cls: '' },
    { label: '账户状态', value: d.user.enabled ? '正常' : '禁用', sub: d.user.expire_at ? '到期 ' + fmtDate(d.user.expire_at) : '永久有效', cls: d.user.enabled ? 'green' : 'red' },
  ];
  document.getElementById('ovCards').innerHTML = cards.map(c =>
    '<div class="stat-card"><div class="label">' + c.label + '</div><div class="value ' + c.cls + '">' +
    c.value + '</div><div class="sub">' + (c.sub || '') + '</div></div>').join('');
  if (quota > 0) {
    document.getElementById('ovCards').insertAdjacentHTML('beforeend',
      '<div class="stat-card" style="grid-column:span 2"><div class="label">流量使用进度</div>' +
      '<div class="progress ' + (over ? 'over' : '') + '" style="width:100%;margin-top:12px"><div style="width:' + pct + '%"></div></div>' +
      '<div class="sub" style="margin-top:6px">' + fmtBytes(used) + ' / ' + fmtBytes(quota * 1024 * 1024 * 1024) + '</div></div>');
  }
  barChart(document.getElementById('ovChart'), d.last7 || []);
}

// ---------------- 我的端口 ----------------

async function loadMyPorts() {
  const d = await api('GET', '/api/user/ports?page=' + portsPage + '&page_size=' + encodeURIComponent(portsPageSize));
  const ranges = (d.ranges || []).map(r => r.start + ' - ' + r.end).join('、') || '暂无分配端口段，请联系管理员';
  document.getElementById('myRanges').textContent = '端口段: ' + ranges;

  const tbody = document.querySelector('#portsTable tbody');
  tbody.innerHTML = (d.ports || []).map(p =>
    '<tr><td><input type="checkbox" class="port-select" data-id="' + p.id + '"' + (selectedPortIDs.has(p.id) ? ' checked' : '') + '></td><td class="mono" style="font-weight:600">' + p.port + '</td><td>' + typeBadge(p) +
    '</td><td>' + allowedLabel(p.allowed) + '</td><td class="mono">' + esc(p.target) + '</td><td>' +
    (p.enabled ? '<span class="badge green">启用</span>' : '<span class="badge gray">停用</span>') +
    '</td><td>' + fmtBytes(p.total_bytes) + '</td><td>' + p.total_conns + '</td><td>' +
    '<button class="btn sm secondary" data-edit="' + p.id + '">编辑</button> ' +
    '<button class="btn sm danger" data-del="' + p.id + '">删除</button></td></tr>').join('') ||
    '<tr><td colspan="9" class="empty">暂无转发规则，点击右上角添加</td></tr>';

  portsPage = d.page || 1;
  const pageSize = document.getElementById('portsPageSize');
  pageSize.value = portsPageSize;
  pageSize.onchange = () => { portsPageSize = pageSize.value; portsPage = 1; loadMyPorts(); };
  document.getElementById('portsPageInfo').textContent = '第 ' + portsPage + ' / ' + (d.total_pages || 1) + ' 页，共 ' + (d.total || 0) + ' 条';
  const prev = document.getElementById('portsPrev'); const next = document.getElementById('portsNext');
  prev.disabled = portsPage <= 1;
  next.disabled = portsPage >= (d.total_pages || 1);
  prev.onclick = () => { if (portsPage > 1) { portsPage--; loadMyPorts(); } };
  next.onclick = () => { if (portsPage < (d.total_pages || 1)) { portsPage++; loadMyPorts(); } };

  const selectAll = document.getElementById('portsSelectAll');
  const checkboxes = tbody.querySelectorAll('.port-select');
  selectAll.checked = checkboxes.length > 0 && Array.from(checkboxes).every(b => b.checked);
  selectAll.onchange = () => { checkboxes.forEach(b => { b.checked = selectAll.checked; if (b.checked) selectedPortIDs.add(Number(b.dataset.id)); else selectedPortIDs.delete(Number(b.dataset.id)); }); updateBatchDeleteButton(); };
  checkboxes.forEach(b => b.onchange = () => { if (b.checked) selectedPortIDs.add(Number(b.dataset.id)); else selectedPortIDs.delete(Number(b.dataset.id)); selectAll.checked = checkboxes.length > 0 && Array.from(checkboxes).every(x => x.checked); updateBatchDeleteButton(); });
  updateBatchDeleteButton();
  document.getElementById('batchDeleteBtn').onclick = batchDeletePorts;
  document.getElementById('batchAddBtn').onclick = batchAddModal;
  document.getElementById('addPortBtn').onclick = () => portModal(null, d.free || []);
  tbody.querySelectorAll('[data-edit]').forEach(b => b.onclick = () => portModal(d.ports.find(p => p.id == b.dataset.edit), []));
  tbody.querySelectorAll('[data-del]').forEach(b => b.onclick = () => {
    const p = d.ports.find(x => x.id == b.dataset.del);
    confirmModal('删除转发规则', '确定删除端口 ' + p.port + ' 的转发规则？', async () => {
      await api('DELETE', '/api/user/ports/' + p.id);
      selectedPortIDs.delete(p.id);
      toast('已删除');
      loadMyPorts();
    });
  });
}

function portModal(p, freePorts) {
  p = p || { tcp: true, udp: false, allowed: 'auto', enabled: true };
  const freeOpts = (freePorts || []).map(n => '<option value="' + n + '">' + n + '</option>').join('');
  const mask = openModal(p.id ? '编辑转发规则' : '添加转发规则',
    '<div class="form-row"><label>端口号（在分配的端口段内，建议使用右侧空闲端口）</label>' +
    '<div class="flex"><input type="number" id="pPort" value="' + (p.port || '') + '" min="1" max="65535" ' + (p.id ? 'disabled' : '') + '>' +
    (freeOpts ? '<select id="pFreePort"><option value="">空闲端口…</option>' + freeOpts + '</select>' : '') +
    '</div></div>' +
    '<div class="form-row"><label>监听类型</label><div class="checkbox-row">' +
    '<label><input type="checkbox" id="pTcp"' + (p.tcp ? ' checked' : '') + '> TCP</label>' +
    '<label><input type="checkbox" id="pUdp"' + (p.udp ? ' checked' : '') + '> UDP</label></div></div>' +
    '<div class="form-row"><label>协议白名单</label><select id="pAllowed">' +
    '<option value="auto"' + (p.allowed === 'auto' ? ' selected' : '') + '>自动识别（SOCKS5/WireGuard/OpenVPN）</option>' +
    '<option value="socks5"' + (p.allowed === 'socks5' ? ' selected' : '') + '>仅 SOCKS5</option>' +
    '<option value="wireguard"' + (p.allowed === 'wireguard' ? ' selected' : '') + '>仅 WireGuard</option>' +
    '<option value="openvpn"' + (p.allowed === 'openvpn' ? ' selected' : '') + '>仅 OpenVPN</option></select></div>' +
    '<div class="form-row"><label>目标地址（转发目标，格式 ip:端口）</label><input type="text" id="pTarget" value="' + esc(p.target || '') + '" placeholder="例如 1.2.3.4:51820"></div>' +
    '<div class="form-row"><label>状态</label><select id="pEnabled">' +
    '<option value="true"' + (p.enabled ? ' selected' : '') + '>启用</option>' +
    '<option value="false"' + (!p.enabled ? ' selected' : '') + '>停用</option></select></div>',
    async (body) => {
      const tcp = body.querySelector('#pTcp').checked;
      const udp = body.querySelector('#pUdp').checked;
      if (!tcp && !udp) throw new Error('至少选择 TCP 或 UDP');
      let port = parseInt(body.querySelector('#pPort').value);
      const freeSel = body.querySelector('#pFreePort');
      if (freeSel && freeSel.value) port = parseInt(freeSel.value);
      if (!port) throw new Error('端口号必填');
      const payload = {
        port,
        tcp,
        udp,
        allowed: body.querySelector('#pAllowed').value,
        target: body.querySelector('#pTarget').value.trim(),
        enabled: body.querySelector('#pEnabled').value === 'true',
      };
      if (p.id) {
        await api('PUT', '/api/user/ports/' + p.id, payload);
        toast('已保存');
      } else {
        await api('POST', '/api/user/ports', payload);
        toast('已创建');
      }
      loadMyPorts();
    }, p.id ? '保存' : '创建');
}

function updateBatchDeleteButton() {
  const btn = document.getElementById('batchDeleteBtn');
  if (!btn) return;
  btn.disabled = selectedPortIDs.size === 0;
  btn.textContent = selectedPortIDs.size ? '删除已选 (' + selectedPortIDs.size + ')' : '删除已选';
}

function batchDeletePorts() {
  const ids = Array.from(selectedPortIDs);
  if (!ids.length) return;
  confirmModal('删除已选规则', '确定删除已选的 ' + ids.length + ' 条转发规则？', async () => {
    await api('POST', '/api/user/ports/batch-delete', { ids });
    selectedPortIDs.clear();
    toast('已删除 ' + ids.length + ' 条规则');
    loadMyPorts();
  }, '确认删除');
}

function batchAddModal() {
  openModal('批量添加转发规则',
    '<div class="form-row"><label>规则列表（每行：转发端口:目标地址；IPv6 目标请写为 [IPv6]:端口）</label><textarea id="batchRules" rows="9" placeholder="10000:1.2.3.4:51820\n10001:[2001:db8::1]:1194"></textarea></div>' +
    '<div class="form-row"><label>监听类型</label><div class="checkbox-row"><label><input type="checkbox" id="batchTcp" checked> TCP</label><label><input type="checkbox" id="batchUdp"> UDP</label></div></div>' +
    '<div class="form-row"><label>协议白名单</label><select id="batchAllowed"><option value="auto">自动识别（三种均可）</option><option value="socks5">仅 SOCKS5</option><option value="wireguard">仅 WireGuard</option><option value="openvpn">仅 OpenVPN</option></select></div>' +
    '<div class="form-row"><label>状态</label><select id="batchEnabled"><option value="true">启用</option><option value="false">停用</option></select></div>',
    async (body) => {
      const tcp = body.querySelector('#batchTcp').checked;
      const udp = body.querySelector('#batchUdp').checked;
      if (!tcp && !udp) throw new Error('至少选择 TCP 或 UDP');
      const lines = body.querySelector('#batchRules').value.split(/\r?\n/).map(s => s.trim()).filter(Boolean);
      if (!lines.length) throw new Error('请至少输入一条规则');
      if (lines.length > 200) throw new Error('一次最多添加 200 条规则');
      const rules = lines.map((line, index) => {
        const match = line.match(/^(\d+)\s*:\s*(.+)$/);
        if (!match) throw new Error('第 ' + (index + 1) + ' 行格式错误，应为 转发端口:目标地址');
        return { port: Number(match[1]), target: match[2].trim(), tcp, udp, allowed: body.querySelector('#batchAllowed').value, enabled: body.querySelector('#batchEnabled').value === 'true' };
      });
      await api('POST', '/api/user/ports/batch', { rules });
      toast('已添加 ' + rules.length + ' 条规则');
      portsPage = 1;
      loadMyPorts();
    }, '批量添加');
}

// ---------------- 使用记录 ----------------

async function loadMyRecords() {
  const records = await api('GET', '/api/user/records?limit=200');
  const tbody = document.querySelector('#recordsTable tbody');
  tbody.innerHTML = (records || []).map(r =>
    '<tr><td>' + fmtTime(r.time) + '</td><td>' + r.port + '</td><td>' +
    (r.proto === 'udp' ? 'UDP' : 'TCP') + '</td><td>' + protoBadge(r.protocol) +
    '</td><td class="mono">' + esc(r.source) + '</td><td>' + actionBadge(r.action) +
    '</td><td class="mono">' + esc(r.target || '-') + '</td></tr>').join('') ||
    '<tr><td colspan="7" class="empty">暂无记录</td></tr>';
}

// ---------------- 修改密码 ----------------

function loadPassword() {
  document.getElementById('pwBtn').onclick = async () => {
    const old = document.getElementById('pwOld').value;
    const nw = document.getElementById('pwNew').value;
    const nw2 = document.getElementById('pwNew2').value;
    if (!old || !nw) { toast('请填写完整', 'err'); return; }
    if (nw.length < 6) { toast('新密码至少 6 位', 'err'); return; }
    if (nw !== nw2) { toast('两次密码不一致', 'err'); return; }
    try {
      await api('PUT', '/api/user/password', { old_password: old, new_password: nw });
      toast('密码已修改');
      document.getElementById('pwOld').value = '';
      document.getElementById('pwNew').value = '';
      document.getElementById('pwNew2').value = '';
    } catch (err) {
      toast(err.message, 'err');
    }
  };
}

initUser();
