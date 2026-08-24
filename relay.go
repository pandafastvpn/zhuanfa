package main

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------- 令牌桶（带宽限速） ----------------

type TokenBucket struct {
	mu     sync.Mutex
	rate   float64 // 每秒字节数
	cap    float64 // 桶容量（突发）
	tokens float64
	last   time.Time
}

func NewTokenBucket(bytesPerSec float64) *TokenBucket {
	if bytesPerSec <= 0 {
		return nil
	}
	return &TokenBucket{
		rate:   bytesPerSec,
		cap:    bytesPerSec * 1.5, // 允许 1.5 秒突发
		tokens: bytesPerSec,
		last:   time.Now(),
	}
}

// Wait 消耗 n 字节的令牌，不足则等待。
func (b *TokenBucket) Wait(n int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.cap {
		b.tokens = b.cap
	}
	b.last = now
	need := float64(n)
	if b.tokens >= need {
		b.tokens -= need
		b.mu.Unlock()
		return
	}
	missing := need - b.tokens
	b.tokens = 0
	wait := time.Duration(missing / b.rate * float64(time.Second))
	b.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

// ---------------- 活跃连接 ----------------

type activeConn struct {
	id        int
	userID    int
	groupID   int
	portID    int
	portNum   int
	proto     string // tcp | udp
	upBytes   atomic.Int64
	downBytes atomic.Int64
	killed    atomic.Bool
	closeFn   func()
}

// ---------------- 转发引擎 ----------------

type Relay struct {
	store   *Store
	rm      *RealmManager
	mu      sync.Mutex
	tcp     map[int]*net.TCPListener
	udp     map[int]*net.UDPConn
	buckets map[int]*TokenBucket // 每个用户独立的限速桶，key 为 userID
	conns   map[int]*activeConn
	connSeq atomic.Int64
	stopCh  chan struct{}
}

func NewRelay(store *Store, rm *RealmManager) *Relay {
	return &Relay{
		store:   store,
		rm:      rm,
		tcp:     map[int]*net.TCPListener{},
		udp:     map[int]*net.UDPConn{},
		buckets: map[int]*TokenBucket{},
		conns:   map[int]*activeConn{},
	}
}

func hostOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

// Start 启动后台任务并应用当前规则。
func (r *Relay) Start() {
	r.mu.Lock()
	r.stopCh = make(chan struct{})
	r.mu.Unlock()
	go r.loop()
	r.Reload()
}

// ResetUserTraffic 清零用户统计，并丢弃该用户当前连接尚未结算的流量。
func (r *Relay) ResetUserTraffic(userID int) bool {
	r.mu.Lock()
	for _, c := range r.conns {
		if c.userID == userID {
			c.upBytes.Store(0)
			c.downBytes.Store(0)
		}
	}
	r.mu.Unlock()
	return r.store.ResetUserTraffic(userID)
}

func (r *Relay) Stop() {
	r.mu.Lock()
	if r.stopCh != nil {
		close(r.stopCh)
		r.stopCh = nil
	}
	for _, l := range r.tcp {
		l.Close()
	}
	for _, u := range r.udp {
		u.Close()
	}
	r.tcp = map[int]*net.TCPListener{}
	r.udp = map[int]*net.UDPConn{}
	r.mu.Unlock()
}

func (r *Relay) loop() {
	flushTicker := time.NewTicker(15 * time.Second)
	saveTicker := time.NewTicker(60 * time.Second)
	for {
		select {
		case <-r.stopCh:
			return
		case <-flushTicker.C:
			r.flush()
		case <-saveTicker.C:
			_ = r.store.saveRecords()
		}
	}
}

// Reload 根据数据库中的规则增删监听器。
func (r *Relay) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.buckets = map[int]*TokenBucket{}

	ports := r.store.Ports()
	desired := map[int]*Port{}
	for _, p := range ports {
		if p.Enabled && (p.TCP || p.UDP) {
			desired[p.Port] = p
		}
	}

	for num, l := range r.tcp {
		if _, ok := desired[num]; !ok {
			l.Close()
			delete(r.tcp, num)
		}
	}
	for num, u := range r.udp {
		if _, ok := desired[num]; !ok {
			u.Close()
			delete(r.udp, num)
		}
	}

	for num, p := range desired {
		if _, ok := r.tcp[num]; !ok && p.TCP {
			l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", num))
			if err != nil {
				continue
			}
			tl := l.(*net.TCPListener)
			r.tcp[num] = tl
			go r.acceptTCP(p, tl)
		}
		if _, ok := r.udp[num]; !ok && p.UDP {
			u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: num})
			if err != nil {
				continue
			}
			r.udp[num] = u
			go r.udpLoop(p, u)
		}
	}
}

func (r *Relay) acceptTCP(p *Port, l *net.TCPListener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go r.handleTCP(p, conn)
	}
}

