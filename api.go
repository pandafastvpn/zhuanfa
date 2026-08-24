package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------------- App ----------------

type App struct {
	store    *Store
	sessions *SessionStore
	realm    *RealmManager
	relay    *Relay
	sampler  *systemSampler
	started  time.Time
}

// ---------------- 工具函数 ----------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (a *App) fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"error": msg})
}

func (a *App) ok(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": data})
}

func (a *App) getUser(r *http.Request) *User {
	c, err := r.Cookie("zfs")
	if err != nil {
		return nil
	}
	s := a.sessions.Get(c.Value)
	if s == nil {
		return nil
	}
	return a.store.UserByID(s.UserID)
}

func (a *App) requireUser(w http.ResponseWriter, r *http.Request) *User {
	u := a.getUser(r)
	if u == nil {
		a.fail(w, http.StatusUnauthorized, "未登录或会话已过期")
		return nil
	}
	return u
}

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) *User {
	u := a.requireUser(w, r)
	if u == nil {
		return nil
	}
	if u.Role != "admin" {
		a.fail(w, http.StatusForbidden, "需要管理员权限")
		return nil
	}
	return u
}

func (a *App) setSession(w http.ResponseWriter, userID int) {
	token := a.sessions.Create(userID)
	http.SetCookie(w, &http.Cookie{
		Name:     "zfs",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
}

func (a *App) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "zfs",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func validUsername(name string) bool {
	if len(name) < 3 || len(name) > 32 {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return false
		}
	}
	return true
}

func validTarget(t string) bool {
	h, p, err := net.SplitHostPort(t)
	if err != nil || h == "" {
		return false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	return true
}

func validAllowed(s string) bool {
	switch s {
	case "auto", ProtoSocks5, ProtoWireGuard, ProtoOpenVPN:
		return true
	}
	return false
}

func (a *App) validPortNum(num int) (string, bool) {
	if num < 1 || num > 65535 {
		return "端口号必须在 1-65535 之间", false
	}
	if exist := a.store.PortByNum(num); exist != nil {
		return fmt.Sprintf("端口 %d 已被使用", num), false
	}
	return "", true
}

func rangesOverlap(a, b PortRange) bool {
	return a.Start <= b.End && b.Start <= a.End
}

// validateUserRanges 要求每段端口均有效、同一用户内部不重叠，且不与其他用户重叠。
func (a *App) validateUserRanges(ranges []PortRange, excludeUserID int) (string, bool) {
	for i, r := range ranges {
		if r.Start < 1 || r.End > 65535 || r.Start > r.End {
			return fmt.Sprintf("端口段 %d-%d 无效，端口必须在 1-65535 之间且起始端口不能大于结束端口", r.Start, r.End), false
		}
		for j := 0; j < i; j++ {
			if rangesOverlap(r, ranges[j]) {
				return fmt.Sprintf("端口段 %d-%d 与同一用户的端口段 %d-%d 重叠", r.Start, r.End, ranges[j].Start, ranges[j].End), false
			}
		}
		for _, u := range a.store.Users() {
			if u.ID == excludeUserID {
				continue
			}
			for _, other := range u.Ranges {
				if rangesOverlap(r, other) {
					return fmt.Sprintf("端口段 %d-%d 与用户 %s 的端口段 %d-%d 重叠", r.Start, r.End, u.Username, other.Start, other.End), false
				}
			}
		}
	}
	return "", true
}

// applyPorts 端口规则变化后统一刷新 realm 配置与转发监听器。
func (a *App) applyPorts() {
	if err := a.realm.apply(); err != nil {
		logf("realm 应用配置失败: %v", err)
	}
	a.relay.Reload()
}

// ---------------- 认证 ----------------

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		a.fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	u := a.store.UserByName(req.Username)
	if u == nil || !verifyPassword(req.Password, u.Salt, u.Password) {
		a.fail(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !u.Enabled {
		a.fail(w, http.StatusForbidden, "账户已被禁用")
		return
	}
	if u.Expired() {
		a.fail(w, http.StatusForbidden, "账户已过期")
		return
	}
	a.setSession(w, u.ID)
	a.ok(w, map[string]interface{}{"id": u.ID, "username": u.Username, "role": u.Role})
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	st := a.store.SettingsView()
	if !st.AllowRegister {
		a.fail(w, http.StatusForbidden, "未开放自助注册")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		a.fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !validUsername(req.Username) {
		a.fail(w, http.StatusBadRequest, "用户名需为 3-32 位字母/数字/下划线")
		return
	}
	if len(req.Password) < 6 {
		a.fail(w, http.StatusBadRequest, "密码至少 6 位")
		return
	}
	if a.store.UserByName(req.Username) != nil {
		a.fail(w, http.StatusConflict, "用户名已存在")
		return
	}
	gid := st.DefaultGroupID
	if a.store.GroupByID(gid) == nil {
		a.fail(w, http.StatusBadRequest, "默认用户组不存在，请联系管理员")
		return
	}
	salt, hash := hashPassword(req.Password)
	u := &User{
		ID:        a.store.NextID(),
		Username:  req.Username,
		Salt:      salt,
		Password:  hash,
		Role:      "user",
		GroupID:   gid,
		Enabled:   true,
		Ranges:    []PortRange{},
		CreatedAt: time.Now().Unix(),
	}
	a.store.mu.Lock()
	a.store.db.Users = append(a.store.db.Users, u)
	a.store.mu.Unlock()
	_ = a.store.saveDB()
	a.setSession(w, u.ID)
	a.ok(w, map[string]interface{}{"id": u.ID, "username": u.Username, "role": u.Role})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("zfs"); err == nil {
		a.sessions.Delete(c.Value)
	}
	a.clearSession(w)
	a.ok(w, nil)
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	u := a.getUser(r)
	if u == nil {
		a.fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	g := a.store.GroupByID(u.GroupID)
	a.ok(w, map[string]interface{}{
		"user":           u,
		"group":          g,
		"allow_register": a.store.SettingsView().AllowRegister,
	})
}

// ---------------- 管理后台 ----------------

func (a *App) handleAdminSystemStatus(w http.ResponseWriter, r *http.Request) {
	iface := r.URL.Query().Get("interface")
	a.ok(w, a.sampler.Status(iface, a.relay, a.started))
}

func (a *App) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	st := a.store.SettingsView()
	var totalBytes int64
	groups := a.store.Groups()
	for _, g := range groups {
		totalBytes += g.TotalBytes
	}
	today := a.store.DayStatView(time.Now().Format("2006-01-02"))
	recent := a.store.Records()
	if len(recent) > 20 {
		recent = recent[:20]
	}
	a.ok(w, map[string]interface{}{
		"users":       len(a.store.Users()),
		"groups":      len(groups),
		"ports":       len(a.store.Ports()),
		"total_bytes": totalBytes,
		"today":       today,
		"last7":       a.store.LastDays(7),
		"recent":      recent,
		"realm": map[string]interface{}{
			"mode":    st.ForwardMode,
			"running": a.realm.Running(),
			"bin":     st.RealmBin,
		},
	})
}

func (a *App) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.ok(w, a.store.Users())
	case http.MethodPost:
		var req struct {
			Username string      `json:"username"`
			Password string      `json:"password"`
			GroupID  int         `json:"group_id"`
			Enabled  bool        `json:"enabled"`
			ExpireAt int64       `json:"expire_at"`
			Ranges   []PortRange `json:"ranges"`
			Note     string      `json:"note"`
			Role     string      `json:"role"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if !validUsername(req.Username) {
			a.fail(w, http.StatusBadRequest, "用户名需为 3-32 位字母/数字/下划线")
			return
		}
		if len(req.Password) < 6 {
			a.fail(w, http.StatusBadRequest, "密码至少 6 位")
			return
		}
		if a.store.UserByName(req.Username) != nil {
			a.fail(w, http.StatusConflict, "用户名已存在")
			return
		}
		if a.store.GroupByID(req.GroupID) == nil {
			a.fail(w, http.StatusBadRequest, "用户组不存在")
			return
		}
		if msg, ok := a.validateUserRanges(req.Ranges, 0); !ok {
			a.fail(w, http.StatusBadRequest, msg)
			return
		}
		role := req.Role
		if role != "admin" && role != "user" {
			role = "user"
		}
		salt, hash := hashPassword(req.Password)
		u := &User{
			ID:        a.store.NextID(),
			Username:  req.Username,
			Salt:      salt,
			Password:  hash,
			Role:      role,
			GroupID:   req.GroupID,
			Enabled:   req.Enabled,
			ExpireAt:  req.ExpireAt,
			Ranges:    req.Ranges,
			Note:      req.Note,
			CreatedAt: time.Now().Unix(),
		}
		a.store.mu.Lock()
		a.store.db.Users = append(a.store.db.Users, u)
		a.store.mu.Unlock()
		_ = a.store.saveDB()
		a.ok(w, u)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminUserItem(w http.ResponseWriter, r *http.Request, id int) {
	u := a.store.UserByID(id)
	if u == nil {
		a.fail(w, http.StatusNotFound, "用户不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Username string      `json:"username"`
			Password string      `json:"password"`
			GroupID  int         `json:"group_id"`
			Enabled  bool        `json:"enabled"`
			ExpireAt int64       `json:"expire_at"`
			Ranges   []PortRange `json:"ranges"`
			Note     string      `json:"note"`
			Role     string      `json:"role"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.Username != "" {
			if !validUsername(req.Username) {
				a.fail(w, http.StatusBadRequest, "用户名格式错误")
				return
			}
			if other := a.store.UserByName(req.Username); other != nil && other.ID != id {
				a.fail(w, http.StatusConflict, "用户名已存在")
				return
			}
			u.Username = req.Username
		}
		if req.Password != "" {
			if len(req.Password) < 6 {
				a.fail(w, http.StatusBadRequest, "密码至少 6 位")
				return
			}
			u.Salt, u.Password = hashPassword(req.Password)
		}
		if req.GroupID > 0 {
			if a.store.GroupByID(req.GroupID) == nil {
				a.fail(w, http.StatusBadRequest, "用户组不存在")
				return
			}
			u.GroupID = req.GroupID
		}
		if msg, ok := a.validateUserRanges(req.Ranges, u.ID); !ok {
			a.fail(w, http.StatusBadRequest, msg)
			return
		}
		if req.Role == "admin" || req.Role == "user" {
			u.Role = req.Role
		}
		u.Enabled = req.Enabled
		u.ExpireAt = req.ExpireAt
		u.Ranges = req.Ranges
		u.Note = req.Note
		_ = a.store.saveDB()
		a.ok(w, u)
	case http.MethodDelete:
		if u.ID == a.getUser(r).ID {
			a.fail(w, http.StatusBadRequest, "不能删除当前登录的管理员账户")
			return
		}
		// 删除该用户的所有端口规则
		var removed []*Port
		a.store.mu.Lock()
		var keep []*Port
		for _, p := range a.store.db.Ports {
			if p.UserID == id {
				removed = append(removed, p)
			} else {
				keep = append(keep, p)
			}
		}
		a.store.db.Ports = keep
		var keepUsers []*User
		for _, x := range a.store.db.Users {
			if x.ID != id {
				keepUsers = append(keepUsers, x)
			}
		}
		a.store.db.Users = keepUsers
		a.store.mu.Unlock()
		for _, p := range removed {
			a.realm.freeInternalPort(p.ID)
		}
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, nil)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminUserReset(w http.ResponseWriter, r *http.Request, id int) {
	if !a.relay.ResetUserTraffic(id) {
		a.fail(w, http.StatusNotFound, "用户不存在")
		return
	}
	if err := a.store.saveDB(); err != nil {
		a.fail(w, http.StatusInternalServerError, "保存流量统计失败")
		return
	}
	a.relay.Reload()
	a.ok(w, nil)
}

func (a *App) handleAdminUserPassword(w http.ResponseWriter, r *http.Request, id int) {
	u := a.store.UserByID(id)
	if u == nil {
		a.fail(w, http.StatusNotFound, "用户不存在")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Password) < 6 {
		a.fail(w, http.StatusBadRequest, "密码至少 6 位")
		return
	}
	u.Salt, u.Password = hashPassword(req.Password)
	_ = a.store.saveDB()
	a.ok(w, nil)
}

func (a *App) handleAdminGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.ok(w, a.store.Groups())
	case http.MethodPost:
		var req struct {
			Name          string `json:"name"`
			BandwidthMbps int    `json:"bandwidth_mbps"`
			QuotaGB       int64  `json:"quota_gb"`
			Enabled       bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			a.fail(w, http.StatusBadRequest, "组名称不能为空")
			return
		}
		g := &Group{
			ID:            a.store.NextID(),
			Name:          req.Name,
			BandwidthMbps: req.BandwidthMbps,
			QuotaGB:       req.QuotaGB,
			Enabled:       req.Enabled,
			CreatedAt:     time.Now().Unix(),
		}
		a.store.mu.Lock()
		a.store.db.Groups = append(a.store.db.Groups, g)
		a.store.mu.Unlock()
		_ = a.store.saveDB()
		a.ok(w, g)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminGroupItem(w http.ResponseWriter, r *http.Request, id int) {
	g := a.store.GroupByID(id)
	if g == nil {
		a.fail(w, http.StatusNotFound, "用户组不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Name          string `json:"name"`
			BandwidthMbps int    `json:"bandwidth_mbps"`
			QuotaGB       int64  `json:"quota_gb"`
			Enabled       bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.Name != "" {
			g.Name = req.Name
		}
		g.BandwidthMbps = req.BandwidthMbps
		g.QuotaGB = req.QuotaGB
		g.Enabled = req.Enabled
		_ = a.store.saveDB()
		a.relay.Reload() // 刷新限速桶
		a.ok(w, g)
	case http.MethodDelete:
		for _, u := range a.store.Users() {
			if u.GroupID == id {
				a.fail(w, http.StatusBadRequest, "组内仍有用户，无法删除")
				return
			}
		}
		a.store.mu.Lock()
		var keep []*Group
		for _, x := range a.store.db.Groups {
			if x.ID != id {
				keep = append(keep, x)
			}
		}
		a.store.db.Groups = keep
		a.store.mu.Unlock()
		_ = a.store.saveDB()
		a.ok(w, nil)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminGroupReset(w http.ResponseWriter, r *http.Request, id int) {
	if a.store.GroupByID(id) == nil {
		a.fail(w, http.StatusNotFound, "用户组不存在")
		return
	}
	a.store.ResetGroupTraffic(id)
	_ = a.store.saveDB()
	a.relay.Reload() // 解除配额限制
	a.ok(w, nil)
}

func (a *App) handleAdminPorts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ports := a.store.Ports()
		if q := r.URL.Query().Get("user_id"); q != "" {
			if uid, err := strconv.Atoi(q); err == nil {
				ports = a.store.PortsOfUser(uid)
			}
		}
		a.ok(w, map[string]interface{}{"ports": ports, "users": a.store.Users(), "groups": a.store.Groups()})
	case http.MethodPost:
		var req struct {
			UserID  int    `json:"user_id"`
			Port    int    `json:"port"`
			TCP     bool   `json:"tcp"`
			UDP     bool   `json:"udp"`
			Target  string `json:"target"`
			Allowed string `json:"allowed"`
			Enabled bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if msg, ok := a.validPortNum(req.Port); !ok {
			a.fail(w, http.StatusBadRequest, msg)
			return
		}
		u := a.store.UserByID(req.UserID)
		if u == nil {
			a.fail(w, http.StatusBadRequest, "用户不存在")
			return
		}
		if !u.InRange(req.Port) {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("端口 %d 不在该用户的端口段内", req.Port))
			return
		}
		if !req.TCP && !req.UDP {
			a.fail(w, http.StatusBadRequest, "至少选择 TCP 或 UDP 之一")
			return
		}
		if !validTarget(req.Target) {
			a.fail(w, http.StatusBadRequest, "目标地址格式应为 ip/host:端口")
			return
		}
		if !validAllowed(req.Allowed) {
			a.fail(w, http.StatusBadRequest, "协议设置无效")
			return
		}
		p := &Port{
			ID:        a.store.NextID(),
			UserID:    req.UserID,
			Port:      req.Port,
			TCP:       req.TCP,
			UDP:       req.UDP,
			Target:    req.Target,
			Allowed:   req.Allowed,
			Enabled:   req.Enabled,
			CreatedAt: time.Now().Unix(),
		}
		a.store.mu.Lock()
		a.store.db.Ports = append(a.store.db.Ports, p)
		a.store.mu.Unlock()
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, p)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminPortItem(w http.ResponseWriter, r *http.Request, id int) {
	p := a.store.PortByID(id)
	if p == nil {
		a.fail(w, http.StatusNotFound, "端口规则不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req struct {
			UserID  int    `json:"user_id"`
			Port    int    `json:"port"`
			TCP     bool   `json:"tcp"`
			UDP     bool   `json:"udp"`
			Target  string `json:"target"`
			Allowed string `json:"allowed"`
			Enabled bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.Port > 0 && req.Port != p.Port {
			if msg, ok := a.validPortNum(req.Port); !ok {
				a.fail(w, http.StatusBadRequest, msg)
				return
			}
			p.Port = req.Port
		}
		if req.UserID > 0 && req.UserID != p.UserID {
			u := a.store.UserByID(req.UserID)
			if u == nil {
				a.fail(w, http.StatusBadRequest, "用户不存在")
				return
			}
			if !u.InRange(p.Port) {
				a.fail(w, http.StatusBadRequest, fmt.Sprintf("端口 %d 不在该用户的端口段内", p.Port))
				return
			}
			p.UserID = req.UserID
		}
		if !req.TCP && !req.UDP {
			a.fail(w, http.StatusBadRequest, "至少选择 TCP 或 UDP 之一")
			return
		}
		if req.Target != "" {
			if !validTarget(req.Target) {
				a.fail(w, http.StatusBadRequest, "目标地址格式应为 ip/host:端口")
				return
			}
			p.Target = req.Target
		}
		if validAllowed(req.Allowed) {
			p.Allowed = req.Allowed
		}
		p.TCP = req.TCP
		p.UDP = req.UDP
		p.Enabled = req.Enabled
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, p)
	case http.MethodDelete:
		a.store.mu.Lock()
		var keep []*Port
		for _, x := range a.store.db.Ports {
			if x.ID != id {
				keep = append(keep, x)
			}
		}
		a.store.db.Ports = keep
		a.store.mu.Unlock()
		a.realm.freeInternalPort(id)
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, nil)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	proto := q.Get("proto")
	protocol := q.Get("protocol")
	action := q.Get("action")
	userID := 0
	if v := q.Get("user_id"); v != "" {
		userID, _ = strconv.Atoi(v)
	}
	port := 0
	if v := q.Get("port"); v != "" {
		port, _ = strconv.Atoi(v)
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	out := make([]*Record, 0)
	for _, rec := range a.store.Records() {
		if proto != "" && rec.Proto != proto {
			continue
		}
		if protocol != "" && rec.Protocol != protocol {
			continue
		}
		if action != "" && rec.Action != action {
			continue
		}
		if userID > 0 && rec.UserID != userID {
			continue
		}
		if port > 0 && rec.Port != port {
			continue
		}
		out = append(out, rec)
		if len(out) >= limit {
			break
		}
	}
	a.ok(w, out)
}

func (a *App) handleAdminRecordStats(w http.ResponseWriter, r *http.Request) {
	// 按协议/动作统计（基于全部记录）
	byProtocol := map[string]int{}
	byPort := map[string]int{}
	byAction := map[string]int{}
	for _, rec := range a.store.Records() {
		byAction[rec.Action]++
		key := rec.Protocol + "/" + rec.Action
		byProtocol[key]++
		byPort[fmt.Sprintf("%d/%s", rec.Port, rec.Action)]++
	}
	a.ok(w, map[string]interface{}{
		"by_action":   byAction,
		"by_protocol": byProtocol,
		"by_port":     byPort,
	})
}

func (a *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	st := a.store.SettingsView()
	switch r.Method {
	case http.MethodGet:
		a.ok(w, st)
	case http.MethodPut:
		var req Settings
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.ForwardMode != "realm" && req.ForwardMode != "direct" {
			a.fail(w, http.StatusBadRequest, "转发模式只能是 realm 或 direct")
			return
		}
		if req.DefaultGroupID > 0 && a.store.GroupByID(req.DefaultGroupID) == nil {
			a.fail(w, http.StatusBadRequest, "默认用户组不存在")
			return
		}
		if req.RecordsLimit < 100 || req.RecordsLimit > 20000 {
			req.RecordsLimit = 3000
		}
		a.store.mu.Lock()
		a.store.db.Settings = req
		a.store.mu.Unlock()
		_ = a.store.saveDB()
		a.realm.reloadSettings()
		a.applyPorts()
		a.ok(w, req)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminRealmReload(w http.ResponseWriter, r *http.Request) {
	a.applyPorts()
	a.ok(w, map[string]interface{}{"running": a.realm.Running()})
}

// ---------------- 用户中心 ----------------

func (a *App) handleUserPorts(w http.ResponseWriter, r *http.Request, me *User) {
	switch r.Method {
	case http.MethodGet:
		allPorts := a.store.PortsOfUser(me.ID)
		page := 1
		if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 0 {
			page = n
		}
		pageSize := 20
		if raw := r.URL.Query().Get("page_size"); raw == "all" {
			pageSize = len(allPorts)
		} else if n, err := strconv.Atoi(raw); err == nil && (n == 20 || n == 50 || n == 100) {
			pageSize = n
		}
		total := len(allPorts)
		totalPages := 1
		if pageSize > 0 {
			totalPages = (total + pageSize - 1) / pageSize
			if totalPages < 1 {
				totalPages = 1
			}
		}
		if page > totalPages {
			page = totalPages
		}
		start, end := 0, total
		if pageSize > 0 {
			start = (page - 1) * pageSize
			end = start + pageSize
			if end > total {
				end = total
			}
		}
		ports := allPorts[start:end]
		// 生成可用的空闲端口建议（每个端口段最多 50 个）
		used := map[int]bool{}
		for _, p := range allPorts {
			used[p.Port] = true
		}
		var free []int
		for _, rg := range me.Ranges {
			cnt := 0
			for port := rg.Start; port <= rg.End && cnt < 50; port++ {
				if !used[port] {
					free = append(free, port)
					cnt++
				}
			}
		}
		a.ok(w, map[string]interface{}{
			"ports":       ports,
			"ranges":      me.Ranges,
			"free":        free,
			"total":       total,
			"page":        page,
			"page_size":   pageSize,
			"total_pages": totalPages,
		})
	case http.MethodPost:
		var req struct {
			Port    int    `json:"port"`
			TCP     bool   `json:"tcp"`
			UDP     bool   `json:"udp"`
			Target  string `json:"target"`
			Allowed string `json:"allowed"`
			Enabled bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if msg, ok := a.validPortNum(req.Port); !ok {
			a.fail(w, http.StatusBadRequest, msg)
			return
		}
		if !me.InRange(req.Port) {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("端口 %d 不在您的端口段内", req.Port))
			return
		}
		if !req.TCP && !req.UDP {
			a.fail(w, http.StatusBadRequest, "至少选择 TCP 或 UDP 之一")
			return
		}
		if !validTarget(req.Target) {
			a.fail(w, http.StatusBadRequest, "目标地址格式应为 ip/host:端口")
			return
		}
		if !validAllowed(req.Allowed) {
			a.fail(w, http.StatusBadRequest, "协议设置无效")
			return
		}
		p := &Port{
			ID:        a.store.NextID(),
			UserID:    me.ID,
			Port:      req.Port,
			TCP:       req.TCP,
			UDP:       req.UDP,
			Target:    req.Target,
			Allowed:   req.Allowed,
			Enabled:   true,
			CreatedAt: time.Now().Unix(),
		}
		a.store.mu.Lock()
		a.store.db.Ports = append(a.store.db.Ports, p)
		a.store.mu.Unlock()
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, p)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleUserPortsBatch(w http.ResponseWriter, r *http.Request, me *User) {
	var req struct {
		Rules []struct {
			Port    int    `json:"port"`
			TCP     bool   `json:"tcp"`
			UDP     bool   `json:"udp"`
			Target  string `json:"target"`
			Allowed string `json:"allowed"`
			Enabled bool   `json:"enabled"`
		} `json:"rules"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Rules) == 0 || len(req.Rules) > 200 {
		a.fail(w, http.StatusBadRequest, "请求格式错误：一次最多添加 200 条规则")
		return
	}
	seen := map[int]bool{}
	for i, rule := range req.Rules {
		if rule.Port < 1 || rule.Port > 65535 {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：端口号必须在 1-65535 之间", i+1))
			return
		}
		if seen[rule.Port] {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：端口 %d 在本次批量添加中重复", i+1, rule.Port))
			return
		}
		seen[rule.Port] = true
		if a.store.PortByNum(rule.Port) != nil {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：端口 %d 已被使用", i+1, rule.Port))
			return
		}
		if !me.InRange(rule.Port) {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：端口 %d 不在您的端口段内", i+1, rule.Port))
			return
		}
		if !rule.TCP && !rule.UDP {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：至少选择 TCP 或 UDP 之一", i+1))
			return
		}
		if !validTarget(rule.Target) {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：目标地址格式应为 ip/host:端口", i+1))
			return
		}
		if !validAllowed(rule.Allowed) {
			a.fail(w, http.StatusBadRequest, fmt.Sprintf("第 %d 行：协议设置无效", i+1))
			return
		}
	}
	ports := make([]*Port, 0, len(req.Rules))
	a.store.mu.Lock()
	for _, rule := range req.Rules {
		p := &Port{ID: a.store.NextID(), UserID: me.ID, Port: rule.Port, TCP: rule.TCP, UDP: rule.UDP, Target: rule.Target, Allowed: rule.Allowed, Enabled: rule.Enabled, CreatedAt: time.Now().Unix()}
		a.store.db.Ports = append(a.store.db.Ports, p)
		ports = append(ports, p)
	}
	a.store.mu.Unlock()
	if err := a.store.saveDB(); err != nil {
		a.fail(w, http.StatusInternalServerError, "保存规则失败")
		return
	}
	a.applyPorts()
	a.ok(w, ports)
}

func (a *App) handleUserPortsBatchDelete(w http.ResponseWriter, r *http.Request, me *User) {
	var req struct { IDs []int `json:"ids"` }
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 200 {
		a.fail(w, http.StatusBadRequest, "请求格式错误：请选择要删除的规则")
		return
	}
	ids := map[int]bool{}
	for _, id := range req.IDs {
		p := a.store.PortByID(id)
		if p == nil || p.UserID != me.ID {
			a.fail(w, http.StatusBadRequest, "存在无权删除或不存在的规则")
			return
		}
		ids[id] = true
	}
	a.store.mu.Lock()
	keep := make([]*Port, 0, len(a.store.db.Ports))
	for _, p := range a.store.db.Ports {
		if !ids[p.ID] { keep = append(keep, p) }
	}
	a.store.db.Ports = keep
	a.store.mu.Unlock()
	for id := range ids { a.realm.freeInternalPort(id) }
	if err := a.store.saveDB(); err != nil { a.fail(w, http.StatusInternalServerError, "保存规则失败"); return }
	a.applyPorts()
	a.ok(w, nil)
}

func (a *App) handleUserPortItem(w http.ResponseWriter, r *http.Request, me *User, id int) {
	p := a.store.PortByID(id)
	if p == nil || p.UserID != me.ID {
		a.fail(w, http.StatusNotFound, "端口规则不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req struct {
			TCP     bool   `json:"tcp"`
			UDP     bool   `json:"udp"`
			Target  string `json:"target"`
			Allowed string `json:"allowed"`
			Enabled bool   `json:"enabled"`
		}
		if err := readJSON(r, &req); err != nil {
			a.fail(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if !req.TCP && !req.UDP {
			a.fail(w, http.StatusBadRequest, "至少选择 TCP 或 UDP 之一")
			return
		}
		if req.Target != "" {
			if !validTarget(req.Target) {
				a.fail(w, http.StatusBadRequest, "目标地址格式应为 ip/host:端口")
				return
			}
			p.Target = req.Target
		}
		if validAllowed(req.Allowed) {
			p.Allowed = req.Allowed
		}
		p.TCP = req.TCP
		p.UDP = req.UDP
		p.Enabled = req.Enabled
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, p)
	case http.MethodDelete:
		a.store.mu.Lock()
		var keep []*Port
		for _, x := range a.store.db.Ports {
			if x.ID != id {
				keep = append(keep, x)
			}
		}
		a.store.db.Ports = keep
		a.store.mu.Unlock()
		a.realm.freeInternalPort(id)
		_ = a.store.saveDB()
		a.applyPorts()
		a.ok(w, nil)
	default:
		a.fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleUserRecords(w http.ResponseWriter, r *http.Request, me *User) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	var out []*Record
	for _, rec := range a.store.Records() {
		if rec.UserID == me.ID {
			out = append(out, rec)
			if len(out) >= limit {
				break
			}
		}
	}
	a.ok(w, out)
}

func (a *App) handleUserUsage(w http.ResponseWriter, r *http.Request, me *User) {
	g := a.store.GroupByID(me.GroupID)
	ports := a.store.PortsOfUser(me.ID)
	allocated := 0
	for _, rg := range me.Ranges {
		allocated += rg.End - rg.Start + 1
	}
	a.ok(w, map[string]interface{}{
		"user":            me,
		"group":           g,
		"ports":           ports,
		"allocated_ports": allocated,
		"last7":           a.store.LastDays(7),
		"today":           a.store.DayStatView(time.Now().Format("2006-01-02")),
	})
}

func (a *App) handleUserPassword(w http.ResponseWriter, r *http.Request, me *User) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil {
		a.fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !verifyPassword(req.OldPassword, me.Salt, me.Password) {
		a.fail(w, http.StatusBadRequest, "原密码错误")
		return
	}
	if len(req.NewPassword) < 6 {
		a.fail(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	me.Salt, me.Password = hashPassword(req.NewPassword)
	_ = a.store.saveDB()
	a.ok(w, nil)
}

// ---------------- 路由 ----------------

func (a *App) routeAPI(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	switch p {
	case "/api/login":
		a.handleLogin(w, r)
		return
	case "/api/register":
		a.handleRegister(w, r)
		return
	case "/api/logout":
		a.handleLogout(w, r)
		return
	case "/api/me":
		a.handleMe(w, r)
		return
	}

	if strings.HasPrefix(p, "/api/admin/") {
		if a.requireAdmin(w, r) == nil {
			return
		}
		a.routeAdmin(w, r, strings.TrimPrefix(p, "/api/admin/"))
		return
	}
	if strings.HasPrefix(p, "/api/user/") {
		me := a.requireUser(w, r)
		if me == nil {
			return
		}
		a.routeUser(w, r, strings.TrimPrefix(p, "/api/user/"), me)
		return
	}
	a.fail(w, http.StatusNotFound, "接口不存在")
}

func (a *App) routeAdmin(w http.ResponseWriter, r *http.Request, sub string) {
	switch {
	case sub == "stats":
		a.handleAdminStats(w, r)
	case sub == "system-status":
		a.handleAdminSystemStatus(w, r)
	case sub == "users":
		a.handleAdminUsers(w, r)
	case sub == "records":
		a.handleAdminRecords(w, r)
	case sub == "records/stats":
		a.handleAdminRecordStats(w, r)
	case sub == "settings":
		a.handleAdminSettings(w, r)
	case sub == "realm/reload":
		a.handleAdminRealmReload(w, r)
	case sub == "groups":
		a.handleAdminGroups(w, r)
	case sub == "ports":
		a.handleAdminPorts(w, r)
	case strings.HasPrefix(sub, "users/"):
		rest := strings.TrimPrefix(sub, "users/")
		if strings.HasSuffix(rest, "/reset") {
			id, err := strconv.Atoi(strings.TrimSuffix(rest, "/reset"))
			if err != nil {
				a.fail(w, http.StatusBadRequest, "参数错误")
				return
			}
			a.handleAdminUserReset(w, r, id)
			return
		}
		if strings.HasSuffix(rest, "/password") {
			id, err := strconv.Atoi(strings.TrimSuffix(rest, "/password"))
			if err != nil {
				a.fail(w, http.StatusBadRequest, "参数错误")
				return
			}
			a.handleAdminUserPassword(w, r, id)
			return
		}
		id, err := strconv.Atoi(rest)
		if err != nil {
			a.fail(w, http.StatusBadRequest, "参数错误")
			return
		}
		a.handleAdminUserItem(w, r, id)
	case strings.HasPrefix(sub, "groups/"):
		rest := strings.TrimPrefix(sub, "groups/")
		if strings.HasSuffix(rest, "/reset") {
			id, err := strconv.Atoi(strings.TrimSuffix(rest, "/reset"))
			if err != nil {
				a.fail(w, http.StatusBadRequest, "参数错误")
				return
			}
			a.handleAdminGroupReset(w, r, id)
			return
		}
		id, err := strconv.Atoi(rest)
		if err != nil {
			a.fail(w, http.StatusBadRequest, "参数错误")
			return
		}
		a.handleAdminGroupItem(w, r, id)
	case strings.HasPrefix(sub, "ports/"):
		id, err := strconv.Atoi(strings.TrimPrefix(sub, "ports/"))
		if err != nil {
			a.fail(w, http.StatusBadRequest, "参数错误")
			return
		}
		a.handleAdminPortItem(w, r, id)
	default:
		a.fail(w, http.StatusNotFound, "接口不存在")
	}
}

func (a *App) routeUser(w http.ResponseWriter, r *http.Request, sub string, me *User) {
	switch {
	case sub == "ports":
		a.handleUserPorts(w, r, me)
	case sub == "records":
		a.handleUserRecords(w, r, me)
	case sub == "usage":
		a.handleUserUsage(w, r, me)
	case sub == "password":
		a.handleUserPassword(w, r, me)
	case sub == "ports/batch":
		if r.Method != http.MethodPost { a.fail(w, http.StatusMethodNotAllowed, "method not allowed"); return }
		a.handleUserPortsBatch(w, r, me)
	case sub == "ports/batch-delete":
		if r.Method != http.MethodPost { a.fail(w, http.StatusMethodNotAllowed, "method not allowed"); return }
		a.handleUserPortsBatchDelete(w, r, me)
	case strings.HasPrefix(sub, "ports/"):
		id, err := strconv.Atoi(strings.TrimPrefix(sub, "ports/"))
		if err != nil {
			a.fail(w, http.StatusBadRequest, "参数错误")
			return
		}
		a.handleUserPortItem(w, r, me, id)
	default:
		a.fail(w, http.StatusNotFound, "接口不存在")
	}
}
