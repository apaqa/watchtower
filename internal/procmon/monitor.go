// Package procmon 通过 gopsutil 收集进程级指标并将其写入 TSDB。
package procmon

import (
	"fmt"
	"sort"
	"sync"
	"time"

	goproc "github.com/shirou/gopsutil/v3/process"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const (
	// maxProcesses 是 API 和 TSDB 写入保留的最大进程数量
	maxProcesses = 20
	// collectInterval 是采集周期
	collectInterval = 10 * time.Second
)

// ProcessInfo 描述单个进程的运行时快照
type ProcessInfo struct {
	PID           int32   `json:"pid"`
	Name          string  `json:"name"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryMB      float64 `json:"memory_mb"`
	MemoryPercent float64 `json:"memory_percent"`
	Status        string  `json:"status"`    // running/sleeping/zombie
	StartedAt     int64   `json:"started_at"` // Unix 毫秒
	CommandLine   string  `json:"command_line"`
	User          string  `json:"user"`
}

// Monitor 定期采集系统进程信息，保存快照并写入 TSDB
type Monitor struct {
	mu       sync.RWMutex
	procs    []ProcessInfo // 最近一次采集结果（按 CPU 降序）
	db       *tsdb.TSDB
	stopCh   chan struct{}
}

// New 创建进程监控器实例（db 可为 nil 以禁止 TSDB 写入）
func New(db *tsdb.TSDB) *Monitor {
	return &Monitor{
		db:     db,
		stopCh: make(chan struct{}),
	}
}

// Start 在后台 goroutine 中启动定期采集循环
func (m *Monitor) Start() {
	go m.collectLoop()
}

// Stop 停止后台采集循环
func (m *Monitor) Stop() {
	close(m.stopCh)
}

// GetProcesses 返回最新进程快照。
// sort 为 "memory" 时按内存降序排列，否则按 CPU 降序。
func (m *Monitor) GetProcesses(sortBy string) []ProcessInfo {
	m.mu.RLock()
	out := make([]ProcessInfo, len(m.procs))
	copy(out, m.procs)
	m.mu.RUnlock()

	if sortBy == "memory" {
		sort.Slice(out, func(i, j int) bool {
			return out[i].MemoryMB > out[j].MemoryMB
		})
	}
	return out
}

// collectLoop 每 collectInterval 执行一次采集
func (m *Monitor) collectLoop() {
	// 启动时立即采集一次
	m.collect()
	ticker := time.NewTicker(collectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.collect()
		case <-m.stopCh:
			return
		}
	}
}

// collect 获取全部进程并筛选出 CPU 占用前 maxProcesses 的快照
func (m *Monitor) collect() {
	procs, err := goproc.Processes()
	if err != nil {
		return
	}

	infos := make([]ProcessInfo, 0, len(procs))
	for _, p := range procs {
		info := buildProcessInfo(p)
		if info == nil {
			continue
		}
		infos = append(infos, *info)
	}

	// 按 CPU 降序排列，取前 maxProcesses 条
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].CPUPercent > infos[j].CPUPercent
	})
	if len(infos) > maxProcesses {
		infos = infos[:maxProcesses]
	}

	m.mu.Lock()
	m.procs = infos
	m.mu.Unlock()

	// 将 CPU 和内存指标写入 TSDB，便于历史查询和告警
	if m.db != nil {
		m.writeMetrics(infos)
	}
}

// buildProcessInfo 从 gopsutil Process 对象构建 ProcessInfo
// 任意字段失败时跳过（不中断整体采集）
func buildProcessInfo(p *goproc.Process) *ProcessInfo {
	name, err := p.Name()
	if err != nil {
		return nil
	}

	cpu, _ := p.CPUPercent()
	memInfo, _ := p.MemoryInfo()
	memPct, _ := p.MemoryPercent()
	statusSlice, _ := p.Status()
	createTime, _ := p.CreateTime()
	cmdline, _ := p.Cmdline()
	user, _ := p.Username()

	var memMB float64
	if memInfo != nil {
		memMB = float64(memInfo.RSS) / 1024 / 1024
	}

	status := "unknown"
	if len(statusSlice) > 0 {
		status = statusSlice[0]
	}

	return &ProcessInfo{
		PID:           p.Pid,
		Name:          name,
		CPUPercent:    cpu,
		MemoryMB:      memMB,
		MemoryPercent: float64(memPct),
		Status:        status,
		StartedAt:     createTime,
		CommandLine:   cmdline,
		User:          user,
	}
}

// writeMetrics 将当前进程快照写入 TSDB
func (m *Monitor) writeMetrics(infos []ProcessInfo) {
	ts := time.Now().UnixMilli()
	points := make([]model.MetricPoint, 0, len(infos)*2)
	for _, info := range infos {
		labels := map[string]string{
			"name": info.Name,
			"pid":  fmt.Sprintf("%d", info.PID),
		}
		points = append(points,
			model.MetricPoint{Name: "process_cpu", Labels: labels, Value: info.CPUPercent, Timestamp: ts},
			model.MetricPoint{Name: "process_memory_mb", Labels: labels, Value: info.MemoryMB, Timestamp: ts},
		)
	}
	m.db.Write(points)
}
