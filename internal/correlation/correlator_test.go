package correlation

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 辅助函数 ───────────────────────────────────────────────────────────────────

// writeLinear 向 TSDB 写入两条线性相关的指标序列。
// aSlope/bSlope 控制各自的增长斜率，count 为点数，间隔 1 分钟。
func writeLinear(db *tsdb.TSDB, nameA, nameB string, aSlope, bSlope float64, count int) {
	now := time.Now()
	pts := make([]model.MetricPoint, 0, count*2)
	for i := 0; i < count; i++ {
		ts := now.Add(-time.Duration(count-i) * time.Minute).UnixMilli()
		pts = append(pts,
			model.MetricPoint{Name: nameA, Labels: map[string]string{}, Value: aSlope * float64(i), Timestamp: ts},
			model.MetricPoint{Name: nameB, Labels: map[string]string{}, Value: bSlope * float64(i), Timestamp: ts},
		)
	}
	db.Write(pts)
}

// ── Pearson 计算（pearsonWithPoints）──────────────────────────────────────────

func TestPearson_PerfectPositive(t *testing.T) {
	a := makeBuckets([]float64{1, 2, 3, 4, 5})
	b := makeBuckets([]float64{1, 2, 3, 4, 5})
	r, _ := pearsonWithPoints(a, b)
	if math.Abs(r-1.0) > 1e-9 {
		t.Errorf("expected r=1 for identical series, got %f", r)
	}
}

func TestPearson_PerfectNegative(t *testing.T) {
	a := makeBuckets([]float64{1, 2, 3, 4, 5})
	b := makeBuckets([]float64{5, 4, 3, 2, 1})
	r, _ := pearsonWithPoints(a, b)
	if math.Abs(r+1.0) > 1e-9 {
		t.Errorf("expected r=-1 for inversely proportional series, got %f", r)
	}
}

func TestPearson_NoCorrelation(t *testing.T) {
	// 一条递增，一条常数：相关系数为 0
	a := makeBuckets([]float64{1, 2, 3, 4, 5})
	b := makeBuckets([]float64{3, 3, 3, 3, 3})
	r, _ := pearsonWithPoints(a, b)
	if math.Abs(r) > 1e-9 {
		t.Errorf("expected r=0 for constant series, got %f", r)
	}
}

func TestPearson_InsufficientData(t *testing.T) {
	// 仅 1 个公共时间桶：返回 r=0
	a := makeBuckets([]float64{42})
	b := makeBuckets([]float64{99})
	r, _ := pearsonWithPoints(a, b)
	if r != 0 {
		t.Errorf("expected r=0 for single point, got %f", r)
	}
}

// makeBuckets 将值切片转换为时间递增的 bucketVal 切片
func makeBuckets(vals []float64) []bucketVal {
	buckets := make([]bucketVal, len(vals))
	base := int64(1_700_000_000_000) // 固定基准时间戳（毫秒）
	for i, v := range vals {
		buckets[i] = bucketVal{t: base + int64(i)*60_000, v: v}
	}
	return buckets
}

// ── interpret ─────────────────────────────────────────────────────────────────

func TestInterpret(t *testing.T) {
	cases := []struct{ r float64; want string }{
		{0.9, "strong_positive"},
		{0.5, "weak_positive"},
		{0.1, "none"},
		{-0.5, "weak_negative"},
		{-0.9, "strong_negative"},
	}
	for _, tc := range cases {
		if got := interpret(tc.r); got != tc.want {
			t.Errorf("interpret(%f): expected %s, got %s", tc.r, tc.want, got)
		}
	}
}

// ── Correlator 集成测试 ───────────────────────────────────────────────────────

func TestCorrelate_StrongPositive(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "cpu", "mem", 1.0, 1.0, 30)
	c := New(db)
	res := c.Correlate("cpu", "mem", "1h")
	if res.Coefficient < 0.9 {
		t.Errorf("expected strong positive correlation, got r=%f", res.Coefficient)
	}
	if res.Interpretation != "strong_positive" {
		t.Errorf("expected strong_positive, got %s", res.Interpretation)
	}
}

func TestCorrelate_StrongNegative(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "cpu2", "inv", 1.0, -1.0, 30)
	c := New(db)
	res := c.Correlate("cpu2", "inv", "1h")
	if res.Coefficient > -0.9 {
		t.Errorf("expected strong negative correlation, got r=%f", res.Coefficient)
	}
}

