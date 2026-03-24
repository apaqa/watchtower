// Package quota 提供资源配额管理和速率限制功能
package quota

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ResourceType 是资源类型常量
type ResourceType string

const (
	ResourceMetricsPerMinute ResourceType = "metrics_per_minute"  // 每分钟指标写入次数
	ResourceLogsPerMinute    ResourceType = "logs_per_minute"     // 每分钟日志写入次数
	ResourceAPIRequests      ResourceType = "api_requests_per_minute" // 每分钟 API 请求数
	ResourceSeriesCount      ResourceType = "series_count"        // 活跃时间序列数量
	ResourceStorageBytes     ResourceType = "storage_bytes"       // 存储字节数
)

// ResourceQuota 描述单个资源类型的配额及当前使用量
type ResourceQuota struct {
	Resource   ResourceType `json:"resource"`
	Limit      int64        `json:"limit"`       // 0 表示不限制
	Used       int64        `json:"used"`        // 当前使用量
	ResetAt    int64        `json:"reset_at_ms"` // 下次重置时间（Unix 毫秒）
}

// quotaState 是 Manager 内部维护的可变状态
type quotaState struct {
	mu         sync.Mutex
	limit      int64
	used       int64
	lastMinute time.Time // 上次重置的分钟边界
}

// Manager 管理所有资源配额，支持每分钟自动重置
type Manager struct {
	mu     sync.RWMutex
	quotas map[ResourceType]*quotaState
	nowFn  func() time.Time
}

// NewManager 创建一个包含默认配额的 Manager
func NewManager() *Manager {
	m := &Manager{
		quotas: make(map[ResourceType]*quotaState),
		nowFn:  time.Now,
	}
	// 设置默认配额；lastMinute 留零值，首次 Increment 时自动初始化
	defaults := map[ResourceType]int64{
		ResourceMetricsPerMinute: 10000,
		ResourceLogsPerMinute:    5000,
		ResourceAPIRequests:      1000,
		ResourceSeriesCount:      0, // 不限制
		ResourceStorageBytes:     0, // 不限制
	}
	for rt, limit := range defaults {
		m.quotas[rt] = &quotaState{limit: limit}
	}
	return m
}

// SetNowFn 注入自定义时间函数（测试专用）
func (m *Manager) SetNowFn(fn func() time.Time) {
	m.nowFn = fn
}

// Increment 将指定资源的使用量增加 delta。
// 若超出限制（limit > 0 且 used+delta > limit）返回 false，否则返回 true。
func (m *Manager) Increment(rt ResourceType, delta int64) bool {
	m.mu.RLock()
	state, ok := m.quotas[rt]
	m.mu.RUnlock()
	if !ok {
		return true // 未配置配额，默认允许
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	// 若当前分钟窗口与上次不同则重置计数器（支持时间注入测试）
	now := m.nowFn()
	currentMinute := now.Truncate(time.Minute)
	if !currentMinute.Equal(state.lastMinute) {
		state.used = 0
		state.lastMinute = currentMinute
	}

	if state.limit > 0 && state.used+delta > state.limit {
		return false
	}
	state.used += delta
	return true
}

// UpdateLimit 更新指定资源的配额上限（0 表示不限制）
func (m *Manager) UpdateLimit(rt ResourceType, limit int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.quotas[rt]
	if !ok {
		state = &quotaState{lastMinute: m.nowFn().Truncate(time.Minute)}
		m.quotas[rt] = state
	}
	state.mu.Lock()
	state.limit = limit
	state.mu.Unlock()
}

// List 返回所有配额的快照列表
func (m *Manager) List() []ResourceQuota {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := m.nowFn()
	result := make([]ResourceQuota, 0, len(m.quotas))
	for rt, state := range m.quotas {
		state.mu.Lock()
		// 计算下次分钟边界
		nextReset := state.lastMinute.Add(time.Minute).UnixMilli()
		rq := ResourceQuota{
			Resource: rt,
			Limit:    state.limit,
			Used:     state.used,
			ResetAt:  nextReset,
		}
		// 若已进入新分钟窗口，使用量实际为 0
		if !now.Truncate(time.Minute).Equal(state.lastMinute) {
			rq.Used = 0
		}
		state.mu.Unlock()
		result = append(result, rq)
	}
	return result
}

// RegisterRoutes 注册配额 API 路由
func RegisterRoutes(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("/api/v1/quotas", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"仅支持 GET 方法"}`, http.StatusMethodNotAllowed)
			return
		}
		json.NewEncoder(w).Encode(m.List())
	})

	// PUT /api/v1/quotas/{resource_type}
	mux.HandleFunc("/api/v1/quotas/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, `{"error":"仅支持 PUT 方法"}`, http.StatusMethodNotAllowed)
			return
		}

		resource := strings.TrimPrefix(r.URL.Path, "/api/v1/quotas/")
		if resource == "" {
			http.Error(w, `{"error":"缺少资源类型"}`, http.StatusBadRequest)
			return
		}

		var body struct {
			Limit int64 `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusBadRequest)
			return
		}

		m.UpdateLimit(ResourceType(resource), body.Limit)
		w.WriteHeader(http.StatusNoContent)
	})
}
