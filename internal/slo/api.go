// Package slo — HTTP API：/api/v1/slos CRUD 接口
package slo

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes 将 SLO CRUD API 注册到给定的 ServeMux
func RegisterRoutes(mux *http.ServeMux, store *Store) {
	// POST/GET /api/v1/slos
	mux.HandleFunc("/api/v1/slos", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			statuses := store.ListStatuses()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(statuses)

		case http.MethodPost:
			var slo SLO
			if err := json.NewDecoder(r.Body).Decode(&slo); err != nil {
				http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
				return
			}
			if err := store.Add(slo); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(slo)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// DELETE /api/v1/slos/{name}
	mux.HandleFunc("/api/v1/slos/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/slos/")
		if name == "" {
			http.Error(w, "SLO name required", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := store.Remove(name); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
