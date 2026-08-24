#!/usr/bin/env bash
# ============================================================================
# 转发面板一键安装脚本（Debian 12）
#
# 功能：
#   1. 安装 Go 编译面板二进制
#   2. 下载 realm（realm 负责实际转发）
#   3. 注册 systemd 服务并启动
#
# 用法：
#   bash install.sh
# ============================================================================
set -euo pipefail

REALM_VERSION="v2.9.4"

log()  { echo -e "\033[32m[安装]\033[0m $*"; }
warn() { echo -e "\033[33m[警告]\033[0m $*"; }
die()  { echo -e "\033[31m[错误]\033[0m $*" >&2; exit 1; }

# ---------- 环境检查 ----------
[ "$(id -u)" -eq 0 ] || die "请使用 root 用户执行安装"

if [ ! -f /etc/debian_version ]; then
  warn "当前不是 Debian 系统，脚本主要为 Debian 12 设计，继续尝试安装..."
else
  ver=$(cat /etc/debian_version)
  log "检测到 Debian 版本: $ver"
fi

command -v systemctl >/dev/null 2>&1 || die "未检测到 systemd，无法安装服务"

# ---------- 架构检测 ----------
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) REALM_ARCH="x86_64-unknown-linux-gnu" ;;
  aarch64|arm64) REALM_ARCH="aarch64-unknown-linux-gnu" ;;
  *) die "不支持的架构: $ARCH（仅支持 x86_64 / aarch64）" ;;
esac

# ---------- 安装依赖 ----------
log "安装依赖 (golang-go curl tar)..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq golang-go curl tar ca-certificates >/dev/null

# 检查 Go 版本（需要 >= 1.19）
GO_VER=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1 | tr -d 'go')
if [ -n "$GO_VER" ] && [ "$(echo "$GO_VER" | awk -F. '{print $1*100+$2}')" -lt 119 ]; then
  die "Go 版本过低 ($GO_VER)，请手动安装 Go >= 1.19 后重试"
fi

# ---------- 编译面板 ----------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -f "$SCRIPT_DIR/go.mod" ]; then
  die "未找到 go.mod，请进入项目目录后运行 bash install.sh"
fi
log "编译面板二进制..."
cd "$SCRIPT_DIR"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/local/bin/zhuanfa-panel .
log "面板已编译: /usr/local/bin/zhuanfa-panel"

# ---------- 下载 realm ----------
REALM_BIN="/usr/local/bin/realm"
if [ -x "$REALM_BIN" ] && "$REALM_BIN" --version >/dev/null 2>&1; then
  log "realm 已存在，跳过下载"
else
  log "下载 realm $REALM_VERSION ($REALM_ARCH)..."
  RELEASE_URL="https://github.com/zhboner/realm/releases/download/${REALM_VERSION}/realm-${REALM_ARCH}.tar.gz"
  TMP_TGZ="$(mktemp /tmp/realm.XXXXXX.tar.gz)"
  TMP_DIR="$(mktemp -d /tmp/realm.XXXXXX)"

  download_ok=""
  # 依次尝试官方源与国内镜像源
  for base in \
    "https://github.com" \
    "https://ghfast.top/https://github.com" \
    "https://gh-proxy.com/https://github.com" \
    "https://ghproxy.net/https://github.com" \
    "https://mirror.ghproxy.com/https://github.com"; do
    url="${base}${RELEASE_URL#https://github.com}"
    log "尝试下载: $url"
    if curl -fsSL --connect-timeout 10 --max-time 300 -o "$TMP_TGZ" "$url"; then
      download_ok=1
      break
    fi
  done

  [ -n "$download_ok" ] || die "realm 下载失败，请手动下载 ${RELEASE_URL} 并解压 realm 到 /usr/local/bin/realm"

  tar -xzf "$TMP_TGZ" -C "$TMP_DIR"
  find "$TMP_DIR" -type f -name "realm" -exec install -m 0755 {} "$REALM_BIN" \;
  rm -rf "$TMP_TGZ" "$TMP_DIR"
  log "realm 已安装: $REALM_BIN"
fi

# ---------- 目录 ----------
DATA_DIR="/var/lib/zhuanfa"
mkdir -p "$DATA_DIR"

# ---------- systemd 服务 ----------
log "注册 systemd 服务..."
cat > /etc/systemd/system/zhuanfa.service <<'EOF'
[Unit]
Description=Zhuanfa Forwarding Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/zhuanfa-panel -data /var/lib/zhuanfa -listen :8080
Restart=always
RestartSec=3
LimitNOFILE=1048576
Environment=GOGC=400

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable zhuanfa >/dev/null 2>&1 || true
systemctl restart zhuanfa
sleep 1
systemctl --no-pager --lines=20 status zhuanfa | head -n 25 || true

if systemctl is-active --quiet zhuanfa; then
  log "面板服务运行中 ✓"
else
  warn "面板服务启动失败，请查看日志: journalctl -u zhuanfa -n 50"
fi

# ---------- 完成 ----------
IP=$(hostname -I 2>/dev/null | awk '{print $1}')
cat <<EOF

============================================================
  安装完成！
============================================================
  管理后台:  http://${IP:-<服务器IP>}:8080/admin
  初始账号:  admin
  初始密码:  admin123  （请登录后立即修改！）

  常见命令:
    查看状态    systemctl status zhuanfa
    查看日志    journalctl -u zhuanfa -f
    重启服务    systemctl restart zhuanfa

  使用提示:
    1. 首次登录后，在"用户管理"中给用户分配端口段
       （例如 1000-11000）
    2. 在"端口管理"中添加转发规则：选择用户、端口、
       监听类型(TCP/UDP)、协议白名单、目标地址
    3. 端口只放行 SOCKS5 / WireGuard / OpenVPN，
       其他协议连接会被拒绝并记录在"协议/端口记录"中
    4. 在"用户组"中设置宽带峰值与总流量配额
    5. 如需开放自助注册，在"系统设置"中开启

  日志文件: /var/lib/zhuanfa/realm.log
============================================================
EOF
