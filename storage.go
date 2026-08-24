package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ---------------- 数据模型 ----------------

type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type User struct {
	ID         int         `json:"id"`
	Username   string      `json:"username"`
	Salt       string      `json:"salt"`
	Password   string      `json:"password"` // PBKDF2-HMAC-SHA256 摘要
	Role       string      `json:"role"`     // admin | user
	GroupID    int         `json:"group_id"`
	Enabled    bool        `json:"enabled"`
	ExpireAt   int64       `json:"expire_at"` // 0 表示永不过期
	Ranges     []PortRange `json:"ranges"`    // 分配给用户的端口段
	Note       string      `json:"note"`
	CreatedAt  int64       `json:"created_at"`
	TotalBytes int64       `json:"total_bytes"`
}

func (u *User) Expired() bool {
	return u.ExpireAt > 0 && time.Now().Unix() > u.ExpireAt
}

func (u *User) InRange(port int) bool {
	for _, r := range u.Ranges {
		if port >= r.Start && port <= r.End {
			return true
		}
	}
	return false
}

type Group struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	BandwidthMbps int    `json:"bandwidth_mbps"` // 组宽带峰值，0 不限
	QuotaGB       int64  `json:"quota_gb"`       // 组总流量配额，0 不限
	Enabled       bool   `json:"enabled"`
	TotalBytes    int64  `json:"total_bytes"`
	CreatedAt     int64  `json:"created_at"`
}

func (g *Group) QuotaBytes() int64 {
	if g == nil || g.QuotaGB <= 0 {
		return 0
	}
	return g.QuotaGB * 1024 * 1024 * 1024
}

type Port struct {
	ID         int    `json:"id"`
	UserID     int    `json:"user_id"`
	Port       int    `json:"port"`
	TCP        bool   `json:"tcp"`
	UDP        bool   `json:"udp"`
	Target     string `json:"target"`  // ip/host:port
	Allowed    string `json:"allowed"` // auto | socks5 | wireguard | openvpn
	Enabled    bool   `json:"enabled"`
	TotalBytes int64  `json:"total_bytes"`
	TotalConns int64  `json:"total_conns"`
	CreatedAt  int64  `json:"created_at"`
}

type Record struct {
	ID       int64  `json:"id"`
	Time     int64  `json:"time"`
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
	Port     int    `json:"port"`
	Proto    string `json:"proto"`    // tcp | udp
	Protocol string `json:"protocol"` // 识别出的协议
	Source   string `json:"source"`   // 来源 IP
	Action   string `json:"action"`   // allow | deny_protocol | deny_quota | deny_disabled | deny_user | deny_group | deny_target | deny_unbound
	Target   string `json:"target"`
}

type DayStat struct {
	Date      string         `json:"date"`
	Bytes     int64          `json:"bytes"`
	Connects  int            `json:"connects"`
	Denies    int            `json:"denies"`
	Protocols map[string]int `json:"protocols"`
}

type Settings struct {
	AllowRegister   bool   `json:"allow_register"`    // 是否开放自助注册
	DefaultGroupID  int    `json:"default_group_id"`  // 自助注册默认加入的组
	RealmBin        string `json:"realm_bin"`         // realm 可执行文件路径
	RealmConfigPath string `json:"realm_config_path"` // realm 配置写入路径
	RealmLogPath    string `json:"realm_log_path"`    // realm 日志路径
	ForwardMode     string `json:"forward_mode"`      // realm | direct
	RecordsLimit    int    `json:"records_limit"`     // 记录保留条数
}

type DB struct {
	Seq        int                 `json:"seq"`
	Users      []*User             `json:"users"`
	Groups     []*Group            `json:"groups"`
	Ports      []*Port             `json:"ports"`
	Settings   Settings            `json:"settings"`
	RealmPorts map[string]int      `json:"realm_ports"` // portID -> realm 内部监听端口
	Daily      map[string]*DayStat `json:"daily"`       // 按天统计
}

// ---------------- 存储 ----------------

type Store struct {
	mu      sync.RWMutex
	dir     string
	db      *DB
	records []*Record
	seq     int64
}

