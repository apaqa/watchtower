// Package probe 实现 HTTP 端点的周期性健康探测、结果存储和状态统计
package probe

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const (
	// maxHistory 是每个探针保留的最大探测结果条数
	maxHistory = 100
)

// ProbeConfig 描述一个 HTTP 端点探针的运行参数
type ProbeConfig struct {
	Name           string            `json:"name"`             // 探针唯一名称
	URL            string            `json:"url"`              // 目标 URL
	Method         string            `json:"method"`           // HTTP 方法（GET/POST 等）
	ExpectedStatus int               `json:"expected_status"`  // 期望 HTTP 状态码
	IntervalSecs   int               `json:"interval_seconds"` // 探测间隔（秒）
	TimeoutMs      int               `json:"timeout_ms"`       // 请求超时（毫秒）
	Headers        map[string]string `json:"headers,omitempty"` // 附加请求头
}

// ProbeResult 记录一次探测的结果
type ProbeResult struct {
	Timestamp  int64  `json:"timestamp"`       // Unix 毫秒
	Status     string `json:"status"`          // "up" 或 "down"
	StatusCode int    `json:"status_code"`     // 实际 HTTP 状态码（0 表示请求失败）
	ResponseMs int64  `json:"response_ms"`     // 响应耗时（毫秒）
	Error      string `json:"error,omitempty"` // 错误信息（仅 down 时非空）
}

// ProbeStatus 是 GET /api/v1/probes 的单条响应记录，包含当前状态和近期历史
type ProbeStatus struct {
	ProbeConfig
	Status        string        `json:"status"`         // 当前状态：up/down/unknown
	ResponseMs    int64         `json:"response_ms"`    // 最近一次响应耗时
	StatusCode    int           `json:"status_code"`    // 最近一次 HTTP 状态码
	UptimePct     float64       `json:"uptime_pct"`     // 近期检查的可用率（%）
	LastCheck     int64         `json:"last_check"`     // 最近一次探测时间戳（Unix 毫秒）
	RecentHistory []ProbeResult `json:"recent_history"` // 最近 20 次结果（升序）
}

// entry 是一个正在运行的探针实例（内部使用）
type entry struct {
	cfg     ProbeConfig
	mu      sync.RWMutex
	history []*ProbeResult // 按时间升序，最旧在前
	stopCh  chan struct{}
}

// addResult 追加一条探测结果并维护最大历史条数（必须在持锁外调用）
func (e *entry) addResult(r *ProbeResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, r)
	if len(e.history) > maxHistory {
		e.history = e.history[len(e.history)-maxHistory:]
	}
}

// getHistory 返回所有历史记录的副本（升序）
func (e *entry) getHistory() []ProbeResult {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]ProbeResult, len(e.history))
	for i, r := range e.history {
		result[i] = *r
	}
	return result
}

// currentStatus 计算当前状态和统计摘要
func (e *entry) currentStatus() ProbeStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	ps := ProbeStatus{ProbeConfig: e.cfg, Status: "unknown"}
	if len(e.history) == 0 {
		ps.RecentHistory = []ProbeResult{}
		return ps
	}

	latest := e.history[len(e.history)-1]
	ps.Status = latest.Status
	ps.ResponseMs = latest.ResponseMs
	ps.StatusCode = latest.StatusCode
	ps.LastCheck = latest.Timestamp

	// 可用率：全量历史中 up 的比例
	up := 0
	for _, r := range e.history {
		if r.Status == "up" {
			up++
		}
	}
	ps.UptimePct = float64(up) / float64(len(e.history)) * 100

	// 近期历史（最新 20 条，升序）供仪表板火花图使用
	start := 0
	if len(e.history) > 20 {
		start = len(e.history) - 20
	}
	recent := e.history[start:]
	ps.RecentHistory = make([]ProbeResult, len(recent))
	for i, r := range recent {
		ps.RecentHistory[i] = *r
	}

	return ps
}

// Manager 管理所有 HTTP 端点探针的生命周期
type Manager struct {
	mu     sync.RWMutex
	probes map[string]*entry
	db     *tsdb.TSDB
}

