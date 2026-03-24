// Package health 提供 Kubernetes 风格的健康检查机制
package health

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// HealthStatus 表示健康检查结果状态
type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"   // 完全正常
	StatusDegraded  HealthStatus = "degraded"  // 部分降级但可用
	StatusUnhealthy HealthStatus = "unhealthy" // 不可用
)

// HealthResult 是单次健康检查的结果
type HealthResult struct {
	Status    HealthStatus `json:"status"`
	Message   string       `json:"message"`
	LatencyMs int64        `json:"latency_ms"`
}

// HealthCheck 是所有健康检查组件须实现的接口
type HealthCheck interface {
	Name() string
	Check() HealthResult
}

// FuncCheck 通过函数实现 HealthCheck 接口，方便内联定义检查逻辑
type FuncCheck struct {
	name    string
	checkFn func() HealthResult
}

// NewFuncCheck 创建一个基于函数的健康检查
func NewFuncCheck(name string, fn func() HealthResult) *FuncCheck {
	return &FuncCheck{name: name, checkFn: fn}
}

// Name 返回检查项名称
func (f *FuncCheck) Name() string { return f.name }

// Check 执行检查并返回结果
func (f *FuncCheck) Check() HealthResult { return f.checkFn() }

// CheckEntry 是缓存中一个检查项的快照
type CheckEntry struct {
	Name   string       `json:"name"`
	Result HealthResult `json:"result"`
}

// HealthReport 是所有检查项的聚合报告
type HealthReport struct {
	Status  HealthStatus `json:"status"`   // 整体状态（取最差子状态）
	Checks  []CheckEntry `json:"checks"`
	CheckedAt int64      `json:"checked_at_ms"` // Unix 毫秒时间戳
}

// Manager 定期运行所有注册的健康检查并缓存结果
type Manager struct {
	mu       sync.RWMutex
	checks   []HealthCheck
	cache    []CheckEntry
	overall  HealthStatus
	lastRun  int64 // Unix 毫秒

	stopCh  chan struct{}
	started bool
	nowFn   func() time.Time // 可注入，便于测试
}

// New 创建一个新的健康检查管理器（默认每 15 秒轮询一次）
func New() *Manager {
	return &Manager{
		overall: StatusHealthy,
		stopCh:  make(chan struct{}),
		nowFn:   time.Now,
	}
}

// SetNowFn 注入自定义时间函数（测试专用）
func (m *Manager) SetNowFn(fn func() time.Time) { m.nowFn = fn }

// Register 注册一个健康检查项（必须在 Start 之前调用）
func (m *Manager) Register(c HealthCheck) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checks = append(m.checks, c)
}

// Start 启动后台轮询协程（每 15 秒执行一次所有检查）
func (m *Manager) Start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	// 立即执行一次，确保启动后即有缓存结果
	m.runChecks()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.runChecks()
			case <-m.stopCh:
				return
			}
		}
	}()
}

// Stop 停止后台轮询协程
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		close(m.stopCh)
		m.started = false
	}
}

// runChecks 执行所有检查并更新缓存（并发执行各检查项）
func (m *Manager) runChecks() {
	m.mu.RLock()
	checks := make([]HealthCheck, len(m.checks))
	copy(checks, m.checks)
	m.mu.RUnlock()

	entries := make([]CheckEntry, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(idx int, hc HealthCheck) {
			defer wg.Done()
			start := m.nowFn()
			result := hc.Check()
			if result.LatencyMs == 0 {
				result.LatencyMs = m.nowFn().Sub(start).Milliseconds()
			}
			entries[idx] = CheckEntry{Name: hc.Name(), Result: result}
		}(i, c)
	}
	wg.Wait()

	// 聚合整体状态：取所有检查项中最差状态
	overall := StatusHealthy
	for _, e := range entries {
		switch e.Result.Status {
		case StatusUnhealthy:
			overall = StatusUnhealthy
		case StatusDegraded:
			if overall == StatusHealthy {
				overall = StatusDegraded
			}
		}
	}

	now := m.nowFn().UnixMilli()
	m.mu.Lock()
	m.cache = entries
	m.overall = overall
	m.lastRun = now
	m.mu.Unlock()
}

// Report 返回最新缓存的健康报告
func (m *Manager) Report() HealthReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	checks := make([]CheckEntry, len(m.cache))
	copy(checks, m.cache)
	return HealthReport{
		Status:    m.overall,
		Checks:    checks,
		CheckedAt: m.lastRun,
	}
}

// IsReady 当所有检查均为 healthy 时返回 true（用于 readiness probe）
func (m *Manager) IsReady() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.overall == StatusHealthy
}

// RegisterRoutes 将健康检查 API 路由注册到给定的 ServeMux
func RegisterRoutes(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("/api/v1/health", m.handleHealth)
	mux.HandleFunc("/api/v1/health/live", m.handleLive)
	mux.HandleFunc("/api/v1/health/ready", m.handleReady)
}

// handleHealth 返回完整健康报告（GET /api/v1/health）
func (m *Manager) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"仅支持 GET 方法"}`, http.StatusMethodNotAllowed)
		return
	}
	report := m.Report()
	if report.Status == StatusUnhealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(report)
}

// handleLive Kubernetes liveness probe（进程存活即返回 200）
func (m *Manager) handleLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"alive"}`))
}

// handleReady Kubernetes readiness probe（所有检查健康才返回 200，否则 503）
func (m *Manager) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if m.IsReady() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"not_ready"}`))
	}
}
