// Package grafana 实现 Grafana SimpleJSON 数据源协议，
// 使 Grafana 可将 WatchTower 作为数据源直接查询指标时序数据和告警事件。
//
// 支持的端点：
//   GET  /api/grafana/            — 健康检查（Grafana 数据源连接测试）
//   POST /api/grafana/search      — 返回可用指标列表（供 Grafana 目标选择器使用）
//   POST /api/grafana/query       — 查询指标时序数据（返回 SimpleJSON timeserie 格式）
//   POST /api/grafana/annotations — 返回告警事件作为面板注释
//
// Grafana SimpleJSON 规范参考：https://grafana.com/grafana/plugins/grafana-simple-json-datasource/
package grafana

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/apaqa/watchtower/internal/wql"
)

// Handler 持有 Grafana API 所需的 TSDB 和告警引擎引用
type Handler struct {
	db     *tsdb.TSDB
	alerts *alert.Engine // 可为 nil（不使用告警注释时）
}

// New 创建 Grafana SimpleJSON Handler 实例
func New(db *tsdb.TSDB, alerts *alert.Engine) *Handler {
	return &Handler{db: db, alerts: alerts}
}

// RegisterRoutes 在 ServeMux 上注册 Grafana SimpleJSON 路由
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	// 更具体的路由先注册，/api/grafana/ 作为兜底处理健康检查
	mux.HandleFunc("/api/grafana/search", h.handleSearch)
	mux.HandleFunc("/api/grafana/query", h.handleQuery)
	mux.HandleFunc("/api/grafana/annotations", h.handleAnnotations)
	mux.HandleFunc("/api/grafana/", h.handleHealth) // 健康检查（必须最后注册）
}

// ─────────────────────────────────────────────────────────────────────────────
// 请求 / 响应结构体
// ─────────────────────────────────────────────────────────────────────────────

// searchRequest 是 Grafana search 端点的请求体
type searchRequest struct {
	Target string `json:"target"` // 过滤前缀；空字符串表示返回全部
}

// queryRequest 是 Grafana query 端点的请求体
type queryRequest struct {
	Range         grafanaTimeRange `json:"range"`
	Interval      string           `json:"interval"`
	IntervalMs    int64            `json:"intervalMs"`
	Targets       []queryTarget    `json:"targets"`
	MaxDataPoints int              `json:"maxDataPoints"`
}

// grafanaTimeRange 表示 Grafana 查询时间范围
type grafanaTimeRange struct {
	From string `json:"from"` // ISO 8601，如 "2024-01-01T00:00:00.000Z"
	To   string `json:"to"`
}

// queryTarget 是 Grafana 查询请求中的单个目标
type queryTarget struct {
	Target string `json:"target"` // 指标名称或 WQL 表达式
	RefID  string `json:"refId"`
	Type   string `json:"type"` // "timeserie" 或 "table"
}

// TimeSeriesResponse 是 Grafana SimpleJSON timeserie 格式的响应
type TimeSeriesResponse struct {
	Target     string          `json:"target"`
	Datapoints [][]interface{} `json:"datapoints"` // [[value, timestamp_ms], ...]
}

// annotationRequest 是 Grafana annotations 端点的请求体
type annotationRequest struct {
	Range      grafanaTimeRange       `json:"range"`
	Annotation annotationQueryOptions `json:"annotation"`
}

// annotationQueryOptions 描述注释查询选项
type annotationQueryOptions struct {
	Name       string `json:"name"`
	Datasource string `json:"datasource"`
}

