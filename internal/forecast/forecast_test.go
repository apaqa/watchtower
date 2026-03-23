package forecast

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

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

// makePoints 构造等间隔（1 秒）数据点序列，以当前时间为起点
func makePoints(values []float64) []model.DataPoint {
	base := time.Now().Add(-time.Duration(len(values)) * time.Second).UnixMilli()
	pts := make([]model.DataPoint, len(values))
	for i, v := range values {
		pts[i] = model.DataPoint{Timestamp: base + int64(i)*1000, Value: v}
	}
	return pts
}

// writeMetric 将给定数据点写入 TSDB
func writeMetric(db *tsdb.TSDB, name string, points []model.DataPoint) {
	mp := make([]model.MetricPoint, len(points))
	for i, p := range points {
		mp[i] = model.MetricPoint{
			Name:      name,
			Labels:    map[string]string{},
			Value:     p.Value,
			Timestamp: p.Timestamp,
		}
	}
	db.Write(mp)
}

// ── linearRegression 单元测试 ─────────────────────────────────────────────────

func TestLinearRegression_KnownData(t *testing.T) {
	// y = 2x + 5（x 为秒），期望斜率≈2，截距≈5，R²≈1
	base := int64(1000000000000) // 固定基准时间戳（毫秒）
	pts := make([]model.DataPoint, 10)
	for i := range pts {
		xSec := float64(i * 10) // 每 10 秒一个点
		pts[i] = model.DataPoint{
			Timestamp: base + int64(i*10)*1000,
			Value:     2*xSec + 5,
		}
	}

	params, err := linearRegression(pts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if math.Abs(params.slope-2.0) > 0.001 {
		t.Errorf("slope: want 2.0, got %.6f", params.slope)
	}
	// intercept 是相对于 t0 的截距（t0 时 x=0，所以 intercept 应≈5）
	if math.Abs(params.intercept-5.0) > 0.001 {
		t.Errorf("intercept: want 5.0, got %.6f", params.intercept)
	}
	if params.r2 < 0.9999 {
		t.Errorf("R² should be ~1.0 for perfect linear data, got %.6f", params.r2)
	}
}

func TestLinearRegression_R2NoisyData(t *testing.T) {
	// 带噪声的线性数据：R² 应 < 1 但 > 0
	pts := makePoints([]float64{1, 3, 2, 5, 4, 7, 6, 9, 8, 11, 10, 13})
	params, err := linearRegression(pts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params.r2 <= 0 || params.r2 >= 1.0 {
		t.Errorf("expected 0 < R² < 1 for noisy data, got %.6f", params.r2)
	}
	if params.slope <= 0 {
		t.Errorf("expected positive slope for increasing noisy data, got %.6f", params.slope)
	}
}

func TestLinearRegression_FlatData_R2Zero(t *testing.T) {
	// 水平线：斜率≈0，R²=0（SS_tot≈0）
	vals := make([]float64, 10)
	for i := range vals {
		vals[i] = 42.0
	}
	params, err := linearRegression(makePoints(vals))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if math.Abs(params.slope) > 1e-8 {
		t.Errorf("expected slope≈0 for flat data, got %.10f", params.slope)
	}
	if params.r2 != 0 {
		t.Errorf("expected R²=0 for flat data, got %.6f", params.r2)
	}
}

// ── Forecaster.Forecast 测试 ──────────────────────────────────────────────────

func TestForecast_IncreasingTrend(t *testing.T) {
	db := tsdb.New()
	// 生成单调递增序列（每秒 +1，当前值 ~30）
	vals := make([]float64, 30)
	for i := range vals {
		vals[i] = float64(i + 1)
	}
	writeMetric(db, "cpu_usage_percent", makePoints(vals))

	f := New(db)
	result, err := f.Forecast("cpu_usage_percent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Trend != TrendIncreasing {
		t.Errorf("expected trend=increasing, got %s", result.Trend)
	}
	// 24h 预测值应远高于当前值
	if result.Predicted24h <= result.CurrentValue {
		t.Errorf("predicted_24h (%.2f) should be > current (%.2f) for increasing trend",
			result.Predicted24h, result.CurrentValue)
	}
	if result.ConfidenceScore < 0.99 {
		t.Errorf("R² should be near 1.0 for perfectly linear data, got %.4f", result.ConfidenceScore)
	}
}

func TestForecast_DecreasingTrend(t *testing.T) {
	db := tsdb.New()
	// 生成单调递减序列（从 80 降到 51）
	vals := make([]float64, 30)
	for i := range vals {
		vals[i] = 80 - float64(i)
	}
	writeMetric(db, "memory_usage_percent", makePoints(vals))

	f := New(db)
	result, err := f.Forecast("memory_usage_percent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Trend != TrendDecreasing {
		t.Errorf("expected trend=decreasing, got %s", result.Trend)
	}
}

func TestForecast_StableTrend(t *testing.T) {
	db := tsdb.New()
	// 完全水平序列（斜率为 0），期望趋势为 stable
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 50.0
	}
	writeMetric(db, "disk_usage_percent", makePoints(vals))

	f := New(db)
	result, err := f.Forecast("disk_usage_percent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Trend != TrendStable {
		t.Errorf("expected trend=stable for near-flat data, got %s", result.Trend)
	}
}

func TestForecast_InsufficientData_ReturnsError(t *testing.T) {
	db := tsdb.New()
	// 写入 3 个点（< minPoints=5）
	writeMetric(db, "cpu_usage_percent", makePoints([]float64{10, 20, 30}))

	f := New(db)
	_, err := f.Forecast("cpu_usage_percent")
	if err == nil {
		t.Fatal("expected error for insufficient data, got nil")
	}
}

func TestForecast_MetricNotFound_ReturnsError(t *testing.T) {
	f := New(tsdb.New())
	_, err := f.Forecast("nonexistent_metric")
	if err == nil {
		t.Fatal("expected error for unknown metric, got nil")
	}
}

// ── Forecaster.Exhaustion 测试 ────────────────────────────────────────────────

func TestExhaustion_Accuracy(t *testing.T) {
	db := tsdb.New()
	// 磁盘从 50% 线性增长，每秒 +1%
	// 在 40 秒后达到 90%（阈值）
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 50 + float64(i)
	}
	writeMetric(db, "disk_usage_percent", makePoints(vals))

	f := New(db)
	est, err := f.Exhaustion("disk_usage_percent", 90.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !est.Reachable {
		t.Fatal("expected reachable=true for increasing disk usage")
	}
	// 当前约 69%，距 90% 还有约 21 秒（≈0.0058 小时）
	if est.HoursUntilFull <= 0 {
		t.Errorf("expected hours_until_threshold > 0, got %.4f", est.HoursUntilFull)
	}
	if est.ExhaustedAtMs == 0 {
		t.Error("expected non-zero exhausted_at_ms")
	}
}

func TestExhaustion_NotReachable_ForDecreasingMetric(t *testing.T) {
	db := tsdb.New()
	// 内存使用率持续下降，不会到达 90%
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 80 - float64(i)
	}
	writeMetric(db, "memory_usage_percent", makePoints(vals))

	f := New(db)
	est, err := f.Exhaustion("memory_usage_percent", 90.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if est.Reachable {
		t.Error("expected reachable=false for decreasing metric")
	}
}

// ── CapacityReport 测试 ────────────────────────────────────────────────────────

func TestCapacityReport_AllMetricsPresent(t *testing.T) {
	db := tsdb.New()
	// 写入足够的 CPU/内存/磁盘数据
	cpuVals := make([]float64, 20)
	memVals := make([]float64, 20)
	diskVals := make([]float64, 20)
	for i := range cpuVals {
		cpuVals[i] = 30 + float64(i)*0.5
		memVals[i] = 50 + float64(i)*0.2
		diskVals[i] = 40 + float64(i)*0.1
	}
	writeMetric(db, "cpu_usage_percent", makePoints(cpuVals))
	writeMetric(db, "memory_usage_percent", makePoints(memVals))
	writeMetric(db, "disk_usage_percent", makePoints(diskVals))

	f := New(db)
	report := f.GenerateReport()

	if report.GeneratedAtMs == 0 {
		t.Error("expected non-zero generated_at_ms")
	}
	if report.CPU.Name != metricCPU {
		t.Errorf("CPU.Name: want %s, got %s", metricCPU, report.CPU.Name)
	}
	if report.Memory.Name != metricMemory {
		t.Errorf("Memory.Name: want %s, got %s", metricMemory, report.Memory.Name)
	}
	if report.Disk.Name != metricDisk {
		t.Errorf("Disk.Name: want %s, got %s", metricDisk, report.Disk.Name)
	}
}

func TestCapacityReport_HealthStatus_Critical(t *testing.T) {
	db := tsdb.New()
	// CPU 快速增长到 95%，预计 24h 后远超 90% → critical
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 88 + float64(i)*0.5 // 从 88% 涨到 97.5%
	}
	writeMetric(db, "cpu_usage_percent", makePoints(vals))
	// 内存和磁盘低位稳定
	stableVals := make([]float64, 20)
	for i := range stableVals {
		stableVals[i] = 30.0
	}
	writeMetric(db, "memory_usage_percent", makePoints(stableVals))
	writeMetric(db, "disk_usage_percent", makePoints(stableVals))

	f := New(db)
	report := f.GenerateReport()

	if report.CPU.HealthStatus != HealthCritical {
		t.Errorf("expected cpu health=critical for rapidly increasing CPU, got %s", report.CPU.HealthStatus)
	}
	if report.Memory.HealthStatus != HealthHealthy {
		t.Errorf("expected memory health=healthy for stable 30%%, got %s", report.Memory.HealthStatus)
	}
}

