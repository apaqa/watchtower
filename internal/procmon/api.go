// Package procmon 的 HTTP API：返回实时进程列表。
package procmon

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 在给定的 ServeMux 上注册进程监控 API 路由
func RegisterRoutes(mux *http.ServeMux, m *Monitor) {
	// GET /api/v1/processes?sort=cpu|memory
	mux.HandleFunc("/api/v1/processes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sortBy := r.URL.Query().Get("sort")
		procs := m.GetProcesses(sortBy)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(procs)
	})
}
