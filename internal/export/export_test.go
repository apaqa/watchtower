package export

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tracestore"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 辅助函数 ───────────────────────────────────────────────────────────────────

func newTestHandler() (*Handler, *tsdb.TSDB, *logstore.Store, *tracestore.Store) {
	db := tsdb.New()
	ls := logstore.New()
	ts := tracestore.New()
	return New(db, ls, ts), db, ls, ts
}

func doRequest(mux *http.ServeMux, method, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, url, nil))
	return rec
}

// ── 指标导出 ───────────────────────────────────────────────────────────────────

func TestMetricsExport_JSON(t *testing.T) {
	h, db, _, _ := newTestHandler()
	now := time.Now().UnixMilli()
	db.Write([]model.MetricPoint{
		{Name: "cpu", Labels: map[string]string{"host": "srv1"}, Value: 55.5, Timestamp: now - 1000},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := doRequest(mux, http.MethodGet, "/api/v1/export/metrics?format=json&name=cpu")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var rows []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&rows)
	if len(rows) == 0 {
		t.Fatal("expected at least 1 row")
	}
	if rows[0]["metric"] != "cpu" {
		t.Errorf("unexpected metric: %v", rows[0]["metric"])
	}
}

func TestMetricsExport_CSV(t *testing.T) {
	h, db, _, _ := newTestHandler()
	now := time.Now().UnixMilli()
	db.Write([]model.MetricPoint{
		{Name: "mem", Labels: map[string]string{}, Value: 1234, Timestamp: now - 500},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := doRequest(mux, http.MethodGet, "/api/v1/export/metrics?format=csv&name=mem")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("expected CSV content type, got %s", ct)
	}
	r := csv.NewReader(rec.Body)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse error: %v", err)
	}
	// 标题行 + 至少 1 条数据行
	if len(records) < 2 {
		t.Fatalf("expected header + data rows, got %d rows", len(records))
	}
	if records[0][0] != "metric" {
		t.Errorf("unexpected CSV header: %v", records[0])
	}
}

func TestMetricsExport_TimeRange(t *testing.T) {
	h, db, _, _ := newTestHandler()
	now := time.Now().UnixMilli()
	// 两个数据点：一个在范围内，一个在 5 秒前（范围外）
	db.Write([]model.MetricPoint{
		{Name: "rps", Labels: map[string]string{}, Value: 10, Timestamp: now - 500},
		{Name: "rps", Labels: map[string]string{}, Value: 99, Timestamp: now - 5000},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/export/metrics", nil)
	q := req.URL.Query()
	q.Set("format", "json")
	q.Set("name", "rps")
	q.Set("start", strconv.FormatInt(now-1000, 10))
	q.Set("end", strconv.FormatInt(now, 10))
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var rows []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&rows)
	if len(rows) != 1 {
		t.Errorf("expected 1 row (time-range filter), got %d", len(rows))
	}
}

func TestMetricsExport_AllMetrics(t *testing.T) {
	h, db, _, _ := newTestHandler()
	now := time.Now().UnixMilli()
	db.Write([]model.MetricPoint{
		{Name: "cpu", Labels: map[string]string{}, Value: 1, Timestamp: now - 100},
		{Name: "mem", Labels: map[string]string{}, Value: 2, Timestamp: now - 100},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := doRequest(mux, http.MethodGet, "/api/v1/export/metrics?format=json")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var rows []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&rows)
	if len(rows) < 2 {
		t.Errorf("expected at least 2 rows (all metrics), got %d", len(rows))
	}
}

// ── 日志导出 ───────────────────────────────────────────────────────────────────

func TestLogsExport_JSON(t *testing.T) {
	h, _, ls, _ := newTestHandler()
	ls.Write(model.LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     model.LogLevelInfo,
		Source:    "test",
		Message:   "hello export",
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := doRequest(mux, http.MethodGet, "/api/v1/export/logs?format=json")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var entries []model.LogEntry
	json.NewDecoder(rec.Body).Decode(&entries)
	if len(entries) == 0 {
		t.Fatal("expected at least 1 log entry")
	}
	if entries[0].Source != "test" {
		t.Errorf("unexpected source: %s", entries[0].Source)
	}
}

func TestLogsExport_CSV(t *testing.T) {
	h, _, ls, _ := newTestHandler()
	ls.Write(model.LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     model.LogLevelError,
		Source:    "svc",
		Message:   "oops",
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := doRequest(mux, http.MethodGet, "/api/v1/export/logs?format=csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	r := csv.NewReader(rec.Body)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse error: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + data rows, got %d", len(records))
	}
	if records[0][0] != "timestamp" {
		t.Errorf("unexpected CSV header: %v", records[0])
	}
}

// ── 链路追踪导出 ───────────────────────────────────────────────────────────────

func TestTracesExport_JSON(t *testing.T) {
	h, _, _, ts := newTestHandler()
	now := time.Now().UnixMilli()
	ts.WriteSpans([]model.Span{
		{SpanID: "s1", TraceID: "t1", ServiceName: "api", OperationName: "GET /", StartTime: now - 100, DurationMs: 50, Status: "ok"},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/export/traces", nil)
	q := req.URL.Query()
	q.Set("format", "json")
	q.Set("start", strconv.FormatInt(now-10000, 10))
	q.Set("end", strconv.FormatInt(now+10000, 10))
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var traces []*model.Trace
	json.NewDecoder(rec.Body).Decode(&traces)
	if len(traces) == 0 {
		t.Fatal("expected at least 1 trace")
	}
	if traces[0].TraceID != "t1" {
		t.Errorf("unexpected trace ID: %s", traces[0].TraceID)
	}
}

func TestTracesExport_CSV(t *testing.T) {
	h, _, _, ts := newTestHandler()
	now := time.Now().UnixMilli()
	ts.WriteSpans([]model.Span{
		{SpanID: "s1", TraceID: "t2", ServiceName: "db", OperationName: "SELECT", StartTime: now - 100, DurationMs: 10, Status: "ok"},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/export/traces", nil)
	q := req.URL.Query()
	q.Set("format", "csv")
	q.Set("start", strconv.FormatInt(now-10000, 10))
	q.Set("end", strconv.FormatInt(now+10000, 10))
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	r := csv.NewReader(rec.Body)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse error: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + data rows, got %d", len(records))
	}
	if records[0][0] != "trace_id" {
		t.Errorf("unexpected CSV header: %v", records[0])
	}
}

func TestTracesExport_TimeFilter(t *testing.T) {
	h, _, _, ts := newTestHandler()
	now := time.Now().UnixMilli()
	// 两条 trace：一条在范围内，一条太旧
	ts.WriteSpans([]model.Span{
		{SpanID: "s1", TraceID: "new-trace", ServiceName: "api", OperationName: "GET /", StartTime: now - 500, DurationMs: 10, Status: "ok"},
	})
	ts.WriteSpans([]model.Span{
		{SpanID: "s2", TraceID: "old-trace", ServiceName: "api", OperationName: "GET /", StartTime: now - 100000, DurationMs: 10, Status: "ok"},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/export/traces", nil)
	q := req.URL.Query()
	q.Set("format", "json")
	q.Set("start", strconv.FormatInt(now-1000, 10))
	q.Set("end", strconv.FormatInt(now, 10))
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var traces []*model.Trace
	json.NewDecoder(rec.Body).Decode(&traces)
	if len(traces) != 1 {
		t.Errorf("expected 1 trace after time filter, got %d", len(traces))
	}
	if len(traces) > 0 && traces[0].TraceID != "new-trace" {
		t.Errorf("expected new-trace, got %s", traces[0].TraceID)
	}
}

// ── 方法限制 ──────────────────────────────────────────────────────────────────

func TestMethodNotAllowed(t *testing.T) {
	h, _, _, _ := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	for _, path := range []string{
		"/api/v1/export/metrics",
		"/api/v1/export/logs",
		"/api/v1/export/traces",
	} {
		rec := doRequest(mux, http.MethodPost, path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", path, rec.Code)
		}
	}
}

// ── labelsToString ───────────────────────────────────────────────────────────

func TestLabelsToString(t *testing.T) {
	s := labelsToString(map[string]string{"b": "2", "a": "1"})
	if s != "a=1,b=2" {
		t.Errorf("unexpected: %q", s)
	}
	if labelsToString(nil) != "" {
		t.Error("expected empty string for nil labels")
	}
}
