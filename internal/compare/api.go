package compare

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 注册指标对比与变化检测 API。
func RegisterRoutes(mux *http.ServeMux, engine *Engine, detector *ChangeDetector) {
	mux.HandleFunc("/api/v1/compare", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		metric := r.URL.Query().Get("metric")
		currentWindow := r.URL.Query().Get("current")
		previousWindow := r.URL.Query().Get("previous")
		if metric == "" || currentWindow == "" || previousWindow == "" {
			http.Error(w, `{"error":"metric, current, and previous parameters are required"}`, http.StatusBadRequest)
			return
		}

		result, err := engine.Compare(metric, currentWindow, previousWindow)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("/api/v1/compare/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		currentWindow := r.URL.Query().Get("current")
		previousWindow := r.URL.Query().Get("previous")
		if currentWindow == "" {
			currentWindow = "1h"
		}
		if previousWindow == "" {
			previousWindow = currentWindow
		}

		report, err := engine.Report(currentWindow, previousWindow)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	})

	mux.HandleFunc("/api/v1/changes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		metric := r.URL.Query().Get("metric")
		changes := detector.GetChangePoints(metric)
		if changes == nil {
			changes = []ChangePoint{}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(changes)
	})
}