func TestCorrelate_DefaultWindow(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "a", "b", 1.0, 2.0, 10)
	c := New(db)
	// 空 window 应使用默认值 30m 而不 panic
	res := c.Correlate("a", "b", "")
	if res.TimeRange == "" {
		t.Error("expected non-empty time_range in result")
	}
}

func TestCorrelate_InsufficientData(t *testing.T) {
	db := tsdb.New()
	// 空 TSDB：结果应返回 r=0，样本数 0
	c := New(db)
	res := c.Correlate("nonexistent_a", "nonexistent_b", "30m")
	if res.Coefficient != 0 {
		t.Errorf("expected coefficient=0 for empty data, got %f", res.Coefficient)
	}
	if res.SampleCount != 0 {
		t.Errorf("expected sample_count=0, got %d", res.SampleCount)
	}
}

func TestCorrelate_ReturnsSampleCountAndPoints(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "x", "y", 1.0, 1.0, 20)
	c := New(db)
	res := c.Correlate("x", "y", "1h")
	if res.SampleCount == 0 {
		t.Error("expected non-zero sample count")
	}
	if len(res.Points) != res.SampleCount {
		t.Errorf("points len %d != sample_count %d", len(res.Points), res.SampleCount)
	}
}

func TestAutoCorrelate_FindsRelated(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "src", "correlated", 1.0, 2.0, 30)
	writeLinear(db, "unrelated1", "unrelated2", 1.0, 1.0, 5) // 少量数据
	c := New(db)
	res := c.AutoCorrelate("src", "1h")
	if res.SourceMetric != "src" {
		t.Errorf("expected source=src, got %s", res.SourceMetric)
	}
	// 至少应找到一条相关指标
	if len(res.Related) == 0 {
		t.Error("expected at least 1 related metric")
	}
}

func TestAutoCorrelate_MaxFive(t *testing.T) {
	db := tsdb.New()
	now := time.Now()
	// 写入 7 条与 src 相关的指标
	for i := 0; i < 7; i++ {
		pts := make([]model.MetricPoint, 30)
		for j := 0; j < 30; j++ {
			ts := now.Add(-time.Duration(30-j) * time.Minute).UnixMilli()
			pts[j] = model.MetricPoint{
				Name:      "other_" + string(rune('a'+i)),
				Labels:    map[string]string{},
				Value:     float64(j) * float64(i+1),
				Timestamp: ts,
			}
		}
		db.Write(pts)
		srcPts := make([]model.MetricPoint, 30)
		for j := 0; j < 30; j++ {
			ts := now.Add(-time.Duration(30-j) * time.Minute).UnixMilli()
			srcPts[j] = model.MetricPoint{
				Name:      "src2",
				Labels:    map[string]string{},
				Value:     float64(j),
				Timestamp: ts,
			}
		}
		db.Write(srcPts)
	}
	c := New(db)
	res := c.AutoCorrelate("src2", "1h")
	if len(res.Related) > 5 {
		t.Errorf("expected at most 5 related metrics, got %d", len(res.Related))
	}
}

// ── HTTP API ──────────────────────────────────────────────────────────────────

func TestAPICorrelate_MissingParams(t *testing.T) {
	c := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/correlate?a=cpu", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAPICorrelate_OK(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "cpu", "mem", 1.0, 1.5, 20)
	c := New(db)
	mux := http.NewServeMux()
	RegisterRoutes(mux, c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/correlate?a=cpu&b=mem&window=1h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res CorrelationResult
	json.NewDecoder(rec.Body).Decode(&res)
	if res.MetricA != "cpu" || res.MetricB != "mem" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestAPIAutoCorrelate_MissingParam(t *testing.T) {
	c := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/correlate/auto", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAPIAutoCorrelate_OK(t *testing.T) {
	db := tsdb.New()
	writeLinear(db, "src", "other", 1.0, 1.0, 15)
	c := New(db)
	mux := http.NewServeMux()
	RegisterRoutes(mux, c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/correlate/auto?metric=src", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var res AutoCorrelationResult
	json.NewDecoder(rec.Body).Decode(&res)
	if res.SourceMetric != "src" {
		t.Errorf("expected source=src, got %s", res.SourceMetric)
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	c := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, c)
	for _, path := range []string{"/api/v1/correlate", "/api/v1/correlate/auto"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", path, rec.Code)
		}
	}
}
