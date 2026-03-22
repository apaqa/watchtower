// Package agent 负责采集本机系统指标并推送到摄入服务
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// Agent 定期采集系统指标并 POST 到摄入服务
type Agent struct {
	ingestURL string        // 摄入服务 URL，如 http://localhost:9090/api/v1/metrics
	interval  time.Duration // 采集间隔
	hostname  string        // 本机主机名，用作 host 标签
	client    *http.Client
	stopCh    chan struct{}
}

// New 创建采集代理，ingestURL 为摄入端点，interval 为采集周期
func New(ingestURL string, interval time.Duration) (*Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown" // 获取失败时使用默认值
	}

	return &Agent{
		ingestURL: ingestURL,
		interval:  interval,
		hostname:  hostname,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
		stopCh: make(chan struct{}),
	}, nil
}

// Start 启动采集循环（阻塞，应在 goroutine 中调用）
func (a *Agent) Start() {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// 立即采集一次，无需等待第一个 tick
	a.collectAndSend()

	for {
		select {
		case <-ticker.C:
			a.collectAndSend()
		case <-a.stopCh:
			return
		}
	}
}

// Stop 停止采集循环
func (a *Agent) Stop() {
	close(a.stopCh)
}

// Collect 采集当前系统指标并返回数据点切片（供测试调用）
func (a *Agent) Collect() ([]model.MetricPoint, error) {
	now := time.Now().UnixMilli()
	labels := map[string]string{"host": a.hostname}
	points := make([]model.MetricPoint, 0, 4)

	// 采集 CPU 使用率（间隔 200ms 取均值）
	cpuPercents, err := cpu.Percent(200*time.Millisecond, false)
	if err != nil {
		return nil, fmt.Errorf("采集 CPU 指标失败: %w", err)
	}
	if len(cpuPercents) > 0 {
		points = append(points, model.MetricPoint{
			Name:      "cpu_usage_percent",
			Labels:    labels,
			Value:     cpuPercents[0],
			Timestamp: now,
		})
	}

	// 采集内存使用情况
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("采集内存指标失败: %w", err)
	}
	points = append(points,
		model.MetricPoint{
			Name:      "memory_usage_percent",
			Labels:    labels,
			Value:     vmStat.UsedPercent,
			Timestamp: now,
		},
		model.MetricPoint{
			Name:      "memory_used_bytes",
			Labels:    labels,
			Value:     float64(vmStat.Used),
			Timestamp: now,
		},
	)

	// 采集根磁盘使用情况（Windows 使用 C:\，Unix 使用 /）
	diskPath := "/"
	if isWindows() {
		diskPath = "C:\\"
	}
	diskStat, err := disk.Usage(diskPath)
	if err != nil {
		// 磁盘采集失败不中断，记录零值
		diskStat = &disk.UsageStat{}
	}
	points = append(points, model.MetricPoint{
		Name:      "disk_usage_percent",
		Labels:    labels,
		Value:     diskStat.UsedPercent,
		Timestamp: now,
	})

	return points, nil
}

// collectAndSend 采集指标并发送到摄入服务；失败时打印错误但不退出
func (a *Agent) collectAndSend() {
	points, err := a.Collect()
	if err != nil {
		fmt.Printf("[agent] 采集失败: %v\n", err)
		return
	}

	if err := a.send(points); err != nil {
		fmt.Printf("[agent] 发送失败: %v\n", err)
	}
}

// send 将数据点序列化为 JSON 并 POST 到摄入服务
func (a *Agent) send(points []model.MetricPoint) error {
	body, err := json.Marshal(points)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	resp, err := a.client.Post(a.ingestURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("摄入服务返回错误状态: %d", resp.StatusCode)
	}
	return nil
}

// isWindows 检测当前操作系统是否为 Windows
func isWindows() bool {
	// 通过环境变量 SystemRoot 判断（Windows 特有）
	return os.Getenv("SystemRoot") != "" || os.Getenv("WINDIR") != ""
}
