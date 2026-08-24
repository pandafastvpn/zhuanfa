package main

// 协议识别：基于连接的前几个字节判断是否是允许的三种协议。
// 只允许 socks5 / wireguard / openvpn，其余一律返回 unknown（将被拒绝）。

const (
	ProtoUnknown   = "unknown"
	ProtoSocks5    = "socks5"
	ProtoWireGuard = "wireguard"
	ProtoOpenVPN   = "openvpn"
)

// detectProtocol 根据初始字节流识别协议。
//
//   - socks5:   第一个字节为 0x05（VER=5）
//   - wireguard: 前 4 字节为小端 uint32 消息类型 1/2/3（握手发起/响应/cookie），
//     数据包长度固定为 64/92/148 字节
//   - openvpn:  第一个字节高 5 位为 opcode，客户端硬重置为 1/7/10
//     (P_CONTROL_HARD_RESET_CLIENT_V1/V2/V3)，低 3 位为 key_id
//
// 注：HTTP/2 前导 "PRI * HTTP/2.0" 首字节 'P'(0x50) 高 5 位恰好等于 10，
// 可能与 OpenVPN 误判，这是 OpenVPN 浅层识别固有的局限。
func detectProtocol(buf []byte) string {
	if len(buf) >= 3 && buf[0] == 0x05 {
		return ProtoSocks5
	}
	if len(buf) >= 4 && len(buf) <= 148 {
		t := buf[0]
		if (t == 1 || t == 2 || t == 3) && buf[1] == 0 && buf[2] == 0 && buf[3] == 0 && len(buf) >= 64 {
			return ProtoWireGuard
		}
	}
	// UDP OpenVPN 直接以 opcode 开头；TCP OpenVPN 在每个数据包前还有
	// 2 字节的大端长度前缀，因此 TCP 首字节通常是 0，不能直接按 buf[0] 判断。
	for _, offset := range []int{0, 2} {
		if len(buf) <= offset {
			continue
		}
		op := buf[offset] >> 3
		keyID := buf[offset] & 0x07
		// TCP 首包可能被分片，只校验 opcode/key_id，不要求一次读完整控制包。
		if (op == 1 || op == 7 || op == 10) && keyID == 0 {
			if offset == 0 || (len(buf) >= 3 && int(buf[0])<<8|int(buf[1]) > 0) {
				return ProtoOpenVPN
			}
		}
	}
	return ProtoUnknown
}

// allowedFor 判断识别出的协议是否允许在该端口规则上通过。
// 规则可以指定只放行某一种协议（socks5/wireguard/openvpn），
// 或者 auto（三种均可）。
func allowedFor(p *Port, proto string) bool {
	switch p.Allowed {
	case ProtoSocks5, ProtoWireGuard, ProtoOpenVPN:
		return proto == p.Allowed
	}
	return proto != ProtoUnknown
}