// AnnotationResponse 是单条 Grafana 注释
type AnnotationResponse struct {
	Annotation annotationQueryOptions `json:"annotation"`
	Title      string                 `json:"title"`
	Time       int64                  `json:"time"`
	IsRegion   bool                   `json:"isRegion"`
	Tags       []string               `json:"tags"`
	Text       string                 `json:"text,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// 处理器方法
// ─────────────────────────────────────────────────────────────────────────────

// handleHealth GET /api/grafana/ — Grafana 数据源连接测试（返回 200）
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`"ok"`))
}

// handleSearch POST /api/grafana/search — 返回匹配目标前缀的指标名称列表
func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req searchRequest
	// 忽略 JSON 解析错误（空 body 也合法）
	json.NewDecoder(r.Body).Decode(&req)

	all := h.db.ListMetrics()
	sort.Strings(all)

	// 按前缀过滤（不区分大小写）
	prefix := strings.ToLower(req.Target)
	var result []string
	for _, name := range all {
		if prefix == "" || strings.HasPrefix(strings.ToLower(name), prefix) {
			result = append(result, name)
		}
	}
	if result == nil {
		result = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleQuery POST /api/grafana/query — 查询时序数据，返回 SimpleJSON timeserie 格式
func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// 解析时间范围
	from, err := parseGrafanaTime(req.Range.From)
	if err != nil {
		from = time.Now().Add(-1 * time.Hour)
	}
	to, err := parseGrafanaTime(req.Range.To)
	if err != nil {
		to = time.Now()
	}
	startMs := from.UnixMilli()
	endMs := to.UnixMilli()

	var results []TimeSeriesResponse
	for _, target := range req.Targets {
		series := h.queryTarget(target.Target, startMs, endMs)
		results = append(results, series...)
	}

	if results == nil {
		results = []TimeSeriesResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// queryTarget 根据目标名称（指标名或 WQL 表达式）查询时序数据
func (h *Handler) queryTarget(target string, startMs, endMs int64) []TimeSeriesResponse {
	// 优先尝试作为原始指标名称直接查询 TSDB（返回完整时序）
	rawSeries := h.db.GetSeries(target)
	if len(rawSeries) > 0 {
		var results []TimeSeriesResponse
		for _, s := range rawSeries {
			pts := s.QueryRange(startMs, endMs)
			datapoints := make([][]interface{}, len(pts))
			for i, p := range pts {
				datapoints[i] = []interface{}{p.Value, p.Timestamp}
			}
			// 若有标签则在目标名称后附加标签信息
			name := target
			if len(s.Labels) > 0 {
				name = target + "{" + formatLabels(s.Labels) + "}"
			}
			results = append(results, TimeSeriesResponse{
				Target:     name,
				Datapoints: datapoints,
			})
		}
		return results
	}

	// 不是已知指标名时，尝试作为 WQL 表达式求值（返回单个当前值点）
	result, err := wql.EvalQuery(h.db, target)
	if err != nil {
		return nil // 无法求值，返回空结果
	}

	now := time.Now().UnixMilli()
	switch result.Type {
	case wql.ResultScalar:
		return []TimeSeriesResponse{{
			Target:     target,
			Datapoints: [][]interface{}{{*result.Scalar, now}},
		}}
	case wql.ResultVector:
		results := make([]TimeSeriesResponse, 0, len(result.Vector))
		for _, s := range result.Vector {
			seriesName := target
			if len(s.Labels) > 0 {
				seriesName = target + "{" + formatLabels(s.Labels) + "}"
			}
			results = append(results, TimeSeriesResponse{
				Target:     seriesName,
				Datapoints: [][]interface{}{{s.Value, now}},
			})
		}
		return results
	}
	return nil
}

// handleAnnotations POST /api/grafana/annotations — 将告警事件作为 Grafana 注释返回
func (h *Handler) handleAnnotations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req annotationRequest
	json.NewDecoder(r.Body).Decode(&req)

	from, _ := parseGrafanaTime(req.Range.From)
	to, _ := parseGrafanaTime(req.Range.To)
	// 解析失败时使用宽泛的默认范围
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now().Add(time.Hour)
	}

	annotations := make([]AnnotationResponse, 0)

	if h.alerts != nil {
		for _, ev := range h.alerts.GetHistory() {
			// 仅返回在查询时间范围内的事件
			if ev.Timestamp.Before(from) || ev.Timestamp.After(to) {
				continue
			}
			tags := []string{"alert", string(ev.State)}
			annotations = append(annotations, AnnotationResponse{
				Annotation: req.Annotation,
				Title:      ev.RuleName,
				Time:       ev.Timestamp.UnixMilli(),
				IsRegion:   false,
				Tags:       tags,
				Text:       ev.Message,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(annotations)
}

// ─────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// parseGrafanaTime 解析 Grafana 传入的时间字符串（ISO 8601 格式）
// 兼容带毫秒（"2006-01-02T15:04:05.000Z"）和不带毫秒（RFC3339）两种格式
func parseGrafanaTime(s string) (time.Time, error) {
	// 先尝试带毫秒格式（Grafana 默认格式）
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", s); err == nil {
		return t, nil
	}
	// 再尝试标准 RFC3339
	return time.Parse(time.RFC3339, s)
}

// formatLabels 将标签 map 格式化为 key=value,... 字符串（用于时序名称后缀）
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}
