// 公共工具函数
function esc(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

async function api(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  let data = null;
  try { data = await res.json(); } catch (e) { /* ignore */ }
  if (!res.ok) {
    throw new Error((data && data.error) || ('HTTP ' + res.status));
  }
  return data ? data.data : null;
}

function fmtBytes(n) {
  n = Number(n || 0);
  if (n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return n.toFixed(n >= 100 ? 0 : 1) + ' ' + units[i];
}

function fmtMbps(m) {
  if (!m) return '不限';
  return m + ' Mbps';
}

function fmtQuota(gb) {
  if (!gb) return '不限';
  return gb + ' GB';
}

function fmtTime(ts) {
  if (!ts) return '-';
  const d = new Date(Number(ts) * 1000);
  const p = (x) => String(x).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) +
    ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

function fmtDate(ts) {
  if (!ts) return '-';
  const d = new Date(Number(ts) * 1000);
  const p = (x) => String(x).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
}

function toast(msg, type) {
  let wrap = document.querySelector('.toast-wrap');
  if (!wrap) {
    wrap = document.createElement('div');
    wrap.className = 'toast-wrap';
    document.body.appendChild(wrap);
  }
  const t = document.createElement('div');
  t.className = 'toast ' + (type || '');
  t.textContent = msg;
  wrap.appendChild(t);
  setTimeout(() => t.remove(), 3500);
}

function openModal(title, bodyHTML, onConfirm, confirmText) {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML =
    '<div class="modal"><h3>' + esc(title) + '</h3><div class="modal-body">' + bodyHTML +
    '</div><div class="actions">' +
    '<button class="btn secondary" data-cancel>取消</button>' +
    '<button class="btn" data-ok>' + esc(confirmText || '确定') + '</button>' +
    '</div></div>';
  mask.addEventListener('click', (e) => {
    if (e.target === mask || e.target.hasAttribute('data-cancel')) mask.remove();
  });
  document.body.appendChild(mask);
  const okBtn = mask.querySelector('[data-ok]');
  okBtn.addEventListener('click', async () => {
    const body = mask.querySelector('.modal-body');
    try {
      okBtn.disabled = true;
      await onConfirm(body);
      mask.remove();
    } catch (err) {
      toast(err.message, 'err');
    } finally {
      okBtn.disabled = false;
    }
  });
  return mask;
}

function confirmModal(title, message, onConfirm, confirmText) {
  openModal(title, '<p>' + esc(message) + '</p>', onConfirm, confirmText || '确认删除');
}

function protoBadge(proto) {
  const map = {
    socks5: ['blue', 'SOCKS5'],
    wireguard: ['green', 'WireGuard'],
    openvpn: ['orange', 'OpenVPN'],
    unknown: ['gray', '未知'],
  };
  const [cls, label] = map[proto] || ['gray', esc(proto)];
  return '<span class="badge ' + cls + '">' + label + '</span>';
}

function actionBadge(action) {
  if (action === 'allow') return '<span class="badge green">允许</span>';
  const labels = {
    deny_protocol: '协议拒绝', deny_quota: '配额拒绝', deny_disabled: '端口停用',
    deny_user: '用户拒绝', deny_group: '用户组拒绝', deny_target: '目标不可达',
    deny_unbound: '未绑定',
  };
  return '<span class="badge red">' + (labels[action] || esc(action)) + '</span>';
}

function typeBadge(p) {
  const t = [];
  if (p.tcp) t.push('TCP');
  if (p.udp) t.push('UDP');
  return '<span class="badge blue">' + t.join('/') + '</span>';
}

function allowedLabel(s) {
  if (s === 'socks5') return '仅 SOCKS5';
  if (s === 'wireguard') return '仅 WireGuard';
  if (s === 'openvpn') return '仅 OpenVPN';
  return '自动识别';
}

function barChart(container, days) {
  const max = Math.max.apply(null, days.map(d => d.bytes).concat([1]));
  let html = '<div class="bar-chart">';
  days.forEach(d => {
    const h = Math.max(2, Math.round((d.bytes / max) * 80));
    html += '<div class="bar-item"><div class="bar" style="height:' + h + 'px" title="' +
      esc(fmtBytes(d.bytes)) + '"></div><div class="bar-label">' +
      d.date.slice(5) + '</div></div>';
  });
  html += '</div>';
  container.innerHTML = html;
}

async function onloadGuard() {
  // 会话检查：登录/注册页未登录，管理页管理员，用户页普通用户
  const p = location.pathname;
  try {
    const me = await api('GET', '/api/me');
    if (me && me.user.role === 'admin') {
      if (p === '/login' || p === '/register') location.href = '/admin';
    } else if (me) {
      if (p === '/login' || p === '/register') location.href = '/user';
    } else {
      if (p !== '/login' && p !== '/register') location.href = '/login';
    }
    return me;
  } catch (e) {
    if (p !== '/login' && p !== '/register') location.href = '/login';
    return null;
  }
}
