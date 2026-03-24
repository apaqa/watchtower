package grafana

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

func newTestMux(db *tsdb.TSDB, eng *alert.Engine) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterRoutes(mux, New(db, eng))
	return mux
}

func writeMetric(db *tsdb.TSDB, name string, value float64) {
	db.Write([]model.MetricPoint{{
		Name:      name,
		Labels:    map[string]string{},
		Value:     value,
		Timestamp: time.Now().UnixMilli(),
	}})
}

// ── 健康检查测试 ───────────────────────────────────────────────────────────────

func TestGrafanaHealth_Returns200(t *testing.T) {
	mux := newTestMux(tsdb.New(), nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/grafana/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ── Search 端点测试 ────────────────────────────────────────────────────────────

func TestGrafanaSearch_ReturnsAllMetrics(t *testing.T) {
	db := tsdb.New()
	writeMetric(db, "cpu_usage_percent", 45.0)
	writeMetric(db, "memory_usage_percent", 60.0)
	writeMetric(db, "disk_usage_percent", 70.0)

	mux := newTestMux(db, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/search",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var metrics []string
	if err := json.NewDecoder(rec.Body).Decode(&metrics); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(metrics) < 3 {
		t.Errorf("expected at least 3 metrics, got %d: %v", len(metrics), metrics)
	}
}

func TestGrafanaSearch_FiltersByPrefix(t *testing.T) {
	db := tsdb.New()
	writeMetric(db, "cpu_usage_percent", 45.0)
	writeMetric(db, "memory_usage_percent", 60.0)
	writeMetric(db, "disk_usage_percent", 70.0)

	mux := newTestMux(db, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/search",
		strings.NewReader(`{"target":"cpu"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var metrics []string
	json.NewDecoder(rec.Body).Decode(&metrics)
	if len(metrics) != 1 || metrics[0] != "cpu_usage_percent" {
		t.Errorf("expected [cpu_usage_percent], got %v", metrics)
	}
}

func TestGrafanaSearch_EmptyDB_ReturnsEmptyArray(t *testing.T) {
	mux := newTestMux(tsdb.New(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/search",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var metrics []string
	json.NewDecoder(rec.Body).Decode(&metrics)
	if metrics == nil {
		t.Error("expected non-nil (empty) slice, got nil")
	}
}

// ── Query 端点测试 ─────────────────────────────────────────────────────────────

func TestGrafanaQuery_ReturnsTimeSeries(t *testing.T) {
	db := tsdb.New()
	now := time.Now()
	// 写入 5 个时间点（过去 5 分钟内）
	for i := 0; i < 5; i++ {
		db.Write([]model.MetricPoint{{
			Name:      "cpu_usage_percent",
			Labels:    map[string]string{},
			Value:     float64(40 + i*5),
			Timestamp: now.Add(time.Duration(-5+i) * time.Minute).UnixMilli(),
		}})
	}

	mux := newTestMux(db, nil)

	from := now.Add(-1 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	to := now.Add(1 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	body := fmt.Sprintf(`{"range":{"from":%q,"to":%q},"targets":[{"target":"cpu_usage_percent","type":"timeserie"}]}`,
		from, to)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/query",
		strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var results []TimeSeriesResponse
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one time series in response")
	}
	if results[0].Target != "cpu_usage_percent" {
		t.Errorf("expected target=cpu_usage_percent, got %q", results[0].Target)
	}
	if len(results[0].Datapoints) == 0 {
		t.Error("expected non-empty datapoints")
	}
	// 每个 datapoint 应为 [value, timestamp_ms]（2 个元素）
	dp := results[0].Datapoints[0]
	if len(dp) != 2 {
		t.Errorf("expected datapoint length 2, got %d", len(dp))
	}
}

func TestGrafanaQuery_UnknownMetric_WQLFallback(t *testing.T) {
	// 纯数值 WQL 表达式（无需 TSDB 数据）
	mux := newTestMux(tsdb.New(), nil)

	now := time.Now()
	from := now.Add(-1 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	to := now.UTC().Format("2006-01-02T15:04:05.000Z")
	body := fmt.Sprintf(`{"range":{"from":%q,"to":%q},"targets":[{"target":"2 + 3","type":"timeserie"}]}`, from, to)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/query",
		strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var results []TimeSeriesResponse
	json.NewDecoder(rec.Body).Decode(&results)
	if len(results) == 0 {
		t.Fatal("expected result from WQL fallback")
	}
	// "2 + 3" = 5.0
	if len(results[0].Datapoints) == 0 {
		t.Fatal("expected at least one datapoint")
	}
	// datapoints[0][0] should be 5.0
	if v, ok := results[0].Datapoints[0][0].(float64); !ok || v != 5.0 {
		t.Errorf("expected value=5.0 from WQL expr, got %v", results[0].Datapoints[0][0])
	}
}

func TestGrafanaQuery_EmptyTargets_ReturnsEmpty(t *testing.T) {
	mux := newTestMux(tsdb.New(), nil)

	now := time.Now()
	from := now.Add(-1 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	to := now.UTC().Format("2006-01-02T15:04:05.000Z")
	body := fmt.Sprintf(`{"range":{"from":%q,"to":%q},"targets":[]}`, from, to)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/query",
		strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var results []TimeSeriesResponse
	json.NewDecoder(rec.Body).Decode(&results)
	if len(results) != 0 {
		t.Errorf("expected empty results for empty targets, got %d", len(results))
	}
}

// ── Annotations 端点测试 ───────────────────────────────────────────────────────

func TestGrafanaAnnotations_NoAlerts_ReturnsEmptyArray(t *testing.T) {
	mux := newTestMux(tsdb.New(), alert.NewEngine(tsdb.New()))
	body := `{"range":{"from":"2020-01-01T00:00:00.000Z","to":"2030-01-01T00:00:00.000Z"},"annotation":{"name":"Alerts"}}`

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/annotations",
		strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var annotations []AnnotationResponse
	if err := json.NewDecoder(rec.Body).Decode(&annotations); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if annotations == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
}

func TestGrafanaAnnotations_WithFiringAlert(t *testing.T) {
	// 触发一条告警，注释端点应返回对应的告警事件
	db := tsdb.New()
	db.Write([]model.MetricPoint{{
		Name:      "cpu_usage_percent",
		Labels:    map[string]string{},
		Value:     95.0,
		Timestamp: time.Now().UnixMilli(),
	}})

	alertEng := alert.NewEngine(db)
	alertEng.AddRule(alert.AlertRule{
		Name:       "high_cpu_test",
		Expression: "cpu_usage_percent",
		Operator:   ">",
		Threshold:  90.0,
		Severity:   "critical",
	})
	alertEng.Evaluate() // 强制立即求值，产生 Firing 事件

	mux := newTestMux(db, alertEng)
	body := `{"range":{"from":"2020-01-01T00:00:00.000Z","to":"2030-01-01T00:00:00.000Z"},"annotation":{"name":"Alerts","datasource":"WatchTower"}}`

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/grafana/annotations",
		strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var annotations []AnnotationResponse
	if err := json.NewDecoder(rec.Body).Decode(&annotations); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(annotations) == 0 {
		t.Fatal("expected at least one annotation from firing alert")
	}
	if annotations[0].Title != "high_cpu_test" {
		t.Errorf("expected title=high_cpu_test, got %q", annotations[0].Title)
	}
	if annotations[0].Time == 0 {
		t.Error("expected non-zero annotation time")
	}
	if len(annotations[0].Tags) == 0 {
		t.Error("expected non-empty tags")
	}
}

// ── 方法验证测试 ───────────────────────────────────────────────────────────────

func TestGrafanaMethodNotAllowed(t *testing.T) {
	mux := newTestMux(tsdb.New(), nil)

	for _, path := range []string{"/api/grafana/search", "/api/grafana/query", "/api/grafana/annotations"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: expected 405, got %d", path, rec.Code)
		}
	}
}

// ── parseGrafanaTime 测试 ─────────────────────────────────────────────────────

func TestParseGrafanaTime_WithMilliseconds(t *testing.T) {
	t1, err := parseGrafanaTime("2024-03-01T12:00:00.000Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if t1.Year() != 2024 || t1.Month() != 3 || t1.Day() != 1 {
		t.Errorf("unexpected time: %v", t1)
	}
}

func TestParseGrafanaTime_RFC3339(t *testing.T) {
	t1, err := parseGrafanaTime("2024-03-01T12:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if t1.Year() != 2024 {
		t.Errorf("unexpected year: %d", t1.Year())
	}
}
