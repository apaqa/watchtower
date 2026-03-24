// Package incident — HTTP API 路由注册与请求处理
package incident

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes 在 ServeMux 上注册事故管理 API 路由
func RegisterRoutes(mux *http.ServeMux, s *Store) {
	mux.HandleFunc("/api/v1/incidents", s.handleList)    // GET list, POST create
	mux.HandleFunc("/api/v1/incidents/", s.handleDetail) // GET/PUT/{id}, PATCH/{id}/resolve, POST/{id}/comment
}

// setCORSHeaders 设置 CORS 响应头（允许仪表板跨域调用）
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

// handleList 处理 GET /api/v1/incidents（列表）和 POST /api/v1/incidents（创建）
func (s *Store) handleList(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// 支持 ?status= 和 ?severity= 过滤
		q := r.URL.Query()
		statusFilter := Status(q.Get("status"))
		sevFilter := Severity(q.Get("severity"))
		list := s.List(statusFilter, sevFilter)
		if list == nil {
			list = []*Incident{}
		}
		json.NewEncoder(w).Encode(list)

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Title         string   `json:"title"`
			Severity      Severity `json:"severity"`
			RelatedAlerts []string `json:"related_alerts"`
			Author        string   `json:"author"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"请求体解析失败"}`, http.StatusBadRequest)
			return
		}
		if body.Author == "" {
			body.Author = "api"
		}
		inc, err := s.Create(body.Title, body.Severity, body.RelatedAlerts, body.Author)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(inc)

	default:
		http.Error(w, `{"error":"仅支持 GET 和 POST 方法"}`, http.StatusMethodNotAllowed)
	}
}

// handleDetail 分发 /api/v1/incidents/{id} 及其子路径的请求
func (s *Store) handleDetail(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 解析路径：/api/v1/incidents/{id}[/{subpath}]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/incidents/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		http.Error(w, `{"error":"缺少事故 ID"}`, http.StatusBadRequest)
		return
	}
	var subpath string
	if len(parts) > 1 {
		subpath = parts[1]
	}

	switch {
	case subpath == "" && r.Method == http.MethodGet:
		// GET /api/v1/incidents/{id} — 获取事故详情
		inc, ok := s.Get(id)
		if !ok {
			http.Error(w, `{"error":"事故不存在"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(inc)

	case subpath == "" && r.Method == http.MethodPut:
		// PUT /api/v1/incidents/{id} — 更新状态/级别/负责人
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Status     Status   `json:"status"`
			Severity   Severity `json:"severity"`
			AssignedTo string   `json:"assigned_to"`
			Author     string   `json:"author"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"请求体解析失败"}`, http.StatusBadRequest)
			return
		}
		if body.Author == "" {
			body.Author = "api"
		}
		inc, err := s.Update(id, body.Status, body.Severity, body.AssignedTo, body.Author)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(inc)

	case subpath == "comment" && r.Method == http.MethodPost:
		// POST /api/v1/incidents/{id}/comment — 添加评论
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Author  string `json:"author"`
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"请求体解析失败"}`, http.StatusBadRequest)
			return
		}
		if body.Author == "" {
			body.Author = "api"
		}
		inc, err := s.AddComment(id, body.Author, body.Message)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(inc)

	case subpath == "resolve" && r.Method == http.MethodPatch:
		// PATCH /api/v1/incidents/{id}/resolve — 解决事故
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Author string `json:"author"`
		}
		json.NewDecoder(r.Body).Decode(&body) // 可选
		if body.Author == "" {
			body.Author = "api"
		}
		inc, err := s.Resolve(id, body.Author)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(inc)

	default:
		http.Error(w, `{"error":"路由未匹配"}`, http.StatusMethodNotAllowed)
	}
}
