// Package anomaly 的 HTTP API：返回异常事件列表。
package anomaly

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 在给定的 ServeMux 上注册异常检测 API 路由
func RegisterRoutes(mux *http.ServeMux, d *Detector) {
	// GET /api/v1/anomalies?metric=<name>
	mux.HandleFunc("/api/v1/anomalies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		metric := r.URL.Query().Get("metric")
		events := d.GetEvents(metric)
		if events == nil {
			events = []AnomalyEvent{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	})
}
