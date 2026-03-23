// Package model — trace types for distributed tracing support.
package model

// SpanLog is a timestamped log message attached to a span.
type SpanLog struct {
	// Timestamp is Unix milliseconds.
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}

// Span represents a single unit of work within a distributed trace.
// Spans sharing the same TraceID form a complete trace; the parent–child
// relationship is encoded via ParentSpanID (empty string for root spans).
type Span struct {
	SpanID        string            `json:"span_id"`
	ParentSpanID  string            `json:"parent_span_id,omitempty"` // 空字符串表示根 span
	TraceID       string            `json:"trace_id"`
	OperationName string            `json:"operation_name"`
	ServiceName   string            `json:"service_name"`
	StartTime     int64             `json:"start_time"`  // Unix 毫秒
	DurationMs    int64             `json:"duration_ms"` // 持续时间（毫秒）
	Status        string            `json:"status"`      // "ok" 或 "error"
	Tags          map[string]string `json:"tags,omitempty"`
	Logs          []SpanLog         `json:"logs,omitempty"`
}

// TraceSummary is a lightweight summary of a trace used in list responses.
type TraceSummary struct {
	TraceID       string   `json:"trace_id"`
	RootOperation string   `json:"root_operation"` // 根 span 的操作名称
	RootService   string   `json:"root_service"`   // 根 span 的服务名称
	ServiceNames  []string `json:"service_names"`  // 所有涉及的服务（去重后排序）
	StartTime     int64    `json:"start_time"`     // Unix 毫秒
	DurationMs    int64    `json:"duration_ms"`
	SpanCount     int      `json:"span_count"`
	HasError      bool     `json:"has_error"`
}

// Trace is a complete distributed trace containing all of its spans.
type Trace struct {
	TraceID      string   `json:"trace_id"`
	StartTime    int64    `json:"start_time"`    // 最早 span 的起始时间（Unix 毫秒）
	DurationMs   int64    `json:"duration_ms"`   // 从最早 span 开始到最晚 span 结束的总时长
	Spans        []*Span  `json:"spans"`         // 所有 span，根 span 排在最前
	ServiceNames []string `json:"service_names"` // 去重后的服务名列表
	HasError     bool     `json:"has_error"`
}
