// WatchTower 监控平台入口
// 在单个二进制文件中同时启动摄入服务、仪表板服务、采集代理和探针管理器
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apaqa/watchtower/internal/agent"
	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/dashboard"
	"github.com/apaqa/watchtower/internal/ingest"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/tsdb"
)

func main() {
	// ── 1. 加载配置文件（不存在时使用默认值）─────────────────────────────────
	cfg, err := config.Load("watchtower.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置文件加载失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("配置已加载 — 摄入端口: %d，仪表板端口: %d，端点探针: %d 个\n",
		cfg.Server.IngestPort, cfg.Server.DashboardPort, len(cfg.Endpoints))

	// ── 2. 初始化时间序列数据库（带磁盘持久化）──────────────────────────────
	db, err := tsdb.NewWithStorage("watchtower-data")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TSDB 初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer db.Stop()

	// ── 3. 初始化告警引擎，并预加载配置文件中定义的规则 ──────────────────────
	alertEng := alert.NewEngine(db)
	for _, ac := range cfg.Alerts {
		if err := alertEng.AddRule(alert.AlertRule{
			Name:       ac.Name,
			Expression: ac.Expression,
			Operator:   ac.Operator,
			Threshold:  ac.Threshold,
			Duration:   ac.Duration,
			Severity:   ac.Severity,
			WebhookURL: ac.WebhookURL,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "加载告警规则 %q 失败: %v\n", ac.Name, err)
		}
	}
	alertEng.Start()
	defer alertEng.Stop()

	// ── 4. 初始化日志存储 ───────────────────────────────────────────────────
	logStore := logstore.New()
	defer logStore.Stop()

	// ── 5. 初始化探针管理器，并加载配置文件中定义的端点探针 ─────────────────
	probeMgr := probe.NewManager(db)
	for _, ep := range cfg.Endpoints {
		if err := probeMgr.AddProbe(probe.ProbeConfig{
			Name:           ep.Name,
			URL:            ep.URL,
			Method:         ep.Method,
			ExpectedStatus: ep.ExpectedStatus,
			IntervalSecs:   ep.IntervalSeconds,
			TimeoutMs:      ep.TimeoutMs,
			Headers:        ep.Headers,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "加载端点探针 %q 失败: %v\n", ep.Name, err)
		}
	}
	defer probeMgr.Stop()

	// ── 6. 启动摄入服务（指标 + WQL + 告警 API + 日志 API + 探针 API）───────
	ingestSrv, err := ingest.New(cfg.IngestAddr(), db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "摄入服务启动失败: %v\n", err)
		os.Exit(1)
	}
	ingestSrv.RegisterAlertEngine(alertEng)
	ingestSrv.RegisterLogStore(logStore)
	ingestSrv.RegisterProbeManager(probeMgr)
	go func() {
		if err := ingestSrv.Start(); err != nil {
			// http.ErrServerClosed 是正常关闭，忽略
		}
	}()

	// ── 7. 启动仪表板服务（Web UI + WebSocket）───────────────────────────────
	dashSrv, err := dashboard.New(cfg.DashboardAddr(), db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "仪表板服务启动失败: %v\n", err)
		os.Exit(1)
	}
	dashSrv.SetAlertEngine(alertEng)
	dashSrv.SetLogStore(logStore)
	go func() {
		if err := dashSrv.Start(); err != nil {
			// 正常关闭时忽略错误
		}
	}()

	// ── 8. 稍等片刻确保摄入服务已就绪，再启动代理 ────────────────────────
	time.Sleep(100 * time.Millisecond)

	if cfg.Agent.Enabled {
		ingestURL := fmt.Sprintf("http://localhost:%d/api/v1/metrics", cfg.Server.IngestPort)
		ag, err := agent.New(ingestURL, time.Duration(cfg.Agent.IntervalSeconds)*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "采集代理初始化失败: %v\n", err)
			os.Exit(1)
		}
		go ag.Start()
		defer ag.Stop()

		logURL := fmt.Sprintf("http://localhost:%d/api/v1/logs/single", cfg.Server.IngestPort)
		logAg := agent.NewLogAgent(logURL, 10*time.Second)
		logAg.Start()
		defer logAg.Stop()
	}

	// ── 9. 打印启动信息 ──────────────────────────────────────────────────────
	base := fmt.Sprintf("http://localhost:%d", cfg.Server.IngestPort)
	fmt.Printf("WatchTower started — Dashboard: http://localhost:%d\n", cfg.Server.DashboardPort)
	fmt.Printf("摄入端点: %s/api/v1/metrics\n", base)
	fmt.Printf("WQL 查询: %s/api/v1/query?q=avg(cpu_usage_percent[5m])\n", base)
	fmt.Printf("告警 API: %s/api/v1/alerts/rules\n", base)
	fmt.Printf("日志 API: %s/api/v1/logs\n", base)
	fmt.Printf("探针 API: %s/api/v1/probes\n", base)
	fmt.Println("按 Ctrl+C 退出")

	// ── 10. 等待终止信号，优雅关闭 ─────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("\n正在关闭 WatchTower…")
	_ = dashSrv.Close()
	_ = ingestSrv.Close()
	fmt.Println("已安全退出")
}
