package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 基础规则 CRUD ──────────────────────────────────────────────────────────────

func TestAddAndListRules(t *testing.T) {
	p := New(tsdb.New())
	rule := Rule{Name: "r1", InputMetric: "cpu", OutputMetric: "cpu_avg", Aggregation: "avg", Window: "1m"}
	if err := p.AddRule(rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	rules := p.ListRules()
	if len(rules) != 1 || rules[0].Name != "r1" {
		t.Fatalf("expected 1 rule named r1, got %v", rules)
	}
}

func TestAddRule_SetsCreatedAt(t *testing.T) {
	p := New(tsdb.New())
	before := time.Now().UnixMilli()
	p.AddRule(Rule{Name: "r", InputMetric: "x", OutputMetric: "y", Aggregation: "sum", Window: "5m"})
	after := time.Now().UnixMilli()
	rules := p.ListRules()
	if rules[0].CreatedAt < before || rules[0].CreatedAt > after {
		t.Errorf("CreatedAt out of range: %d", rules[0].CreatedAt)
	}
}

func TestRemoveRule(t *testing.T) {
	p := New(tsdb.New())
	p.AddRule(Rule{Name: "r1", InputMetric: "cpu", OutputMetric: "cpu_avg", Aggregation: "avg", Window: "1m"})
	if err := p.RemoveRule("r1"); err != nil {
		t.Fatalf("RemoveRule: %v", err)
	}
	if p.Count() != 0 {
		t.Fatal("expected 0 rules after removal")
	}
}

func TestRemoveRule_NotFound(t *testing.T) {
	p := New(tsdb.New())
	if err := p.RemoveRule("missing"); err == nil {
		t.Fatal("expected error for missing rule")
	}
}

func TestAddRule_Validation(t *testing.T) {
	p := New(tsdb.New())
	cases := []struct {
		name string
		rule Rule
	}{
		{"empty name", Rule{InputMetric: "x", OutputMetric: "y", Aggregation: "avg", Window: "1m"}},
		{"empty input", Rule{Name: "r", OutputMetric: "y", Aggregation: "avg", Window: "1m"}},
		{"empty output", Rule{Name: "r", InputMetric: "x", Aggregation: "avg", Window: "1m"}},
		{"bad agg", Rule{Name: "r", InputMetric: "x", OutputMetric: "y", Aggregation: "bad", Window: "1m"}},
		{"bad window", Rule{Name: "r", InputMetric: "x", OutputMetric: "y", Aggregation: "avg", Window: "2m"}},
	}
	for _, tc := range cases {
		if err := p.AddRule(tc.rule); err == nil {
			t.Errorf("%s: expected validation error", tc.name)
		}
	}
}

func TestListRules_SortedByName(t *testing.T) {
	p := New(tsdb.New())
	for _, name := range []string{"c_rule", "a_rule", "b_rule"} {
		p.AddRule(Rule{Name: name, InputMetric: "x", OutputMetric: "y", Aggregation: "avg", Window: "1m"})
	}
	rules := p.ListRules()
	if rules[0].Name != "a_rule" || rules[1].Name != "b_rule" || rules[2].Name != "c_rule" {
		t.Errorf("unexpected sort order: %v", rules)
	}
}

// ── 聚合计算 ────────────────────────────────────────────────────────────────────

func TestComputeAgg_Avg(t *testing.T) {
	v := computeAgg("avg", []float64{1, 2, 3, 4, 5})
	if v != 3.0 {
		t.Errorf("avg: expected 3.0, got %f", v)
	}
}

func TestComputeAgg_Sum(t *testing.T) {
	v := computeAgg("sum", []float64{1, 2, 3})
	if v != 6.0 {
		t.Errorf("sum: expected 6.0, got %f", v)
	}
}

func TestComputeAgg_MaxMin(t *testing.T) {
	vals := []float64{3, 1, 4, 1, 5, 9}
	if computeAgg("max", vals) != 9 {
		t.Error("max failed")
	}
	if computeAgg("min", vals) != 1 {
		t.Error("min failed")
	}
}

func TestComputeAgg_Count(t *testing.T) {
	v := computeAgg("count", []float64{10, 20, 30})
	if v != 3 {
		t.Errorf("count: expected 3, got %f", v)
	}
}

func TestPercentile(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	p50 := percentile(vals, 50)
	if p50 < 5.0 || p50 > 6.0 {
		t.Errorf("p50 out of range: %f", p50)
	}
	p99 := percentile(vals, 99)
	if p99 < 9.8 {
		t.Errorf("p99 too low: %f", p99)
	}
	p0 := percentile(vals, 0)
	if p0 != 1 {
		t.Errorf("p0: expected 1, got %f", p0)
	}
	p100 := percentile(vals, 100)
	if p100 != 10 {
		t.Errorf("p100: expected 10, got %f", p100)
	}
}

func TestPercentile_Empty(t *testing.T) {
	if percentile(nil, 50) != 0 {
		t.Error("expected 0 for empty slice")
	}
}

// ── 聚合评估（写入 TSDB）──────────────────────────────────────────────────────

func TestEvalRule_WritesOutputMetric(t *testing.T) {
	db := tsdb.New()
	now := time.Now().UnixMilli()
	// 写入几个输入数据点
	db.Write([]model.MetricPoint{
		{Name: "req_latency", Labels: map[string]string{}, Value: 100, Timestamp: now - 30000},
		{Name: "req_latency", Labels: map[string]string{}, Value: 200, Timestamp: now - 20000},
		{Name: "req_latency", Labels: map[string]string{}, Value: 300, Timestamp: now - 10000},
	})

	p := New(db)
	p.AddRule(Rule{Name: "lat_avg", InputMetric: "req_latency", OutputMetric: "req_latency_avg_1m", Aggregation: "avg", Window: "1m"})
	p.evaluate()

	// 输出指标应已写入
	series := db.GetSeries("req_latency_avg_1m")
	if len(series) == 0 {
		t.Fatal("output metric not written to TSDB")
	}
	points := series[0].QueryRange(now-5000, now+5000)
	if len(points) == 0 {
		t.Fatal("no data points found for output metric")
	}
	// avg(100,200,300) = 200
	if points[len(points)-1].Value != 200 {
		t.Errorf("expected avg=200, got %f", points[len(points)-1].Value)
	}
}

func TestEvalRule_GroupBy(t *testing.T) {
	db := tsdb.New()
	now := time.Now().UnixMilli()
	// 两个不同 service 的数据点
	db.Write([]model.MetricPoint{
		{Name: "req_count", Labels: map[string]string{"service": "svc-a"}, Value: 10, Timestamp: now - 30000},
		{Name: "req_count", Labels: map[string]string{"service": "svc-a"}, Value: 20, Timestamp: now - 20000},
		{Name: "req_count", Labels: map[string]string{"service": "svc-b"}, Value: 5, Timestamp: now - 30000},
		{Name: "req_count", Labels: map[string]string{"service": "svc-b"}, Value: 15, Timestamp: now - 20000},
	})

	p := New(db)
	p.AddRule(Rule{
		Name: "req_sum", InputMetric: "req_count", OutputMetric: "req_count_sum",
		Aggregation: "sum", Window: "1m", GroupBy: []string{"service"},
	})
	p.evaluate()

	series := db.GetSeries("req_count_sum")
	if len(series) < 2 {
		t.Fatalf("expected 2 output series (one per service group), got %d", len(series))
	}
}

func TestEvalRule_NoInputData(t *testing.T) {
	db := tsdb.New()
	p := New(db)
	p.AddRule(Rule{Name: "r", InputMetric: "nonexistent", OutputMetric: "out", Aggregation: "avg", Window: "1m"})
	// 无数据时不应 panic
	p.evaluate()
}

// ── HTTP API ─────────────────────────────────────────────────────────────────

func TestAPIGetRules_Empty(t *testing.T) {
	p := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, p)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pipeline/rules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var rules []Rule
	json.NewDecoder(rec.Body).Decode(&rules)
	if len(rules) != 0 {
		t.Fatalf("expected empty list, got %v", rules)
	}
}

func TestAPIPostRule(t *testing.T) {
	p := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, p)
	body := `{"name":"r1","input_metric":"cpu","output_metric":"cpu_avg","aggregation":"avg","window":"1m"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/rules", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if p.Count() != 1 {
		t.Fatal("rule not added")
	}
}

func TestAPIPostRule_Invalid(t *testing.T) {
	p := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, p)
	body := `{"name":"","input_metric":"cpu","output_metric":"cpu_avg","aggregation":"avg","window":"1m"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/pipeline/rules", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAPIDeleteRule(t *testing.T) {
	p := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, p)
	p.AddRule(Rule{Name: "r1", InputMetric: "cpu", OutputMetric: "cpu_avg", Aggregation: "avg", Window: "1m"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/pipeline/rules/r1", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if p.Count() != 0 {
		t.Fatal("rule not removed")
	}
}

func TestAPIDeleteRule_NotFound(t *testing.T) {
	p := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, p)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/pipeline/rules/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	p := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, p)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/pipeline/rules", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
