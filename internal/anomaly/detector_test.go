package anomaly

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 辅助函数 ───────────────────────────────────────────────────────────────────

// makePoints 构造一组等间隔数据点（时间戳从 base 递增 1 秒）
func makePoints(values []float64) []model.DataPoint {
	base := time.Now().UnixMilli()
	pts := make([]model.DataPoint, len(values))
	for i, v := range values {
		pts[i] = model.DataPoint{Timestamp: base + int64(i)*1000, Value: v}
	}
	return pts
}

// ── checkSeries 单元测试 ───────────────────────────────────────────────────────

func TestCheckSeries_DetectsSpike(t *testing.T) {
	// 基线：均值≈50，有轻微波动（保证 stddev>0），最新值突然升到 500（Z-score >> 3）
	vals := []float64{48, 51, 49, 52, 50, 47, 53, 50, 51, 49,
		48, 52, 50, 51, 48, 53, 49, 52, 50, 48,
		51, 49, 52, 50, 47, 53, 50, 51, 49, 500}
	pts := makePoints(vals)

	ev := checkSeries("cpu", map[string]string{"host": "srv1"}, pts)
	if ev == nil {
		t.Fatal("expected anomaly event for spike, got nil")
	}
	if ev.Value != 500.0 {
		t.Errorf("expected value=500, got %f", ev.Value)
	}
	if ev.DeviationScore < zThreshold {
		t.Errorf("expected deviation_score >= %f, got %f", zThreshold, ev.DeviationScore)
	}
}

func TestCheckSeries_NoFalsePositiveOnNormal(t *testing.T) {
	// 值在正常范围内波动（5±2），不应触发异常
	vals := []float64{3, 4, 5, 6, 5, 4, 5, 6, 5, 4, 5}
	pts := makePoints(vals)

	ev := checkSeries("mem", map[string]string{}, pts)
	if ev != nil {
		t.Errorf("expected no anomaly for normal data, got %+v", ev)
	}
}

func TestCheckSeries_InsufficientData(t *testing.T) {
	// 少于 2 个点：直接返回 nil
	pts := makePoints([]float64{100.0})
	ev := checkSeries("cpu", map[string]string{}, pts)
	if ev != nil {
		t.Error("expected nil for single data point")
	}
}

func TestCheckSeries_FlatLine_NoFalsePositive(t *testing.T) {
	// 标准差为零时不应触发（避免除零误报）
	vals := make([]float64, 20)
	for i := range vals {
		vals[i] = 42.0
	}
	pts := makePoints(vals)
	ev := checkSeries("metric", map[string]string{}, pts)
	if ev != nil {
		t.Error("expected no anomaly for flat-line data")
	}
}

// ── severity 分类 ──────────────────────────────────────────────────────────────

func TestClassifySeverity_Low(t *testing.T) {
	if classifySeverity(3.1) != SeverityLow {
		t.Error("expected low for z=3.1")
	}
}

func TestClassifySeverity_Medium(t *testing.T) {
	if classifySeverity(4.0) != SeverityMedium {
		t.Error("expected medium for z=4.0")
	}
}

func TestClassifySeverity_High(t *testing.T) {
	if classifySeverity(6.0) != SeverityHigh {
		t.Error("expected high for z=6.0")
	}
}

// ── Detector 集成测试 ─────────────────────────────────────────────────────────

func TestDetector_RingBufferLimit(t *testing.T) {
	d := New(tsdb.New())
	// 直接注入超过上限的事件
	for i := 0; i < maxEvents+10; i++ {
		d.addEvent(AnomalyEvent{MetricName: "m", DeviationScore: 4.0, Severity: SeverityLow})
	}
	if d.Count() > maxEvents {
		t.Errorf("ring buffer exceeded maxEvents: got %d, want <= %d", d.Count(), maxEvents)
	}
}

func TestDetector_GetEvents_Filter(t *testing.T) {
	d := New(tsdb.New())
	d.addEvent(AnomalyEvent{MetricName: "cpu", DeviationScore: 4.0, Severity: SeverityLow})
	d.addEvent(AnomalyEvent{MetricName: "mem", DeviationScore: 5.0, Severity: SeverityHigh})

	cpuEvents := d.GetEvents("cpu")
	if len(cpuEvents) != 1 || cpuEvents[0].MetricName != "cpu" {
		t.Errorf("unexpected cpu events: %v", cpuEvents)
	}
	allEvents := d.GetEvents("")
	if len(allEvents) != 2 {
		t.Errorf("expected 2 total events, got %d", len(allEvents))
	}
}

func TestDetector_DetectWritesToBuffer(t *testing.T) {
	db := tsdb.New()
	now := time.Now().UnixMilli()
	// 写入基线（均值≈50，有轻微波动以保证标准差非零）和一个明显的异常点（值=500）
	baseVals := []float64{48, 51, 49, 52, 50, 47, 53, 50, 51, 49,
		48, 52, 50, 51, 48, 53, 49, 52, 50, 48,
		51, 49, 52, 50, 47, 53, 50, 51, 49}
	points := make([]model.MetricPoint, len(baseVals)+1)
	for i, v := range baseVals {
		points[i] = model.MetricPoint{
			Name:      "request_rate",
			Labels:    map[string]string{},
			Value:     v,
			Timestamp: now - int64(len(baseVals)-i)*10000,
		}
	}
	// 最后一个点为异常值，偏离基线 >10 个标准差
	points[len(baseVals)] = model.MetricPoint{
		Name:      "request_rate",
		Labels:    map[string]string{},
		Value:     500.0,
		Timestamp: now,
	}
	db.Write(points)

	d := New(db)
	d.detect()

	events := d.GetEvents("request_rate")
	if len(events) == 0 {
		t.Fatal("expected at least 1 anomaly event after detect()")
	}
	if events[0].Severity == "" {
		t.Error("expected non-empty severity")
	}
}

func TestDetector_StartStop(t *testing.T) {
	d := New(tsdb.New())
	d.Start()
	time.Sleep(20 * time.Millisecond)
	d.Stop()
	// 停止后读取不应 panic
	_ = d.GetEvents("")
}

// ── HTTP API ──────────────────────────────────────────────────────────────────

func TestAPIGetAnomalies_Empty(t *testing.T) {
	d := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var events []AnomalyEvent
	json.NewDecoder(rec.Body).Decode(&events)
	if events == nil {
		t.Error("expected non-nil (empty) slice")
	}
}

func TestAPIGetAnomalies_FilterByMetric(t *testing.T) {
	d := New(tsdb.New())
	d.addEvent(AnomalyEvent{MetricName: "cpu", DeviationScore: 4.0, Severity: SeverityLow})
	d.addEvent(AnomalyEvent{MetricName: "mem", DeviationScore: 5.0, Severity: SeverityHigh})

	mux := http.NewServeMux()
	RegisterRoutes(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anomalies?metric=cpu", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var events []AnomalyEvent
	json.NewDecoder(rec.Body).Decode(&events)
	if len(events) != 1 || events[0].MetricName != "cpu" {
		t.Errorf("expected 1 cpu event, got %v", events)
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	d := New(tsdb.New())
	mux := http.NewServeMux()
	RegisterRoutes(mux, d)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/anomalies", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