// bucket 获取某个用户组的共享令牌桶（组内所有用户共享带宽峰值）。
func (r *Relay) bucket(userID int, bandwidthMbps int) *TokenBucket {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.buckets[userID]; ok {
		return b
	}
	b := NewTokenBucket(float64(bandwidthMbps) * 1e6 / 8.0)
	r.buckets[userID] = b
	return b
}

// sniffTCP 读取客户端初始数据（最多 148 字节）用于协议识别。
// 已读取的数据会回放给下游。
func sniffTCP(conn net.Conn) ([]byte, string) {
	buf := make([]byte, 0, 148)
	tmp := make([]byte, 148)
	conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	for len(buf) < 148 {
		if len(buf) > 0 {
			conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		}
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			continue
		}
		if err != nil {
			break
		}
	}
	conn.SetReadDeadline(time.Time{})
	return buf, detectProtocol(buf)
}

// canUse 检查端口/用户/组状态与流量配额。
func (r *Relay) canUse(p *Port) (*User, *Group, string) {
	if !p.Enabled {
		return nil, nil, "deny_disabled"
	}
	u := r.store.UserByID(p.UserID)
	if u == nil || !u.Enabled || u.Expired() {
		return nil, nil, "deny_user"
	}
	g := r.store.GroupByID(u.GroupID)
	if g == nil || !g.Enabled {
		return nil, nil, "deny_group"
	}
	if q := g.QuotaBytes(); q > 0 && u.TotalBytes >= q {
		return nil, nil, "deny_quota"
	}
	return u, g, "allow"
}

// logRecord 写入连接记录与当日统计。
func (r *Relay) logRecord(p *Port, proto, protocol, source, action, target string) {
	rec := &Record{
		Time:     time.Now().Unix(),
		UserID:   p.UserID,
		Port:     p.Port,
		Proto:    proto,
		Protocol: protocol,
		Source:   source,
		Action:   action,
		Target:   target,
	}
	if u := r.store.UserByID(p.UserID); u != nil {
		rec.Username = u.Username
	}
	r.store.AddRecord(rec)
	r.store.DayEvent(time.Now().Format("2006-01-02"), protocol, action == "allow")
}

func (r *Relay) register(p *Port, u *User, g *Group, proto string, closeFn func()) *activeConn {
	c := &activeConn{
		id:      int(r.connSeq.Add(1)),
		userID:  u.ID,
		groupID: g.ID,
		portID:  p.ID,
		portNum: p.Port,
		proto:   proto,
		closeFn: closeFn,
	}
	r.mu.Lock()
	r.conns[c.id] = c
	r.mu.Unlock()
	r.store.IncConn(p.ID)
	return c
}

func (r *Relay) unregister(c *activeConn) {
	r.mu.Lock()
	delete(r.conns, c.id)
	r.mu.Unlock()
	up := c.upBytes.Swap(0)
	down := c.downBytes.Swap(0)
	if up+down > 0 {
		r.store.AddTraffic(c.portID, c.userID, c.groupID, up+down)
	}
}

// handleTCP 处理一条 TCP 连接：嗅探 -> 放行/拒绝 -> 转发。
func (r *Relay) handleTCP(p *Port, conn net.Conn) {
	defer conn.Close()
	src := hostOf(conn.RemoteAddr().String())

	buf, proto := sniffTCP(conn)
	if proto == ProtoUnknown || !allowedFor(p, proto) {
		r.logRecord(p, "tcp", proto, src, "deny_protocol", p.Target)
		return
	}
	user, group, action := r.canUse(p)
	if action != "allow" {
		r.logRecord(p, "tcp", proto, src, action, p.Target)
		return
	}
	down, err := r.rm.dialAddr(p)
	if err != nil {
		r.logRecord(p, "tcp", proto, src, "deny_unbound", p.Target)
		return
	}
	rc, err := net.DialTimeout("tcp", down, 5*time.Second)
	if err != nil {
		r.logRecord(p, "tcp", proto, src, "deny_target", p.Target)
		return
	}
	defer rc.Close()

	if len(buf) > 0 {
		rc.Write(buf)
	}
	r.logRecord(p, "tcp", proto, src, "allow", p.Target)

	c := r.register(p, user, group, "tcp", func() { conn.Close(); rc.Close() })
	defer r.unregister(c)

	b := r.bucket(user.ID, group.BandwidthMbps)
	done := make(chan struct{})
	go func() {
		pump(rc, conn, b, c, true)
		conn.Close()
		close(done)
	}()
	pump(conn, rc, b, c, false)
	rc.Close()
	<-done
}

