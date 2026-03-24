package compare

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

func writeMetricSeries(db *tsdb.TSDB, metric string, values []float64, start time.Time, step time.Duration) {
	points := make([]model.MetricPoint, len(values))
	for i, v := range values {
		points[i] = model.MetricPoint{
			Name:      metric,
			Labels:    map[string]string{},
			Value:     v,
			Timestamp: start.Add(time.Duration(i) * step).UnixMilli(),
		}
	}
	db.Write(points)
}

func TestCompareCalculatesWindowAverages(t *testing.T) {
	db := tsdb.New()
	now := time.Now()
	writeMetricSeries(db, "cpu_usage_percent", []float64{10, 10, 10, 20, 20, 20}, now.Add(-55*time.Minute), 10*time.Minute)

	engine := New(db)
	result, err := engine.Compare("cpu_usage_percent", "30m", "30m")
	if err != nil {
		t.Fatalf("expected compare to succeed, got %v", err)
	}

	if math.Abs(result.CurrentAvg-20) > 0.001 {
		t.Fatalf("expected current avg 20, got %.4f", result.CurrentAvg)
	}
	if math.Abs(result.PreviousAvg-10) > 0.001 {
		t.Fatalf("expected previous avg 10, got %.4f", result.PreviousAvg)
	}
}

func TestPercentChangeIncrease(t *testing.T) {
	got := percentChange(10, 15)
	if math.Abs(got-50) > 0.001 {
		t.Fatalf("expected 50%%, got %.4f", got)
	}
}

func TestPercentChangeFromZero(t *testing.T) {
	got := percentChange(0, 12)
	if got != 100 {
		t.Fatalf("expected 100%% fallback, got %.4f", got)
	}
}

func TestDetectDirectionStable(t *testing.T) {
	if detectDirection(0.4) != DirectionStable {
		t.Fatalf("expected stable direction")
	}
}

func TestReportGenerationIncludesMetrics(t *testing.T) {
	db := tsdb.New()
	now := time.Now()
	writeMetricSeries(db, "cpu_usage_percent", []float64{10, 10, 20, 20}, now.Add(-55*time.Minute), 15*time.Minute)
	writeMetricSeries(db, "memory_usage_percent", []float64{30, 30, 30, 30}, now.Add(-55*time.Minute), 15*time.Minute)

	engine := New(db)
	report, err := engine.Report("30m", "30m")
	if err != nil {
		t.Fatalf("expected report to succeed, got %v", err)
	}

	if len(report.Results) != 2 {
		t.Fatalf("expected 2 report results, got %d", len(report.Results))
	}
}

func TestDetectCUSUMOnSyntheticData(t *testing.T) {
	points := make([]model.DataPoint, 0, 40)
	base := time.Now().Add(-40 * time.Minute)
	for i := 0; i < 20; i++ {
		points = append(points, model.DataPoint{Timestamp: base.Add(time.Duration(i) * time.Minute).UnixMilli(), Value: 10})
	}
	for i := 20; i < 40; i++ {
		points = append(points, model.DataPoint{Timestamp: base.Add(time.Duration(i) * time.Minute).UnixMilli(), Value: 50})
	}

	cp := detectCUSUMChange("cpu_usage_percent", points)
	if cp == nil {
		t.Fatal("expected change point to be detected")
	}
	if cp.BeforeMean >= cp.AfterMean {
		t.Fatalf("expected increasing shift, got before=%.2f after=%.2f", cp.BeforeMean, cp.AfterMean)
	}
}

func TestDetectCUSUMNoFalsePositiveOnStableData(t *testing.T) {
	points := make([]model.DataPoint, 0, 40)
	base := time.Now().Add(-40 * time.Minute)
	for i := 0; i < 40; i++ {
		points = append(points, model.DataPoint{
			Timestamp: base.Add(time.Duration(i) * time.Minute).UnixMilli(),
			Value:     100 + math.Sin(float64(i))*0.05,
		})
	}

	cp := detectCUSUMChange("memory_usage_percent", points)
	if cp != nil {
		t.Fatalf("expected no change point, got %+v", cp)
	}
}

func TestCompareAPIAndChangesAPI(t *testing.T) {
	db := tsdb.New()
	now := time.Now()
	writeMetricSeries(db, "disk_usage_percent", []float64{50, 50, 60, 60}, now.Add(-55*time.Minute), 15*time.Minute)

	engine := New(db)
	detector := NewChangeDetector(db)
	detector.add(ChangePoint{Metric: "disk_usage_percent", Timestamp: now.UnixMilli(), BeforeMean: 50, AfterMean: 60, SignificanceScore: 8.2})

	mux := http.NewServeMux()
	RegisterRoutes(mux, engine, detector)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/compare?metric=disk_usage_percent&current=30m&previous=30m", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected compare api 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var cmp CompareResult
	if err := json.NewDecoder(rec.Body).Decode(&cmp); err != nil {
		t.Fatalf("expected valid compare json, got %v", err)
	}
	if cmp.Metric != "disk_usage_percent" {
		t.Fatalf("unexpected compare result: %+v", cmp)
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/changes", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected changes api 200, got %d", rec2.Code)
	}
	var changes []ChangePoint
	if err := json.NewDecoder(rec2.Body).Decode(&changes); err != nil {
		t.Fatalf("expected valid changes json, got %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change point, got %d", len(changes))
	}
}

func TestCompareReportAPI(t *testing.T) {
	db := tsdb.New()
	now := time.Now()
	writeMetricSeries(db, "cpu_usage_percent", []float64{10, 10, 15, 15}, now.Add(-55*time.Minute), 15*time.Minute)

	engine := New(db)
	detector := NewChangeDetector(db)
	mux := http.NewServeMux()
	RegisterRoutes(mux, engine, detector)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/compare/report?current=30m&previous=30m", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected report api 200, got %d", rec.Code)
	}

	var report Report
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("expected valid report json, got %v", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("expected 1 report row, got %d", len(report.Results))
	}
}
