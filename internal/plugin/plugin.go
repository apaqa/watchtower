// Package plugin 提供可扩展的指标采集插件系统
package plugin

import (
	"fmt"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// PluginStatus 描述插件的运行状态
type PluginStatus string

const (
	StatusRunning PluginStatus = "running" // 正在采集中
	StatusStopped PluginStatus = "stopped" // 已停止
	StatusError   PluginStatus = "error"   // 初始化或采集时发生错误
)

// Plugin 是所有指标采集插件必须实现的接口
type Plugin interface {
	// Name 返回插件唯一名称（英文小写，如 "network"）
	Name() string
	// Version 返回插件版本号字符串（如 "1.0.0"）
	Version() string
	// Init 使用给定配置初始化插件；返回错误时插件标记为 error 状态
	Init(config map[string]string) error
	// Collect 执行一次采集并返回指标数据点切片
	Collect() []model.MetricPoint
	// Interval 返回两次采集之间的时间间隔
	Interval() time.Duration
	// Stop 通知插件释放资源（幂等）
	Stop()
}

// PluginInfo 是插件对外暴露的状态快照
type PluginInfo struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Status       PluginStatus `json:"status"`
	Interval     string       `json:"interval"`      // 格式化的时间间隔，如 "10s"
	LastCount    int          `json:"last_collected_count"` // 上次采集返回的指标数量
	ErrorMessage string       `json:"error_message,omitempty"`
}

// entry 是 Manager 内部维护的单个插件运行时状态
type entry struct {
	mu        sync.Mutex
	plugin    Plugin
	status    PluginStatus
	lastCount int
	errMsg    string
	stopCh    chan struct{}
}

// Manager 管理所有插件的生命周期：注册、启动、停止及状态查询
type Manager struct {
	mu      sync.RWMutex
	entries map[string]*entry
	postFn  func([]model.MetricPoint) // 采集完成后回调，通常直接写入 TSDB
}

// New 创建一个 Manager，postFn 将在每次采集后被调用写入指标
func New(postFn func([]model.MetricPoint)) *Manager {
	return &Manager{
		entries: make(map[string]*entry),
		postFn:  postFn,
	}
}

// Register 注册一个插件（必须在 Start 之前调用）
func (m *Manager) Register(p Plugin) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[p.Name()]; exists {
		return fmt.Errorf("插件 %q 已注册", p.Name())
	}
	m.entries[p.Name()] = &entry{
		plugin: p,
		status: StatusStopped,
		stopCh: make(chan struct{}),
	}
	return nil
}

// StartAll 以给定配置初始化并启动所有已注册插件
// cfgs 是 pluginName → configMap 的映射，不存在则以空 map 初始化
func (m *Manager) StartAll(cfgs map[string]map[string]string) {
	m.mu.RLock()
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	m.mu.RUnlock()

	for _, name := range names {
		cfg := cfgs[name]
		if cfg == nil {
			cfg = map[string]string{}
		}
		m.startPlugin(name, cfg)
	}
}

// startPlugin 初始化并启动单个插件的采集协程
func (m *Manager) startPlugin(name string, cfg map[string]string) {
	m.mu.RLock()
	e, ok := m.entries[name]
	m.mu.RUnlock()
	if !ok {
		return
	}

	e.mu.Lock()
	// 若已在运行，先停止旧协程
	if e.status == StatusRunning {
		select {
		case <-e.stopCh: // 已关闭
		default:
			close(e.stopCh)
		}
		e.stopCh = make(chan struct{})
	}

	if err := e.plugin.Init(cfg); err != nil {
		e.status = StatusError
		e.errMsg = err.Error()
		e.mu.Unlock()
		return
	}
	e.status = StatusRunning
	e.errMsg = ""
	stopCh := e.stopCh
	e.mu.Unlock()

	go m.runLoop(e, stopCh)
}

// runLoop 在独立协程中按 Interval 周期执行插件采集
func (m *Manager) runLoop(e *entry, stopCh chan struct{}) {
	ticker := time.NewTicker(e.plugin.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			points := e.plugin.Collect()
			e.mu.Lock()
			e.lastCount = len(points)
			e.mu.Unlock()
			if len(points) > 0 && m.postFn != nil {
				m.postFn(points)
			}
		case <-stopCh:
			return
		}
	}
}

// StopPlugin 停止指定插件
func (m *Manager) StopPlugin(name string) error {
	m.mu.RLock()
	e, ok := m.entries[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("插件 %q 不存在", name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status == StatusRunning {
		select {
		case <-e.stopCh:
		default:
			close(e.stopCh)
		}
		e.plugin.Stop()
		e.status = StatusStopped
	}
	return nil
}

// StartPlugin 重启已停止的插件
func (m *Manager) StartPlugin(name string) error {
	m.mu.RLock()
	_, ok := m.entries[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("插件 %q 不存在", name)
	}
	// 重置 stopCh 并重新启动
	m.mu.Lock()
	e := m.entries[name]
	e.stopCh = make(chan struct{})
	m.mu.Unlock()

	m.startPlugin(name, map[string]string{})
	return nil
}

// StopAll 停止所有正在运行的插件
func (m *Manager) StopAll() {
	m.mu.RLock()
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	m.mu.RUnlock()

	for _, name := range names {
		m.StopPlugin(name) //nolint:errcheck
	}
}

// List 返回所有插件的状态快照
func (m *Manager) List() []PluginInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]PluginInfo, 0, len(m.entries))
	for _, e := range m.entries {
		e.mu.Lock()
		info := PluginInfo{
			Name:         e.plugin.Name(),
			Version:      e.plugin.Version(),
			Status:       e.status,
			Interval:     e.plugin.Interval().String(),
			LastCount:    e.lastCount,
			ErrorMessage: e.errMsg,
		}
		e.mu.Unlock()
		result = append(result, info)
	}
	return result
}

// Get 返回单个插件的状态（不存在时 ok=false）
func (m *Manager) Get(name string) (PluginInfo, bool) {
	m.mu.RLock()
	e, ok := m.entries[name]
	m.mu.RUnlock()
	if !ok {
		return PluginInfo{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return PluginInfo{
		Name:         e.plugin.Name(),
		Version:      e.plugin.Version(),
		Status:       e.status,
		Interval:     e.plugin.Interval().String(),
		LastCount:    e.lastCount,
		ErrorMessage: e.errMsg,
	}, true
}