func NewStore(dir string) (*Store, error) {
	s := &Store{
		dir: dir,
		db: &DB{
			Settings: Settings{
				AllowRegister:   false,
				DefaultGroupID:  0,
				RealmBin:        "/usr/local/bin/realm",
				RealmConfigPath: filepath.Join(dir, "realm.toml"),
				RealmLogPath:    filepath.Join(dir, "realm.log"),
				ForwardMode:     "realm",
				RecordsLimit:    3000,
			},
			RealmPorts: map[string]int{},
			Daily:      map[string]*DayStat{},
		},
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := s.loadDB(); err != nil {
		return nil, err
	}
	if err := s.loadRecords(); err != nil {
		return nil, err
	}
	return s, nil
}

// seed 初始化默认数据：默认组 + 管理员账户。
func (s *Store) seed() error {
	if len(s.db.Groups) == 0 {
		g := &Group{
			ID:            s.NextID(),
			Name:          "默认组",
			BandwidthMbps: 0,
			QuotaGB:       0,
			Enabled:       true,
			CreatedAt:     time.Now().Unix(),
		}
		s.db.Groups = append(s.db.Groups, g)
		s.db.Settings.DefaultGroupID = g.ID
	}
	if len(s.db.Users) == 0 {
		salt, hash := hashPassword("admin123")
		u := &User{
			ID:        s.NextID(),
			Username:  "admin",
			Salt:      salt,
			Password:  hash,
			Role:      "admin",
			GroupID:   s.db.Settings.DefaultGroupID,
			Enabled:   true,
			Ranges:    []PortRange{},
			CreatedAt: time.Now().Unix(),
		}
		s.db.Users = append(s.db.Users, u)
	}
	return s.saveDB()
}

func (s *Store) NextID() int {
	s.db.Seq++
	return s.db.Seq
}

func (s *Store) loadDB() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "db.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return s.seed()
		}
		return err
	}
	if err := json.Unmarshal(b, s.db); err != nil {
		return fmt.Errorf("解析 db.json 失败: %w", err)
	}
	if s.db.Settings.RealmConfigPath == "" {
		s.db.Settings.RealmConfigPath = filepath.Join(s.dir, "realm.toml")
	}
	if s.db.Settings.RealmLogPath == "" {
		s.db.Settings.RealmLogPath = filepath.Join(s.dir, "realm.log")
	}
	if s.db.Settings.RecordsLimit <= 0 {
		s.db.Settings.RecordsLimit = 3000
	}
	if s.db.RealmPorts == nil {
		s.db.RealmPorts = map[string]int{}
	}
	if s.db.Daily == nil {
		s.db.Daily = map[string]*DayStat{}
	}
	// 同步 id 自增器
	maxID := s.db.Seq
	for _, u := range s.db.Users {
		if u.ID > maxID {
			maxID = u.ID
		}
	}
	for _, g := range s.db.Groups {
		if g.ID > maxID {
			maxID = g.ID
		}
	}
	for _, p := range s.db.Ports {
		if p.ID > maxID {
			maxID = p.ID
		}
	}
	s.db.Seq = maxID
	return nil
}

func (s *Store) loadRecords() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "records.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(b, &s.records); err != nil {
		return fmt.Errorf("解析 records.json 失败: %w", err)
	}
	for _, r := range s.records {
		if r.ID > s.seq {
			s.seq = r.ID
		}
	}
	return nil
}

// save 原子写入文件。
func save(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) saveDB() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.MarshalIndent(s.db, "", "  ")
	if err != nil {
		return err
	}
	return save(filepath.Join(s.dir, "db.json"), b)
}

func (s *Store) saveRecords() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}
	return save(filepath.Join(s.dir, "records.json"), b)
}

// ---------------- 查询方法 ----------------

func (s *Store) UserByID(id int) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.db.Users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func (s *Store) UserByName(name string) *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.db.Users {
		if u.Username == name {
			return u
		}
	}
	return nil
}

func (s *Store) GroupByID(id int) *Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.db.Groups {
		if g.ID == id {
			return g
		}
	}
	return nil
}

// PortByNum 按端口号查找（端口号全局唯一）。
func (s *Store) PortByNum(num int) *Port {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.db.Ports {
		if p.Port == num {
			return p
		}
	}
	return nil
}

func (s *Store) PortByID(id int) *Port {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.db.Ports {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Store) Ports() []*Port {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Port, 0, len(s.db.Ports))
	out = append(out, s.db.Ports...)
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// HasActivePorts 是否存在至少一条启用且有效的端口规则。
func (s *Store) HasActivePorts() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.db.Ports {
		if p.Enabled && (p.TCP || p.UDP) {
			return true
		}
	}
	return false
}

func (s *Store) PortsOfUser(uid int) []*Port {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Port, 0)
	for _, p := range s.db.Ports {
		if p.UserID == uid {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func (s *Store) Users() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.db.Users))
	out = append(out, s.db.Users...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) Groups() []*Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Group, 0, len(s.db.Groups))
	out = append(out, s.db.Groups...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) SettingsView() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.Settings
}

// DayStat 获取某天的统计（不存在则创建）。
func (s *Store) DayStat(date string) *DayStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.db.Daily[date]
	if !ok {
		st = &DayStat{Date: date, Protocols: map[string]int{}}
		s.db.Daily[date] = st
	}
	// 清理 90 天以前的统计
	if len(s.db.Daily) > 90 {
		for k := range s.db.Daily {
			if k < date[:8] && len(s.db.Daily) > 90 {
				delete(s.db.Daily, k)
			}
		}
	}
	return st
}

