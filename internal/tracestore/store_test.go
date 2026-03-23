// Package tracestore — unit and API tests for the trace store.
package tracestore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// makeSpan 是测试辅助函数：创建一个具有指定字段的 Span。
func makeSpan(traceID, spanID, parentID, op, svc string, startMs, durMs int64, status string) model.Span {
	return model.Span{
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentID,
		OperationName: op,
		ServiceName:   svc,
		StartTime:     startMs,
		DurationMs:    durMs,
		Status:        status,
	}
}

// TestWriteSpansGroupByTraceID 验证相同 trace_id 的 span 被合并到同一个 Trace。
func TestWriteSpansGroupByTraceID(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	spans := []model.Span{
		makeSpan("trace-1", "span-a", "", "root-op", "svc-a", now, 100, "ok"),
		makeSpan("trace-1", "span-b", "span-a", "child-op", "svc-b", now+5, 50, "ok"),
		makeSpan("trace-2", "span-c", "", "other-op", "svc-c", now, 80, "ok"),
	}
	s.WriteSpans(spans)

	if s.TotalCount() != 2 {
		t.Fatalf("expected 2 traces, got %d", s.TotalCount())
	}

	tr, ok := s.GetTrace("trace-1")
	if !ok {
		t.Fatal("trace-1 not found")
	}
	if len(tr.Spans) != 2 {
		t.Errorf("trace-1: expected 2 spans, got %d", len(tr.Spans))
	}
}

// TestTraceMetadataRebuilt 验证 rebuildTrace 正确计算 StartTime、DurationMs、HasError、ServiceNames。
func TestTraceMetadataRebuilt(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	spans := []model.Span{
		makeSpan("t1", "s1", "", "root", "svc-x", now, 200, "ok"),
		makeSpan("t1", "s2", "s1", "child", "svc-y", now+10, 150, "error"),
	}
	s.WriteSpans(spans)

	tr, _ := s.GetTrace("t1")
	if tr.StartTime != now {
		t.Errorf("StartTime: expected %d, got %d", now, tr.StartTime)
	}
	// span1 ends at now+200, span2 ends at now+10+150=now+160
	// duration = max_end - min_start = (now+200) - now = 200
	if tr.DurationMs != 200 {
		t.Errorf("DurationMs: expected 200, got %d", tr.DurationMs)
	}
	if !tr.HasError {
		t.Error("HasError should be true")
	}
	if len(tr.ServiceNames) != 2 {
		t.Errorf("ServiceNames: expected 2, got %d", len(tr.ServiceNames))
	}
}

// TestListTracesNewestFirst 验证 ListTraces 按插入时间倒序返回（最新在前）。
func TestListTracesNewestFirst(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("trace-%d", i)
		s.WriteSpans([]model.Span{
			makeSpan(id, "s"+id, "", "op", "svc", now+int64(i), 10, "ok"),
		})
	}

	results := s.ListTraces(QueryOptions{Limit: 5})
	if len(results) != 5 {
		t.Fatalf("expected 5, got %d", len(results))
	}
	// First result should be the newest (trace-4)
	if results[0].TraceID != "trace-4" {
		t.Errorf("expected trace-4 first, got %s", results[0].TraceID)
	}
}

// TestListTracesServiceFilter 验证按服务名过滤 trace。
func TestListTracesServiceFilter(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	s.WriteSpans([]model.Span{
		makeSpan("t-alpha", "s1", "", "op", "alpha-svc", now, 50, "ok"),
	})
	s.WriteSpans([]model.Span{
		makeSpan("t-beta", "s2", "", "op", "beta-svc", now, 50, "ok"),
	})
	s.WriteSpans([]model.Span{
		makeSpan("t-alpha2", "s3", "", "op", "alpha-svc", now, 50, "ok"),
	})

	results := s.ListTraces(QueryOptions{Service: "alpha-svc", Limit: 50})
	if len(results) != 2 {
		t.Errorf("expected 2 alpha traces, got %d", len(results))
	}
	for _, r := range results {
		found := false
		for _, svc := range r.ServiceNames {
			if svc == "alpha-svc" {
				found = true
			}
		}
		if !found {
			t.Errorf("trace %s does not contain alpha-svc", r.TraceID)
		}
	}
}

// TestListTracesMinDuration 验证按最小持续时间过滤 trace。
func TestListTracesMinDuration(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	s.WriteSpans([]model.Span{makeSpan("fast", "s1", "", "op", "svc", now, 10, "ok")})
	s.WriteSpans([]model.Span{makeSpan("slow", "s2", "", "op", "svc", now, 200, "ok")})

	results := s.ListTraces(QueryOptions{MinDurationMs: 100, Limit: 10})
	if len(results) != 1 {
		t.Errorf("expected 1 trace with dur>=100ms, got %d", len(results))
	}
	if results[0].TraceID != "slow" {
		t.Errorf("expected 'slow' trace, got %s", results[0].TraceID)
	}
}

