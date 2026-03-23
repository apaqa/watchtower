// Package forecast 的 HTTP API：提供指标预测和容量报告端点。
package forecast

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// RegisterRoutes 在给定的 ServeMux 上注册预测与容量规划 API 路由
func RegisterRoutes(mux *http.ServeMux, f *Forecaster) {
	// GET /api/v1/forecast?metric=<name>
	//   → 返回 1h/24h/7d 预测值及趋势
	// GET /api/v1/forecast?metric=<name>&threshold=<value>
	//   → 额外返回指标达到阈值的估算时间
	mux.HandleFunc("/api/v1/forecast", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		metric := r.URL.Query().Get("metric")
		if metric == "" {
			http.Error(w, `{"error":"metric parameter required"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		// 若提供了 threshold 参数则返回 ExhaustionEstimate
		thresholdStr := r.URL.Query().Get("threshold")
		if thresholdStr != "" {
			threshold, err := strconv.ParseFloat(thresholdStr, 64)
			if err != nil {
				http.Error(w, `{"error":"invalid threshold value"}`, http.StatusBadRequest)
				return
			}
			est, err := f.Exhaustion(metric, threshold)
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			json.NewEncoder(w).Encode(est)
			return
		}

		// 否则返回 ForecastResult
		result, err := f.Forecast(metric)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(result)
	})

	// GET /api/v1/capacity
	//   → 返回所有系统指标的容量规划报告
	mux.HandleFunc("/api/v1/capacity", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		report := f.GenerateReport()
		json.NewEncoder(w).Encode(report)
	})
}
