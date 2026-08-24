# 转发面板 (Zhuanfa Panel)

基于 Go 的流量转发控制面板，只放行 **SOCKS5 / WireGuard / OpenVPN（TCP+UDP）** 三种协议，
其余协议一律拒绝并记录。转发引擎使用 [realm](https://github.com/zhboner/realm)。

## 功能特性

- **协议白名单**：端口只允许 SOCKS5、WireGuard、OpenVPN 流量通过
  - SOCKS5：首字节 `0x05` 识别
  - WireGuard：握手消息类型（`01 00 00 00` / `02 00 00 00` / `03 00 00 00`）识别
  - OpenVPN：opcode 识别（`P_CONTROL_HARD_RESET_CLIENT_V1/V2/V3`），TCP 与 UDP 均支持
  - 其他协议连接直接拒绝，并在后台留下记录
- **转发引擎**：使用 realm 完成 TCP/UDP 转发
  - 数据路径：`客户端 -> 面板(协议检测/限速/计费) -> realm -> 目标服务器`
  - 支持"直连模式"（调试用）
- **多用户系统**
  - 用户自助注册（管理员可开关）
  - 管理员分配端口段（如 `1000-11000`）给用户
  - 用户在分配端口段内自助建立转发规则
  - 管理员可修改任意用户密码、状态、过期时间、角色
- **用户组限速/配额**
  - 组宽带峰值（Mbps，组内用户共享令牌桶限速）
  - 组总流量配额（GB，超限自动断开所有连接并拒绝新连接）
- **协议/端口记录**：后台专门页面记录每次连接的时间、端口、TCP/UDP 类型、
  识别协议、来源 IP、动作（允许/拒绝原因）、目标地址，支持多条件筛选
- **流量统计**：按端口 / 用户 / 用户组累计，仪表盘展示近 7 天流量曲线

## 技术栈

- 后端：Go 1.19+（纯标准库，无第三方依赖），数据存储为 JSON 文件
- 前端：原生 HTML/CSS/JS（无需 npm），通过 `go:embed` 内嵌到二进制
- 转发：realm v2（Rust 编写的 TCP/UDP 中继）

## 快速安装（Debian 12）

```bash
apt-get update && apt-get install -y git
git clone https://github.com/pandafastvpn/zhuafa.git && cd zhuanfa
bash install.sh
```

安装完成后：

- 管理后台：`http://<服务器IP>:8080/admin`
- 初始账号：`admin` / `admin123`（请立即修改）

## 手动启动

```bash
# 需要先安装 realm 到 /usr/local/bin/realm
go build -o zhuanfa-panel .
./zhuanfa-panel -data ./data -listen :8080
```

## 使用流程

1. **管理员登录**，在「用户管理」创建用户并分配端口段（如 `1000-11000`）
2. 在「用户组」设置宽带峰值与总流量配额
3. 在「端口管理」添加转发规则：
   - 选择用户、端口（须在该用户的端口段内）
   - 监听类型：TCP / UDP（可同时勾选）
   - 协议白名单：自动识别 / 仅 SOCKS5 / 仅 WireGuard / 仅 OpenVPN
   - 目标地址：`目标服务器IP:端口`
4. 用户即可连接 `面板IP:端口` 使用转发服务

### 各协议用法示例

| 协议 | 客户端配置 | 说明 |
|------|-----------|------|
| SOCKS5 | 面板IP:端口 作为 SOCKS5 代理 | 目标须为可用的 SOCKS5 服务器 |
| WireGuard | Endpoint = 面板IP:端口 | 目标须为 WireGuard 服务器（UDP） |
| OpenVPN | 面板IP:端口 | 目标须为 OpenVPN 服务器（TCP 或 UDP） |

## 架构说明

```
                    ┌────────────────────────── 面板 (zhuanfa-panel) ──────────────────────────┐
客户端 ───────────► │ 端口监听(公网) → 协议嗅探 → 允许/拒绝(记录) → 令牌桶限速 → 流量计费      │
(互联网)            │                                  │                                     │
                    └──────────────────────────────────┼─────────────────────────────────────┘
                                                       ▼
                                             realm (127.0.0.1:内部端口)
                                                       │
                                                       ▼
                                                 目标服务器
```

- realm 由面板自动管理：面板根据端口规则生成 realm 配置并在规则变化时重启 realm
- realm 内部监听端口池为 `20000-29999`（最多约 1 万条规则）
- 面板进程意外退出后，systemd 会自动拉起；realm 进程异常退出后面板每 30 秒自动重启

## 常见问题

- **转发不可用**：确认 `/usr/local/bin/realm` 存在且可执行，
  面板日志中如出现 "realm 可执行文件不存在" 请检查「系统设置」中的 realm 路径
- **端口无法绑定**：端口已被其他程序占用时规则会自动跳过，请更换端口
- **OpenVPN 识别误判**：HTTP/2 前导与 OpenVPN 特征字节相似，
  如端口仅用于 OpenVPN 建议在规则中指定「仅 OpenVPN」，其余流量会被拒绝
- **数据存储**：`/var/lib/zhuanfa/db.json`（配置与统计）、`records.json`（连接记录）
- **改端口**：编辑 `/etc/systemd/system/zhuanfa.service` 中的 `-listen` 参数

## 安全建议

- 首次登录后立即修改 admin 密码
- 建议在 Nginx 后配置 HTTPS 反向代理
- 定期备份 `/var/lib/zhuanfa/` 目录

## License

MIT
