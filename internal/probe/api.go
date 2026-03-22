// Package probe - 探针 HTTP API：提供端点探针的 CRUD 接口和状态查询接口
package probe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// RegisterRoutes 将探针相关的 HTTP 路由注册到指定的 ServeMux
//
// 路由列表：
//
//	GET    /api/v1/probes               — 列出所有探针及当前状态
//	POST   /api/v1/probes               — 运行时添加新探针
//	DELETE /api/v1/probes/{name}        — 移除指定探针
//	GET    /api/v1/probes/{name}/history — 查询指定探针的历史结果
func RegisterRoutes(mux *http.ServeMux, pm *Manager) {
	// /api/v1/probes（精确匹配）— GET 列出 / POST 创建
	mux.HandleFunc("/api/v1/probes", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		switch r.Method {
		case http.MethodGet:
			handleListProbes(w, r, pm)
		case http.MethodPost:
			handleCreateProbe(w, r, pm)
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		default:
			jsonError(w, "不支持的方法", http.StatusMethodNotAllowed)
		}
	})

	// /api/v1/probes/（前缀匹配）— DELETE {name} / GET {name}/history
	mux.HandleFunc("/api/v1/probes/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/probes/")
		rest = strings.Trim(rest, "/")

		if strings.HasSuffix(rest, "/history") {
			// GET /api/v1/probes/{name}/history
			name := strings.TrimSuffix(rest, "/history")
			name = strings.Trim(name, "/")
			if r.Method != http.MethodGet {
				jsonError(w, "此路径仅支持 GET 方法", http.StatusMethodNotAllowed)
				return
			}
			handleProbeHistory(w, r, pm, name)
			return
		}

		if r.Method == http.MethodDelete {
			// DELETE /api/v1/probes/{name}
			handleDeleteProbe(w, r, pm, rest)
			return
		}

		jsonError(w, "未知路径或方法", http.StatusNotFound)
	})
}

// handleListProbes 处理 GET /api/v1/probes
func handleListProbes(w http.ResponseWriter, _ *http.Request, pm *Manager) {
	statuses := pm.GetStatus()
	if statuses == nil {
		statuses = []ProbeStatus{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(statuses)
}

// handleCreateProbe 处理 POST /api/v1/probes
func handleCreateProbe(w http.ResponseWriter, r *http.Request, pm *Manager) {
	var cfg ProbeConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		jsonError(w, "JSON 解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := pm.AddProbe(cfg); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "name": cfg.Name})
}

// handleDeleteProbe 处理 DELETE /api/v1/probes/{name}
func handleDeleteProbe(w http.ResponseWriter, _ *http.Request, pm *Manager, name string) {
	if name == "" {
		jsonError(w, "缺少探针名称", http.StatusBadRequest)
		return
	}
	if err := pm.RemoveProbe(name); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProbeHistory 处理 GET /api/v1/probes/{name}/history
func handleProbeHistory(w http.ResponseWriter, _ *http.Request, pm *Manager, name string) {
	if name == "" {
		jsonError(w, "缺少探针名称", http.StatusBadRequest)
		return
	}
	history, ok := pm.GetHistory(name)
	if !ok {
		jsonError(w, fmt.Sprintf("探针 %q 不存在", name), http.StatusNotFound)
		return
	}
	if history == nil {
		history = []ProbeResult{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(history)
}

// setCORSHeaders 允许跨域访问
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// jsonError 以 JSON 格式返回错误响应
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
