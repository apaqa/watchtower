// Package agent - 日志采集代理：生成合成系统事件日志并推送到摄入服务
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// syntheticMessages 是合成日志消息模板（模拟真实系统事件）
var syntheticMessages = []struct {
	level   model.LogLevel
	message string
}{
	{model.LogLevelInfo, "System health check passed"},
	{model.LogLevelInfo, "Scheduled backup completed successfully"},
	{model.LogLevelInfo, "Configuration reloaded"},
	{model.LogLevelInfo, "Connection pool initialized: 10 connections"},
	{model.LogLevelInfo, "Disk cleanup completed: freed 2.3 GB"},
	{model.LogLevelWarn, "CPU spike detected: usage above 80%"},
	{model.LogLevelWarn, "Memory pressure: swap activated"},
	{model.LogLevelWarn, "Slow query detected: execution time 2.1s"},
	{model.LogLevelWarn, "Retry attempt 2/3 for upstream service"},
	{model.LogLevelWarn, "Cache hit rate dropped to 65%"},
	{model.LogLevelError, "Failed to connect to upstream service: timeout"},
	{model.LogLevelError, "Disk I/O error: read failure"},
	{model.LogLevelError, "Out of memory: process killed"},
	{model.LogLevelDebug, "Received heartbeat from peer node"},
	{model.LogLevelDebug, "GC cycle completed: 45ms pause"},
}

// LogAgent 定期生成合成系统日志并 POST 到摄入服务
type LogAgent struct {
	logURL   string        // 日志单条摄入端点，如 http://localhost:9090/api/v1/logs/single
	hostname string        // 主机名，用作日志来源
	interval time.Duration // 生成间隔
	client   *http.Client
	stopCh   chan struct{}
}

// NewLogAgent 创建日志采集代理；logURL 为摄入端点，interval 为生成周期
func NewLogAgent(logURL string, interval time.Duration) *LogAgent {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return &LogAgent{
		logURL:   logURL,
		hostname: hostname,
		interval: interval,
		client:   &http.Client{Timeout: 5 * time.Second},
		stopCh:   make(chan struct{}),
	}
}

// Start 在后台 goroutine 中启动日志生成循环
func (a *LogAgent) Start() {
	go a.run()
}

// Stop 停止日志生成循环
func (a *LogAgent) Stop() {
	close(a.stopCh)
}

// run 立即生成一条日志，然后按 interval 定时生成
func (a *LogAgent) run() {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	a.generateAndSend() // 立即发送一条初始日志

	for {
		select {
		case <-ticker.C:
			a.generateAndSend()
		case <-a.stopCh:
			return
		}
	}
}

// generateAndSend 随机选取一条合成日志并发送到摄入端点
func (a *LogAgent) generateAndSend() {
	tmpl := syntheticMessages[rand.Intn(len(syntheticMessages))]
	entry := model.LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     tmpl.level,
		Source:    a.hostname,
		Message:   tmpl.message,
		Labels:    map[string]string{"host": a.hostname},
	}
	if err := a.sendLog(entry); err != nil {
		fmt.Printf("[log-agent] 发送日志失败: %v\n", err)
	}
}

// sendLog 将日志条目序列化为 JSON 并 POST 到摄入端点
func (a *LogAgent) sendLog(entry model.LogEntry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	resp, err := a.client.Post(a.logURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("摄入服务返回错误状态: %d", resp.StatusCode)
	}
	return nil
}
