// Package pipeline 的 HTTP API：管理聚合规则的增删查接口。
package pipeline

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes 在给定的 ServeMux 上注册管道规则的 HTTP 路由
func RegisterRoutes(mux *http.ServeMux, p *Pipeline) {
	// 精确匹配：POST（创建规则）/ GET（列出规则）
	mux.HandleFunc("/api/v1/pipeline/rules", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			rules := p.ListRules()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rules)

		case http.MethodPost:
			var rule Rule
			if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := p.AddRule(rule); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(rule)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// 前缀匹配：DELETE /api/v1/pipeline/rules/{name}
	mux.HandleFunc("/api/v1/pipeline/rules/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/pipeline/rules/")
		if name == "" {
			http.Error(w, "rule name required", http.StatusBadRequest)
			return
		}
		if err := p.RemoveRule(name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
