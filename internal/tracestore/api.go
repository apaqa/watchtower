// Package tracestore — HTTP API for distributed trace ingestion and retrieval.
package tracestore

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/apaqa/watchtower/internal/model"
)

// RegisterRoutes 在给定的 ServeMux 上注册 trace API 路由。
//
// 路由列表：
//
//	POST /api/v1/traces              — 批量写入 span（按 trace_id 自动分组）
//	GET  /api/v1/traces              — 列出 trace 摘要（支持 service/limit/min_duration 过滤）
//	GET  /api/v1/traces/{trace_id}   — 获取单条完整 trace
func RegisterRoutes(mux *http.ServeMux, ts *Store) {
	// /api/v1/traces（精确匹配）— POST 写入 / GET 列表
	mux.HandleFunc("/api/v1/traces", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		switch r.Method {
		case http.MethodPost:
			handleIngestSpans(w, r, ts)
		case http.MethodGet:
			handleListTraces(w, r, ts)
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		default:
			jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// /api/v1/traces/（前缀匹配）— GET {trace_id}
	mux.HandleFunc("/api/v1/traces/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		traceID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/traces/"), "/")
		if traceID == "" {
			jsonError(w, "missing trace_id", http.StatusBadRequest)
			return
		}
		handleGetTrace(w, ts, traceID)
	})
}

// handleIngestSpans 处理 POST /api/v1/traces — 接受 JSON span 数组，按 trace_id 分组写入存储。
func handleIngestSpans(w http.ResponseWriter, r *http.Request, ts *Store) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var spans []model.Span
	if err := json.NewDecoder(r.Body).Decode(&spans); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(spans) == 0 {
		jsonError(w, "empty span batch", http.StatusBadRequest)
		return
	}
	ts.WriteSpans(spans)
	w.WriteHeader(http.StatusNoContent)
}

// handleListTraces 处理 GET /api/v1/traces — 返回 trace 摘要列表。
// 支持查询参数：service（服务名过滤）、limit（最大数量，默认 50）、min_duration（最小持续时间，毫秒）。
func handleListTraces(w http.ResponseWriter, r *http.Request, ts *Store) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	minDur, _ := strconv.ParseInt(q.Get("min_duration"), 10, 64)

	opts := QueryOptions{
		Service:       q.Get("service"),
		Limit:         limit,
		MinDurationMs: minDur,
	}

	summaries := ts.ListTraces(opts)
	if summaries == nil {
		summaries = []model.TraceSummary{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(summaries)
}

// handleGetTrace 处理 GET /api/v1/traces/{trace_id} — 返回完整 trace（含所有 span）。
func handleGetTrace(w http.ResponseWriter, ts *Store, traceID string) {
	t, ok := ts.GetTrace(traceID)
	if !ok {
		jsonError(w, "trace not found: "+traceID, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(t)
}

// setCORSHeaders 设置允许跨域的响应头。
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

// jsonError 以 JSON 格式返回错误响应。
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