// TestGetTrace 验证按 trace_id 返回完整 trace（含所有 span）。
func TestGetTrace(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	spans := []model.Span{
		makeSpan("full-trace", "root", "", "root-op", "svc", now, 100, "ok"),
		makeSpan("full-trace", "child1", "root", "child-op-1", "svc", now+5, 30, "ok"),
		makeSpan("full-trace", "child2", "root", "child-op-2", "svc", now+40, 55, "error"),
	}
	s.WriteSpans(spans)

	tr, ok := s.GetTrace("full-trace")
	if !ok {
		t.Fatal("GetTrace returned false")
	}
	if len(tr.Spans) != 3 {
		t.Errorf("expected 3 spans, got %d", len(tr.Spans))
	}
	// Root span should be first
	if tr.Spans[0].SpanID != "root" {
		t.Errorf("root span should be first, got %s", tr.Spans[0].SpanID)
	}
	// Verify it's a deep copy (modifying the result shouldn't affect the store)
	tr.Spans[0].OperationName = "mutated"
	original, _ := s.GetTrace("full-trace")
	if original.Spans[0].OperationName == "mutated" {
		t.Error("GetTrace should return a deep copy")
	}
}

// TestGetTraceNotFound 验证不存在的 trace_id 返回 false。
func TestGetTraceNotFound(t *testing.T) {
	s := New()
	defer s.Stop()

	_, ok := s.GetTrace("nonexistent")
	if ok {
		t.Error("expected false for unknown trace_id")
	}
}

// TestRingBufferEviction 验证超过 maxTraces 时最旧的 trace 被淘汰。
func TestRingBufferEviction(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	// 写入 maxTraces+10 条 trace
	for i := 0; i < maxTraces+10; i++ {
		id := fmt.Sprintf("evict-%d", i)
		s.WriteSpans([]model.Span{
			makeSpan(id, "s"+id, "", "op", "svc", now+int64(i), 1, "ok"),
		})
	}

	if s.TotalCount() != maxTraces {
		t.Errorf("expected %d traces after eviction, got %d", maxTraces, s.TotalCount())
	}
	// 最旧的 trace（evict-0..evict-9）应已被淘汰
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("evict-%d", i)
		if _, ok := s.GetTrace(id); ok {
			t.Errorf("trace %s should have been evicted", id)
		}
	}
	// 最新的 trace（evict-maxTraces+9）应还在
	newest := fmt.Sprintf("evict-%d", maxTraces+9)
	if _, ok := s.GetTrace(newest); !ok {
		t.Errorf("newest trace %s should still exist", newest)
	}
}

// TestWriteSpansIgnoresInvalid 验证 span_id 或 trace_id 为空的 span 被忽略。
func TestWriteSpansIgnoresInvalid(t *testing.T) {
	s := New()
	defer s.Stop()

	now := time.Now().UnixMilli()
	spans := []model.Span{
		makeSpan("", "s1", "", "op", "svc", now, 10, "ok"),    // 空 trace_id
		makeSpan("t1", "", "", "op", "svc", now, 10, "ok"),    // 空 span_id
		makeSpan("t1", "s2", "", "op", "svc", now, 10, "ok"),  // 有效
	}
	s.WriteSpans(spans)

	if s.TotalCount() != 1 {
		t.Errorf("expected 1 valid trace, got %d", s.TotalCount())
	}
	tr, _ := s.GetTrace("t1")
	if len(tr.Spans) != 1 {
		t.Errorf("expected 1 valid span in trace, got %d", len(tr.Spans))
	}
}

// ── HTTP API tests ──────────────────────────────────────────────────────────

func newTestMux(t *testing.T) (*Store, *http.ServeMux) {
	t.Helper()
	s := New()
	t.Cleanup(func() { s.Stop() })
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	return s, mux
}

// TestAPIIngestSpans 验证 POST /api/v1/traces 写入 span 并返回 204。
func TestAPIIngestSpans(t *testing.T) {
	_, mux := newTestMux(t)

	now := time.Now().UnixMilli()
	spans := []model.Span{
		makeSpan("api-trace", "s1", "", "root", "svc", now, 100, "ok"),
		makeSpan("api-trace", "s2", "s1", "child", "svc", now+5, 40, "ok"),
	}
	body, _ := json.Marshal(spans)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAPIListTraces 验证 GET /api/v1/traces 返回 trace 摘要列表。
func TestAPIListTraces(t *testing.T) {
	store, mux := newTestMux(t)

	now := time.Now().UnixMilli()
	store.WriteSpans([]model.Span{
		makeSpan("list-t1", "s1", "", "root-op", "my-svc", now, 75, "ok"),
	})
	store.WriteSpans([]model.Span{
		makeSpan("list-t2", "s2", "", "other-op", "other-svc", now+1, 30, "ok"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var summaries []model.TraceSummary
	if err := json.NewDecoder(w.Body).Decode(&summaries); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(summaries) != 2 {
		t.Errorf("expected 2 summaries, got %d", len(summaries))
	}
}

// TestAPIGetTrace 验证 GET /api/v1/traces/{trace_id} 返回完整 trace。
func TestAPIGetTrace(t *testing.T) {
	store, mux := newTestMux(t)

	now := time.Now().UnixMilli()
	store.WriteSpans([]model.Span{
		makeSpan("get-trace", "s1", "", "root", "svc", now, 100, "ok"),
		makeSpan("get-trace", "s2", "s1", "child", "svc", now+10, 50, "error"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/get-trace", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var tr model.Trace
	if err := json.NewDecoder(w.Body).Decode(&tr); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if tr.TraceID != "get-trace" {
		t.Errorf("expected trace_id 'get-trace', got %s", tr.TraceID)
	}
	if len(tr.Spans) != 2 {
		t.Errorf("expected 2 spans, got %d", len(tr.Spans))
	}
	if !tr.HasError {
		t.Error("expected HasError=true")
	}
}

// TestAPIGetTraceNotFound 验证请求不存在的 trace 返回 404。
func TestAPIGetTraceNotFound(t *testing.T) {
	_, mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/traces/does-not-exist", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
