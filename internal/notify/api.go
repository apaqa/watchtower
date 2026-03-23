// Package notify — HTTP API：GET /api/v1/notifications 返回通知历史
package notify

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 将通知历史 API 注册到给定的 ServeMux
func RegisterRoutes(mux *http.ServeMux, h *History) {
	mux.HandleFunc("/api/v1/notifications", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		records := h.List()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(records)
	})
}
