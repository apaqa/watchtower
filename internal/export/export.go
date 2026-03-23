// Package export 提供指标、日志、链路追踪数据的 CSV/JSON 导出接口。
package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tracestore"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// Handler 聚合导出所需的各数据源引用
type Handler struct {
	db         *tsdb.TSDB
	logStore   *logstore.Store
	traceStore *tracestore.Store
}

// New 创建导出处理器实例
func New(db *tsdb.TSDB, ls *logstore.Store, ts *tracestore.Store) *Handler {
	return &Handler{db: db, logStore: ls, traceStore: ts}
}

// RegisterRoutes 在给定的 ServeMux 上注册导出 HTTP 路由
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("/api/v1/export/metrics", h.handleMetrics)
	mux.HandleFunc("/api/v1/export/logs", h.handleLogs)
	mux.HandleFunc("/api/v1/export/traces", h.handleTraces)
}

// parseTimeRange 从请求查询参数解析 start/end Unix 毫秒时间范围
// 默认：end=now，start=end-1h
func parseTimeRange(r *http.Request) (start, end int64) {
	now := time.Now().UnixMilli()
	end = now
	start = now - int64(time.Hour/time.Millisecond)

	if s := r.URL.Query().Get("start"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			start = v
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		if v, err := strconv.ParseInt(e, 10, 64); err == nil {
			end = v
		}
	}
	return start, end
}

// handleMetrics 导出指标数据
// GET /api/v1/export/metrics?format=csv|json&name=<metric>&start=<ms>&end=<ms>
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	metricName := r.URL.Query().Get("name")
	start, end := parseTimeRange(r)

	// 收集所有匹配指标名的序列和数据点
	type row struct {
		Metric    string            `json:"metric"`
		Labels    map[string]string `json:"labels"`
		Timestamp int64             `json:"timestamp"`
		Value     float64           `json:"value"`
	}
	var rows []row

	var names []string
	if metricName != "" {
		names = []string{metricName}
	} else {
		names = h.db.ListMetrics()
	}

	for _, name := range names {
		for _, s := range h.db.GetSeries(name) {
			for _, dp := range s.QueryRange(start, end) {
				rows = append(rows, row{
					Metric:    name,
					Labels:    s.Labels,
					Timestamp: dp.Timestamp,
					Value:     dp.Value,
				})
			}
		}
	}

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=\"metrics.csv\"")
		cw := csv.NewWriter(w)
		cw.Write([]string{"metric", "labels", "timestamp", "value"})
		for _, row := range rows {
			cw.Write([]string{
				row.Metric,
				labelsToString(row.Labels),
				strconv.FormatInt(row.Timestamp, 10),
				strconv.FormatFloat(row.Value, 'f', -1, 64),
			})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\"metrics.json\"")
		json.NewEncoder(w).Encode(rows)
	}
}

// handleLogs 导出日志数据
// GET /api/v1/export/logs?format=csv|json&level=<level>&source=<source>&start=<ms>&end=<ms>
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.logStore == nil {
		http.Error(w, "log store not available", http.StatusServiceUnavailable)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	start, end := parseTimeRange(r)

	entries := h.logStore.Search(logstore.SearchOptions{
		Level:  model.LogLevel(r.URL.Query().Get("level")),
		Source: r.URL.Query().Get("source"),
		Start:  start,
		End:    end,
		Limit:  10000,
	})

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=\"logs.csv\"")
		cw := csv.NewWriter(w)
		cw.Write([]string{"timestamp", "level", "source", "message", "labels"})
		for _, e := range entries {
			cw.Write([]string{
				strconv.FormatInt(e.Timestamp, 10),
				string(e.Level),
				e.Source,
				e.Message,
				labelsToString(e.Labels),
			})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\"logs.json\"")
		json.NewEncoder(w).Encode(entries)
	}
}

// handleTraces 导出链路追踪数据
// GET /api/v1/export/traces?format=csv|json&start=<ms>&end=<ms>
func (h *Handler) handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.traceStore == nil {
		http.Error(w, "trace store not available", http.StatusServiceUnavailable)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	start, end := parseTimeRange(r)

	// 过滤时间范围内的 trace
	allTraces := h.traceStore.AllTraces()
	var filtered []*model.Trace
	for _, t := range allTraces {
		if t.StartTime >= start && t.StartTime <= end {
			filtered = append(filtered, t)
		}
	}

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=\"traces.csv\"")
		cw := csv.NewWriter(w)
		// CSV 展平为 span 级别的行
		cw.Write([]string{"trace_id", "span_id", "parent_span_id", "service_name", "operation_name", "start_time", "duration_ms", "status"})
		for _, t := range filtered {
			for _, sp := range t.Spans {
				cw.Write([]string{
					t.TraceID,
					sp.SpanID,
					sp.ParentSpanID,
					sp.ServiceName,
					sp.OperationName,
					strconv.FormatInt(sp.StartTime, 10),
					strconv.FormatInt(sp.DurationMs, 10),
					sp.Status,
				})
			}
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=\"traces.json\"")
		json.NewEncoder(w).Encode(filtered)
	}
}

// labelsToString 将标签映射转换为 "k1=v1,k2=v2" 格式字符串
func labelsToString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	// 排序以保证输出顺序稳定
	for i := 0; i < len(parts)-1; i++ {
		for j := i + 1; j < len(parts); j++ {
			if parts[i] > parts[j] {
				parts[i], parts[j] = parts[j], parts[i]
			}
		}
	}
	return strings.Join(parts, ",")
}
