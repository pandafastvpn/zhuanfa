package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"
)

func logf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

func main() {
	dataDir := flag.String("data", "./data", "数据目录")
	listen := flag.String("listen", ":8080", "HTTP 监听地址")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	store, err := NewStore(*dataDir)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}

	sessions := NewSessionStore(7 * 24 * time.Hour)

	realm := NewRealmManager(store)
	relay := NewRelay(store, realm)
	app := &App{store: store, sessions: sessions, realm: realm, relay: relay, sampler: newSystemSampler(), started: time.Now()}

	// 先应用 realm 配置（若已启用 realm 模式且二进制存在则启动）
	if err := realm.apply(); err != nil {
		logf("警告: %v（转发将不可用，请检查 realm 安装）", err)
	}

	// 启动流量守卫（协议检测/限速/计费）
	relay.Start()
	logf("流量守卫已启动，当前生效规则: %d 条", len(store.Ports()))

	// 会话定期清理
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			sessions.Cleanup()
		}
	}()

	// realm 健康检查：进程意外退出后自动拉起
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if store.SettingsView().ForwardMode != "realm" {
				continue
			}
			// 没有任何启用的端口规则时 realm 本就无需运行，跳过健康检查，
			// 避免每 30 秒误报一次 "realm 已自动重启"
			if !store.HasActivePorts() {
				continue
			}
			if !realm.Running() {
				if err := realm.apply(); err != nil {
					logf("realm 自动重启失败: %v", err)
				} else {
					logf("realm 已自动重启")
				}
			}
		}
	}()

	addr := *listen
	logf("转发面板已启动: http://127.0.0.1%s (数据目录: %s)", addr, *dataDir)
	if err := http.ListenAndServe(addr, app); err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}
