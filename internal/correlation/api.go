// Package correlation 的 HTTP API：Pearson 相关计算接口。
package correlation

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 在给定的 ServeMux 上注册相关分析 API 路由
func RegisterRoutes(mux *http.ServeMux, c *Correlator) {
	// GET /api/v1/correlate?a=<metric>&b=<metric>&window=<window>
	mux.HandleFunc("/api/v1/correlate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a := r.URL.Query().Get("a")
		b := r.URL.Query().Get("b")
		if a == "" || b == "" {
			http.Error(w, "query params 'a' and 'b' are required", http.StatusBadRequest)
			return
		}
		window := r.URL.Query().Get("window")
		result := c.Correlate(a, b, window)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	// GET /api/v1/correlate/auto?metric=<metric>&window=<window>
	mux.HandleFunc("/api/v1/correlate/auto", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		metric := r.URL.Query().Get("metric")
		if metric == "" {
			http.Error(w, "query param 'metric' is required", http.StatusBadRequest)
			return
		}
		window := r.URL.Query().Get("window")
		result := c.AutoCorrelate(metric, window)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}
