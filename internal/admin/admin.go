// Package admin 提供 WatchTower 管理员 API，包括系统状态查询、
// 手动触发 GC/快照、查看运行配置及热重载。
package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const version = "0.15.0"

// SystemStatus 描述系统当前运行状态快照
type SystemStatus struct {
	UptimeSecs      float64 `json:"uptime_seconds"`
	Version         string  `json:"version"`
	GoVersion       string  `json:"go_version"`
	Goroutines      int     `json:"goroutines"`
	HeapAllocMB     float64 `json:"heap_alloc_mb"`
	TSDBSeriesCount int     `json:"tsdb_series_count"`
	TSDBTotalPoints int     `json:"tsdb_total_points"`
	TimestampMs     int64   `json:"timestamp_ms"`
}

// configView 是 Config 的安全视图，API 密钥已脱敏
type configView struct {
	Server    config.ServerConfig    `json:"server"`
	Agent     config.AgentConfig     `json:"agent"`
	Retention config.RetentionConfig `json:"retention"`
	APIKeys   []apiKeyView           `json:"api_keys"`
}

// apiKeyView 脱敏后的 API 密钥信息
type apiKeyView struct {
	Name        string   `json:"name"`
	Key         string   `json:"key"` // 始终显示为 ***REDACTED***
	Permissions []string `json:"permissions"`
}

// Handler 持有管理员 API 所需的引用
type Handler struct {
	db         *tsdb.TSDB
	cfg        *config.Config
	retention  *tsdb.RetentionEngine
	startTime  time.Time
	configPath string // watchtower.yaml 路径，用于热重载
}

// New 创建 Admin Handler 实例
func New(db *tsdb.TSDB, cfg *config.Config, re *tsdb.RetentionEngine, configPath string) *Handler {
	return &Handler{
		db:         db,
		cfg:        cfg,
		retention:  re,
		startTime:  time.Now(),
		configPath: configPath,
	}
}

// RegisterRoutes 在给定 ServeMux 上注册所有 Admin API 路由
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("/api/v1/admin/status", h.handleStatus)
	mux.HandleFunc("/api/v1/admin/gc", h.handleGC)
	mux.HandleFunc("/api/v1/admin/snapshot", h.handleSnapshot)
	mux.HandleFunc("/api/v1/admin/config", h.handleConfig)
	mux.HandleFunc("/api/v1/admin/reload", h.handleReload)
}

// handleStatus GET /api/v1/admin/status — 返回系统运行状态
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	status := SystemStatus{
		UptimeSecs:      time.Since(h.startTime).Seconds(),
		Version:         version,
		GoVersion:       runtime.Version(),
		Goroutines:      runtime.NumGoroutine(),
		HeapAllocMB:     float64(ms.HeapAlloc) / (1024 * 1024),
		TSDBSeriesCount: h.db.SeriesCount(),
		TSDBTotalPoints: h.db.TotalPoints(),
		TimestampMs:     time.Now().UnixMilli(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleGC POST /api/v1/admin/gc — 强制触发 TSDB GC 并执行 Go runtime GC
func (h *Handler) handleGC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 执行 TSDB 数据清理
	h.db.GC()
	// 触发 Go 运行时 GC，回收内存
	runtime.GC()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "GC completed"})
}

// handleSnapshot POST /api/v1/admin/snapshot — 触发 TSDB 快照（持久化到磁盘）
func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if flushed := h.db.Snapshot(); flushed {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "snapshot written to disk"})
	} else {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "no-op: storage not enabled"})
	}
}

// handleConfig GET /api/v1/admin/config — 返回运行配置，API 密钥已脱敏
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	keys := make([]apiKeyView, len(h.cfg.APIKeys))
	for i, k := range h.cfg.APIKeys {
		keys[i] = apiKeyView{
			Name:        k.Name,
			Key:         "***REDACTED***",
			Permissions: k.Permissions,
		}
	}
	view := configView{
		Server:    h.cfg.Server,
		Agent:     h.cfg.Agent,
		Retention: h.cfg.Retention,
		APIKeys:   keys,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(view)
}

// handleReload POST /api/v1/admin/reload — 重新解析 watchtower.yaml 并热更新内存配置
// 当前版本仅更新 server/agent/retention 字段；动态组件（告警规则、端点等）需重启生效
func (h *Handler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	// 尝试读取并解析配置文件
	newCfg, err := config.Load(h.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在时不报错，仅返回提示
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "ok",
				"message": fmt.Sprintf("config file %s not found, using in-memory defaults", h.configPath),
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 热更新内存中的配置（基础字段；复杂组件如告警/探针需重启）
	h.cfg.Server = newCfg.Server
	h.cfg.Agent = newCfg.Agent
	h.cfg.Retention = newCfg.Retention

	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "config reloaded (server/agent/retention fields updated)",
	})
}
