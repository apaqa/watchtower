// WatchTower 监控平台入口
// 在单个二进制文件中同时启动摄入服务、仪表板服务和采集代理
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apaqa/watchtower/internal/agent"
	"github.com/apaqa/watchtower/internal/dashboard"
	"github.com/apaqa/watchtower/internal/ingest"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const (
	ingestAddr    = ":9090"
	dashboardAddr = ":8080"
	agentInterval = 5 * time.Second
)

func main() {
	// ── 1. 初始化时间序列数据库（带磁盘持久化）────────────────────────────────────
	db, err := tsdb.NewWithStorage("watchtower-data")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TSDB 初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer db.Stop()

	// ── 2. 启动摄入服务（HTTP POST /api/v1/metrics + GET /api/v1/query）──────
	ingestSrv, err := ingest.New(ingestAddr, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "摄入服务启动失败: %v\n", err)
		os.Exit(1)
	}
	go func() {
		if err := ingestSrv.Start(); err != nil {
			// http.ErrServerClosed 是正常关闭，忽略
		}
	}()

	// ── 3. 启动仪表板服务（Web UI + WebSocket）────────────────────────────────
	dashSrv, err := dashboard.New(dashboardAddr, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "仪表板服务启动失败: %v\n", err)
		os.Exit(1)
	}
	go func() {
		if err := dashSrv.Start(); err != nil {
			// 正常关闭时忽略错误
		}
	}()

	// ── 4. 稍等片刻确保摄入服务已就绪，再启动代理 ────────────────────────────
	time.Sleep(100 * time.Millisecond)

	ingestURL := "http://localhost" + ingestAddr + "/api/v1/metrics"
	ag, err := agent.New(ingestURL, agentInterval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "采集代理初始化失败: %v\n", err)
		os.Exit(1)
	}
	go ag.Start()

	// ── 5. 打印启动信息 ──────────────────────────────────────────────────────
	fmt.Println("WatchTower started — Dashboard: http://localhost:8080")
	fmt.Println("摄入端点: http://localhost:9090/api/v1/metrics")
	fmt.Println("WQL 查询: http://localhost:9090/api/v1/query?q=avg(cpu_usage_percent[5m])")
	fmt.Println("数据目录: watchtower-data/chunks/")
	fmt.Println("按 Ctrl+C 退出")

	// ── 6. 等待终止信号，优雅关闭 ─────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("\n正在关闭 WatchTower…")
	ag.Stop()
	_ = dashSrv.Close()
	_ = ingestSrv.Close()
	fmt.Println("已安全退出")
}
