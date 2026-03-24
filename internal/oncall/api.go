// Package oncall — HTTP API 路由注册与请求处理
package oncall

import (
	"encoding/json"
	"net/http"
)

// RegisterRoutes 在 ServeMux 上注册值班排班 API 路由
func RegisterRoutes(mux *http.ServeMux, s *Scheduler) {
	mux.HandleFunc("/api/v1/oncall", s.handleCurrent)          // GET 当前值班人
	mux.HandleFunc("/api/v1/oncall/schedule", s.handleSchedule) // GET/POST 排班表
}

// setCORSHeaders 设置 CORS 响应头（允许仪表板跨域调用）
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

// handleCurrent 处理 GET /api/v1/oncall — 返回当前值班人员信息
func (s *Scheduler) handleCurrent(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"仅支持 GET 方法"}`, http.StatusMethodNotAllowed)
		return
	}
	json.NewEncoder(w).Encode(s.CurrentOnCall())
}

// handleSchedule 处理 GET/POST /api/v1/oncall/schedule
func (s *Scheduler) handleSchedule(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		sc := s.GetSchedule()
		if sc == nil {
			sc = &OnCallSchedule{Rotation: []OnCallSlot{}}
		}
		json.NewEncoder(w).Encode(sc)

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var sc OnCallSchedule
		if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
			http.Error(w, `{"error":"请求体解析失败"}`, http.StatusBadRequest)
			return
		}
		if err := s.SetSchedule(&sc); err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(s.GetSchedule())

	default:
		http.Error(w, `{"error":"仅支持 GET 和 POST 方法"}`, http.StatusMethodNotAllowed)
	}
}
