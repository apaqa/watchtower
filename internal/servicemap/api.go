// Package servicemap — HTTP API：GET /api/v1/servicemap 返回服务依赖图
package servicemap

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 将服务地图 API 注册到给定的 ServeMux
func RegisterRoutes(mux *http.ServeMux, b *Builder) {
	mux.HandleFunc("/api/v1/servicemap", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sm := b.Build()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sm)
	})
}
