// Package alert - HTTP API：提供告警规则的 CRUD 接口和告警状态查询接口
package alert

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes 将告警相关的 HTTP 路由注册到指定的 ServeMux
//
// 路由列表：
//   POST   /api/v1/alerts/rules         — 创建告警规则
//   GET    /api/v1/alerts/rules         — 列出所有规则及当前状态
//   DELETE /api/v1/alerts/rules/{name}  — 删除指定规则
//   GET    /api/v1/alerts/active        — 列出当前触发中的告警
//   GET    /api/v1/alerts/history       — 查看最近 100 条状态变化记录
func RegisterRoutes(mux *http.ServeMux, engine *Engine) {
	// /api/v1/alerts/rules（不带尾部 /）处理 POST 和 GET
	mux.HandleFunc("/api/v1/alerts/rules", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		switch r.Method {
		case http.MethodGet:
			handleListRules(w, r, engine)
		case http.MethodPost:
			handleCreateRule(w, r, engine)
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		default:
			jsonError(w, "不支持的方法", http.StatusMethodNotAllowed)
		}
	})

	// /api/v1/alerts/rules/ 处理带规则名称的 DELETE 请求
	mux.HandleFunc("/api/v1/alerts/rules/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodDelete {
			jsonError(w, "此路径仅支持 DELETE 方法", http.StatusMethodNotAllowed)
			return
		}
		handleDeleteRule(w, r, engine)
	})

	// 活跃告警列表（仅 GET）
	mux.HandleFunc("/api/v1/alerts/active", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method != http.MethodGet {
			jsonError(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
			return
		}
		handleActiveAlerts(w, r, engine)
	})

	// 告警历史（仅 GET）
	mux.HandleFunc("/api/v1/alerts/history", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method != http.MethodGet {
			jsonError(w, "仅支持 GET 方法", http.StatusMethodNotAllowed)
			return
		}
		handleHistory(w, r, engine)
	})
}

// handleCreateRule 处理 POST /api/v1/alerts/rules
// 请求体为 AlertRule JSON，创建成功返回 201 Created
func handleCreateRule(w http.ResponseWriter, r *http.Request, engine *Engine) {
	var rule AlertRule
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rule); err != nil {
		jsonError(w, "JSON 解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := engine.AddRule(rule); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "name": rule.Name})
}

// handleListRules 处理 GET /api/v1/alerts/rules
// 返回所有规则及其当前告警状态
func handleListRules(w http.ResponseWriter, r *http.Request, engine *Engine) {
	alerts := engine.GetAlerts()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(alerts)
}

// handleDeleteRule 处理 DELETE /api/v1/alerts/rules/{name}
// 从 URL 路径中提取规则名称
func handleDeleteRule(w http.ResponseWriter, r *http.Request, engine *Engine) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/alerts/rules/")
	name = strings.TrimSpace(name)
	if name == "" {
		jsonError(w, "缺少规则名称", http.StatusBadRequest)
		return
	}
	if err := engine.RemoveRule(name); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleActiveAlerts 处理 GET /api/v1/alerts/active
// 返回当前处于 Firing 状态的告警列表
func handleActiveAlerts(w http.ResponseWriter, r *http.Request, engine *Engine) {
	active := engine.GetActiveAlerts()
	if active == nil {
		active = []Alert{} // 避免返回 JSON null
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(active)
}

// handleHistory 处理 GET /api/v1/alerts/history
// 返回最近 100 条告警状态变化记录
func handleHistory(w http.ResponseWriter, r *http.Request, engine *Engine) {
	history := engine.GetHistory()
	if history == nil {
		history = []AlertEvent{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(history)
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// setCORSHeaders 允许跨域访问（仪表板 :8080 调用告警 API :9090 时需要）
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