// NewManager 创建探针管理器
func NewManager(db *tsdb.TSDB) *Manager {
	return &Manager{
		probes: make(map[string]*entry),
		db:     db,
	}
}

// AddProbe 添加并立即启动一个探针；若名称已存在则返回错误
func (m *Manager) AddProbe(cfg ProbeConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("探针名称不能为空")
	}
	if cfg.URL == "" {
		return fmt.Errorf("探针 URL 不能为空")
	}
	// 填充默认值
	if cfg.Method == "" {
		cfg.Method = "GET"
	}
	if cfg.ExpectedStatus == 0 {
		cfg.ExpectedStatus = 200
	}
	if cfg.IntervalSecs <= 0 {
		cfg.IntervalSecs = 30
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = 10000
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.probes[cfg.Name]; exists {
		return fmt.Errorf("探针 %q 已存在", cfg.Name)
	}

	e := &entry{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
	m.probes[cfg.Name] = e
	go m.runProbe(e)
	return nil
}

// RemoveProbe 停止并移除指定名称的探针；若不存在则返回错误
func (m *Manager) RemoveProbe(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.probes[name]
	if !ok {
		return fmt.Errorf("探针 %q 不存在", name)
	}
	close(e.stopCh)
	delete(m.probes, name)
	return nil
}

// GetStatus 返回所有探针的当前状态快照
func (m *Manager) GetStatus() []ProbeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ProbeStatus, 0, len(m.probes))
	for _, e := range m.probes {
		result = append(result, e.currentStatus())
	}
	return result
}

// GetHistory 返回指定探针的完整历史记录；探针不存在时第二返回值为 false
func (m *Manager) GetHistory(name string) ([]ProbeResult, bool) {
	m.mu.RLock()
	e, ok := m.probes[name]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return e.getHistory(), true
}

// Stop 停止所有探针的运行循环（服务关闭时调用）
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.probes {
		close(e.stopCh)
	}
	m.probes = make(map[string]*entry)
}

// runProbe 在独立 goroutine 中驱动探针的周期性执行
func (m *Manager) runProbe(e *entry) {
	ticker := time.NewTicker(time.Duration(e.cfg.IntervalSecs) * time.Second)
	defer ticker.Stop()

	m.execProbe(e) // 启动时立即执行一次

	for {
		select {
		case <-ticker.C:
			m.execProbe(e)
		case <-e.stopCh:
			return
		}
	}
}

// execProbe 执行一次 HTTP 探测，将结果写入历史并推送到 TSDB
func (m *Manager) execProbe(e *entry) {
	result := m.doHTTP(e.cfg)
	e.addResult(result)

	// 将探测结果写入 TSDB，供 WQL 查询和告警引擎使用
	statusVal := 0.0
	if result.Status == "up" {
		statusVal = 1.0
	}
	labels := map[string]string{"name": e.cfg.Name, "url": e.cfg.URL}
	m.db.Write([]model.MetricPoint{
		{
			Name:      "probe_status",
			Labels:    labels,
			Value:     statusVal,
			Timestamp: result.Timestamp,
		},
		{
			Name:      "probe_response_ms",
			Labels:    labels,
			Value:     float64(result.ResponseMs),
			Timestamp: result.Timestamp,
		},
	})
}

// doHTTP 执行一次 HTTP 请求并返回探测结果（核心探测逻辑，供测试直接调用）
func (m *Manager) doHTTP(cfg ProbeConfig) *ProbeResult {
	start := time.Now()
	result := &ProbeResult{Timestamp: start.UnixMilli()}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, nil)
	if err != nil {
		result.Status = "down"
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		result.ResponseMs = time.Since(start).Milliseconds()
		return result
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	// 不自动跟随重定向（遵循原始状态码）
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	result.ResponseMs = time.Since(start).Milliseconds()

	if err != nil {
		result.Status = "down"
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	if resp.StatusCode == cfg.ExpectedStatus {
		result.Status = "up"
	} else {
		result.Status = "down"
		result.Error = fmt.Sprintf("期望状态码 %d，实际 %d", cfg.ExpectedStatus, resp.StatusCode)
	}
	return result
}
