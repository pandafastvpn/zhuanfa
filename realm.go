package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// RealmManager 负责生成 realm 配置并管理 realm 子进程。
//
// 数据路径：客户端 -> 本面板(协议检测/限速/计费) -> realm(127.0.0.1:内部端口) -> 目标服务器
type RealmManager struct {
	mu      sync.Mutex
	store   *Store
	bin     string
	cfg     string
	logf    string
	cmd     *exec.Cmd
	done    chan struct{} // 进程退出信号
	exited  bool
	waitErr error
}

func NewRealmManager(store *Store) *RealmManager {
	st := store.SettingsView()
	return &RealmManager{
		store: store,
		bin:   st.RealmBin,
		cfg:   st.RealmConfigPath,
		logf:  st.RealmLogPath,
	}
}

// reloadSettings 刷新配置路径（管理员修改设置后调用）。
func (rm *RealmManager) reloadSettings() {
	st := rm.store.SettingsView()
	rm.mu.Lock()
	rm.bin = st.RealmBin
	rm.cfg = st.RealmConfigPath
	rm.logf = st.RealmLogPath
	rm.mu.Unlock()
}

// internalPort 获取某端口规则对应的 realm 内部监听端口，没有则分配。
func (rm *RealmManager) internalPort(port *Port) (int, error) {
	if p, ok := rm.store.RealmInternalPort(port.ID); ok {
		return p, nil
	}
	used := rm.store.UsedInternalPorts()
	for p := 20000; p <= 29999; p++ {
		if !used[p] {
			rm.store.SetRealmInternalPort(port.ID, p)
			return p, nil
		}
	}
	return 0, fmt.Errorf("realm 内部端口池(20000-29999)已用尽")
}

// freeInternalPort 释放内部端口。
func (rm *RealmManager) freeInternalPort(portID int) {
	rm.store.DropRealmInternalPort(portID)
}

// genConfig 生成 realm TOML 配置。
func (rm *RealmManager) genConfig() string {
	var sb strings.Builder
	sb.WriteString("[log]\n")
	sb.WriteString("level = \"warn\"\n")
	sb.WriteString(fmt.Sprintf("output = %q\n\n", rm.logf))

	ports := rm.store.Ports()
	keep := map[int]bool{}
	for _, p := range ports {
		if !p.Enabled || (p.TCP == false && p.UDP == false) {
			continue
		}
		internal, err := rm.internalPort(p)
		if err != nil {
			continue
		}
		keep[p.ID] = true
		sb.WriteString("[[endpoints]]\n")
		sb.WriteString(fmt.Sprintf("name = \"p%d\"\n", p.Port))
		sb.WriteString(fmt.Sprintf("listen = \"127.0.0.1:%d\"\n", internal))
		sb.WriteString(fmt.Sprintf("remote = %q\n", p.Target))
		if p.TCP && p.UDP {
			sb.WriteString("network = { use_udp = true, udp_timeout = 120 }\n")
		} else if p.UDP {
			sb.WriteString("network = { use_udp = true, no_tcp = true, udp_timeout = 120 }\n")
		}
		sb.WriteString("\n")
	}
	// 回收已删除/停用规则的内部端口
	rm.store.PruneRealmPorts(keep)
	return sb.String()
}

// apply 重新生成配置并重启 realm（规则变更时调用）。
func (rm *RealmManager) apply() error {
	rm.reloadSettings()

	ports := rm.store.Ports()
	active := 0
	for _, p := range ports {
		if p.Enabled && (p.TCP || p.UDP) {
			active++
		}
	}

	rm.stop()

	if active == 0 {
		return nil
	}

	cfg := rm.genConfig()
	if err := os.WriteFile(rm.cfg, []byte(cfg), 0o644); err != nil {
		return fmt.Errorf("写入 realm 配置失败: %w", err)
	}
	return rm.start()
}

// start 启动 realm 子进程。
func (rm *RealmManager) start() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.bin == "" {
		return fmt.Errorf("realm 路径未配置")
	}
	if _, err := os.Stat(rm.bin); err != nil {
		return fmt.Errorf("realm 可执行文件不存在: %s", rm.bin)
	}
	logf, err := os.OpenFile(rm.logf, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开 realm 日志失败: %w", err)
	}
	cmd := exec.Command(rm.bin, "-c", rm.cfg)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = filepath.Dir(rm.cfg)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 realm 失败: %w", err)
	}
	rm.cmd = cmd
	rm.exited = false
	rm.waitErr = nil
	rm.done = make(chan struct{})
	go func() {
		err := cmd.Wait()
		rm.mu.Lock()
		rm.exited = true
		rm.waitErr = err
		rm.mu.Unlock()
		close(rm.done)
	}()
	return nil
}

// stop 停止 realm 子进程（如未运行则直接返回）。
func (rm *RealmManager) stop() {
	rm.mu.Lock()
	cmd := rm.cmd
	done := rm.done
	rm.mu.Unlock()
	if cmd != nil && cmd.Process != nil && !rm.isExited() {
		_ = cmd.Process.Kill()
		if done != nil {
			<-done
		}
	}
	rm.mu.Lock()
	rm.cmd = nil
	rm.done = nil
	rm.mu.Unlock()
}

func (rm *RealmManager) isExited() bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.exited
}

// Running 检查 realm 是否存活。
func (rm *RealmManager) Running() bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.cmd != nil && rm.cmd.Process != nil && !rm.exited
}

// dialAddr 返回某规则的下游地址：
//
//	realm 模式 -> 127.0.0.1:内部端口
//	direct 模式 -> 目标地址
func (rm *RealmManager) dialAddr(p *Port) (string, error) {
	if rm.store.SettingsView().ForwardMode == "direct" {
		return p.Target, nil
	}
	internal, err := rm.internalPort(p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("127.0.0.1:%d", internal), nil
}