func TestCapacityReport_NoData_Healthy(t *testing.T) {
	// 无任何数据时报告应返回而不 panic，且 health=healthy（默认）
	f := New(tsdb.New())
	report := f.GenerateReport()

	if report.CPU.HealthStatus != HealthHealthy {
		t.Errorf("expected healthy when no data, got %s", report.CPU.HealthStatus)
	}
}

// ── HTTP API 测试 ─────────────────────────────────────────────────────────────

func TestAPIForecast_Success(t *testing.T) {
	db := tsdb.New()
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = float64(i) * 2
	}
	writeMetric(db, "cpu_usage_percent", makePoints(vals))

	mux := http.NewServeMux()
	RegisterRoutes(mux, New(db))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/forecast?metric=cpu_usage_percent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result ForecastResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if result.MetricName != "cpu_usage_percent" {
		t.Errorf("unexpected metric name: %s", result.MetricName)
	}
}

func TestAPIForecast_WithThreshold(t *testing.T) {
	db := tsdb.New()
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 50 + float64(i)
	}
	writeMetric(db, "disk_usage_percent", makePoints(vals))

	mux := http.NewServeMux()
	RegisterRoutes(mux, New(db))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/forecast?metric=disk_usage_percent&threshold=90", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var est ExhaustionEstimate
	if err := json.NewDecoder(rec.Body).Decode(&est); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if est.Threshold != 90 {
		t.Errorf("expected threshold=90, got %.1f", est.Threshold)
	}
}

func TestAPIForecast_MissingMetric_BadRequest(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, New(tsdb.New()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/forecast", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAPICapacity_Success(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, New(tsdb.New()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capacity", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var report CapacityReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if report.GeneratedAtMs == 0 {
		t.Error("expected non-zero generated_at_ms")
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, New(tsdb.New()))

	for _, path := range []string{"/api/v1/forecast?metric=cpu", "/api/v1/capacity"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("path %s: expected 405, got %d", path, rec.Code)
		}
	}
}