// AddRecord 追加一条连接记录（环形缓冲）。
func (s *Store) AddRecord(r *Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	r.ID = s.seq
	s.records = append(s.records, r)
	limit := s.db.Settings.RecordsLimit
	if limit <= 0 {
		limit = 3000
	}
	if len(s.records) > limit {
		s.records = s.records[len(s.records)-limit:]
	}
}

// Records 返回记录（倒序，新的在前）。
func (s *Store) Records() []*Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Record, len(s.records))
	for i, r := range s.records {
		out[len(s.records)-1-i] = r
	}
	return out
}

// AddTraffic 累加流量到端口/用户/组，并写入当日统计。
func (s *Store) AddTraffic(portID, userID, groupID int, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		return
	}
	today := time.Now().Format("2006-01-02")
	st := s.db.Daily[today]
	if st == nil {
		st = &DayStat{Date: today, Protocols: map[string]int{}}
		s.db.Daily[today] = st
	}
	st.Bytes += n
	for _, p := range s.db.Ports {
		if p.ID == portID {
			p.TotalBytes += n
			break
		}
	}
	for _, u := range s.db.Users {
		if u.ID == userID {
			u.TotalBytes += n
			break
		}
	}
	for _, g := range s.db.Groups {
		if g.ID == groupID {
			g.TotalBytes += n
			break
		}
	}
}

func (s *Store) UserTotal(userID int) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.db.Users {
		if u.ID == userID {
			return u.TotalBytes
		}
	}
	return 0
}

// DayStatView 读取某天统计。
func (s *Store) DayStatView(date string) *DayStat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.db.Daily[date]
	if !ok {
		return &DayStat{Date: date, Protocols: map[string]int{}}
	}
	cp := &DayStat{Date: st.Date, Bytes: st.Bytes, Connects: st.Connects, Denies: st.Denies, Protocols: map[string]int{}}
	for k, v := range st.Protocols {
		cp.Protocols[k] = v
	}
	return cp
}

// LastDays 最近 n 天的流量统计（用于图表）。
func (s *Store) LastDays(n int) []*DayStat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*DayStat, 0, n)
	for i := n - 1; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		st, ok := s.db.Daily[d]
		if !ok {
			st = &DayStat{Date: d, Protocols: map[string]int{}}
		}
		out = append(out, st)
	}
	return out
}

// ResetUserTraffic 清零单个用户及其端口的流量。
func (s *Store) ResetUserTraffic(userID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found bool
	for _, u := range s.db.Users {
		if u.ID == userID {
			u.TotalBytes = 0
			found = true
			break
		}
	}
	if !found {
		return false
	}
	for _, p := range s.db.Ports {
		if p.UserID == userID {
			p.TotalBytes = 0
		}
	}
	return true
}

// ResetGroupTraffic 清零组统计及组内所有用户与端口流量。
func (s *Store) ResetGroupTraffic(groupID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.db.Groups {
		if g.ID == groupID {
			g.TotalBytes = 0
		}
	}
	for _, u := range s.db.Users {
		if u.GroupID == groupID {
			u.TotalBytes = 0
		}
	}
	for _, p := range s.db.Ports {
		for _, u := range s.db.Users {
			if u.ID == p.UserID && u.GroupID == groupID {
				p.TotalBytes = 0
			}
		}
	}
}

// RealmInternalPort 查询/分配 realm 内部端口。
func (s *Store) RealmInternalPort(portID int) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.db.RealmPorts[fmt.Sprint(portID)]
	return v, ok
}

func (s *Store) SetRealmInternalPort(portID, internal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.RealmPorts[fmt.Sprint(portID)] = internal
}

func (s *Store) DropRealmInternalPort(portID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.db.RealmPorts, fmt.Sprint(portID))
}

// UsedInternalPorts 返回当前已占用的内部端口集合。
func (s *Store) UsedInternalPorts() map[int]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[int]bool{}
	for _, v := range s.db.RealmPorts {
		out[v] = true
	}
	return out
}

// PruneRealmPorts 删除不在 keep 集合中的内部端口映射。
func (s *Store) PruneRealmPorts(keep map[int]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.db.RealmPorts {
		id := 0
		for _, ch := range k {
			if ch < '0' || ch > '9' {
				id = 0
				break
			}
			id = id*10 + int(ch-'0')
		}
		if id > 0 && !keep[id] {
			delete(s.db.RealmPorts, k)
		}
	}
}

// IncConn 递增端口规则的总连接数。
func (s *Store) IncConn(portID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.db.Ports {
		if p.ID == portID {
			p.TotalConns++
			return
		}
	}
}

// DayEvent 记录某天的连接/拒绝事件并更新协议计数。
func (s *Store) DayEvent(date, protocol string, allow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.db.Daily[date]
	if st == nil {
		st = &DayStat{Date: date, Protocols: map[string]int{}}
		s.db.Daily[date] = st
	}
	if allow {
		st.Connects++
		st.Protocols[protocol]++
	} else {
		st.Denies++
	}
}