// pump 双向搬运数据，应用限速与流量统计。
func pump(src, dst net.Conn, b *TokenBucket, c *activeConn, down bool) {
	buf := make([]byte, 32*1024)
	for {
		if c.killed.Load() {
			return
		}
		n, err := src.Read(buf)
		if n > 0 {
			if b != nil {
				b.Wait(n)
			}
			if c.killed.Load() {
				return
			}
			if down {
				c.downBytes.Add(int64(n))
			} else {
				c.upBytes.Add(int64(n))
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ---------------- UDP ----------------

type udpSession struct {
	rc         *net.UDPConn
	clientAddr *net.UDPAddr
	bucket     *TokenBucket
	last       atomic.Int64 // UnixNano
	conn       *activeConn
}

func (r *Relay) udpLoop(p *Port, pc *net.UDPConn) {
	var smu sync.Mutex
	sessions := map[string]*udpSession{}

	// 独立清理协程：空闲超时或配额断连
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			now := time.Now().UnixNano()
			smu.Lock()
			for key, s := range sessions {
				if now-s.last.Load() > 120*int64(time.Second) || s.conn.killed.Load() {
					s.rc.Close()
					r.unregister(s.conn)
					delete(sessions, key)
				}
			}
			smu.Unlock()
		}
	}()

	buf := make([]byte, 65536)
	for {
		n, addr, err := pc.ReadFromUDP(buf)
		if err != nil {
			smu.Lock()
			for _, s := range sessions {
				s.rc.Close()
				r.unregister(s.conn)
			}
			smu.Unlock()
			return
		}

		key := addr.String()
		smu.Lock()
		s := sessions[key]
		if s == nil {
			proto := detectProtocol(buf[:n])
			if proto == ProtoUnknown || !allowedFor(p, proto) {
				smu.Unlock()
				r.logRecord(p, "udp", proto, addr.IP.String(), "deny_protocol", p.Target)
				continue
			}
			user, group, action := r.canUse(p)
			if action != "allow" {
				smu.Unlock()
				r.logRecord(p, "udp", proto, addr.IP.String(), action, p.Target)
				continue
			}
			down, err := r.rm.dialAddr(p)
			if err != nil {
				smu.Unlock()
				r.logRecord(p, "udp", proto, addr.IP.String(), "deny_unbound", p.Target)
				continue
			}
			rc, err := net.Dial("udp", down)
			if err != nil {
				smu.Unlock()
				r.logRecord(p, "udp", proto, addr.IP.String(), "deny_target", p.Target)
				continue
			}
			r.logRecord(p, "udp", proto, addr.IP.String(), "allow", p.Target)
			conn := r.register(p, user, group, "udp", func() { rc.Close() })
			s = &udpSession{
				rc:         rc.(*net.UDPConn),
				clientAddr: addr,
				bucket:     r.bucket(user.ID, group.BandwidthMbps),
				conn:       conn,
			}
			s.last.Store(time.Now().UnixNano())
			sessions[key] = s
			go s.readLoop(pc)
		}

		if s.conn.killed.Load() {
			s.rc.Close()
			r.unregister(s.conn)
			delete(sessions, key)
			smu.Unlock()
			continue
		}
		s.last.Store(time.Now().UnixNano())
		smu.Unlock()

		if s.bucket != nil {
			s.bucket.Wait(n)
		}
		s.conn.upBytes.Add(int64(n))
		if _, err := s.rc.Write(buf[:n]); err != nil {
			s.rc.Close()
			r.unregister(s.conn)
			smu.Lock()
			delete(sessions, key)
			smu.Unlock()
		}
	}
}

func (s *udpSession) readLoop(pc *net.UDPConn) {
	buf := make([]byte, 65536)
	for {
		n, err := s.rc.Read(buf)
		if err != nil {
			return
		}
		if s.conn.killed.Load() {
			return
		}
		if s.bucket != nil {
			s.bucket.Wait(n)
		}
		s.conn.downBytes.Add(int64(n))
		pc.WriteToUDP(buf[:n], s.clientAddr)
	}
}

// flush 周期刷写流量统计并执行配额/健康检查。
func (r *Relay) flush() {
	r.mu.Lock()
	conns := make([]*activeConn, 0, len(r.conns))
	for _, c := range r.conns {
		conns = append(conns, c)
	}
	r.mu.Unlock()

	dirty := false
	for _, c := range conns {
		up := c.upBytes.Swap(0)
		down := c.downBytes.Swap(0)
		if up+down > 0 {
			r.store.AddTraffic(c.portID, c.userID, c.groupID, up+down)
			dirty = true
		}
	}
	if dirty {
		_ = r.store.saveDB()
	}

	// 配额按用户单独计算：超限后仅断开该用户的活跃连接。
	for _, u := range r.store.Users() {
		g := r.store.GroupByID(u.GroupID)
		if g == nil || g.QuotaBytes() <= 0 || u.TotalBytes < g.QuotaBytes() {
			continue
		}
		for _, c := range conns {
			if c.userID == u.ID && !c.killed.Load() {
				c.killed.Store(true)
				if c.closeFn != nil {
					c.closeFn()
				}
			}
		}
	}
}
