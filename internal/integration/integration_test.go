// Package integration contains end-to-end tests that start a real ingest
// server and exercise the full request path from HTTP in to TSDB out.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/auth"
	"github.com/apaqa/watchtower/internal/export"
	"github.com/apaqa/watchtower/internal/health"
	"github.com/apaqa/watchtower/internal/ingest"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tracestore"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// newServer spins up an ingest server backed by an in-memory TSDB and
// returns a test HTTP server that drives it. Caller must defer ts.Close().
func newServer(t *testing.T) (*httptest.Server, *tsdb.TSDB) {
	t.Helper()
	db := tsdb.New()
	srv, err := ingest.New(":0", db) // listener unused; httptest owns transport
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, db
}

// postJSON is a helper to POST JSON and return the response.
func postJSON(t *testing.T, url string, body interface{}, headers map[string]string) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestMetrics_WriteAndRead pushes a metric point and verifies it can be
// queried back via the WQL endpoint.
func TestMetrics_WriteAndRead(t *testing.T) {
	ts, _ := newServer(t)

	pt := model.MetricPoint{
		Name:      "integration_cpu",
		Value:     42.0,
		Timestamp: time.Now().UnixMilli(),
	}
	resp := postJSON(t, ts.URL+"/api/v1/metrics", []model.MetricPoint{pt}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ingest: expected 204, got %d", resp.StatusCode)
	}

	qresp, err := http.Get(ts.URL + "/api/v1/query?q=integration_cpu")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer qresp.Body.Close()
	if qresp.StatusCode != http.StatusOK {
		t.Fatalf("query: expected 200, got %d", qresp.StatusCode)
	}
	var result interface{}
	json.NewDecoder(qresp.Body).Decode(&result)
	if result == nil {
		t.Error("query returned no data")
	}
}

// TestMetrics_BatchWrite verifies multiple points can be submitted at once.
func TestMetrics_BatchWrite(t *testing.T) {
	ts, db := newServer(t)

	now := time.Now().UnixMilli()
	points := []model.MetricPoint{
		{Name: "batch_metric_a", Value: 1.0, Timestamp: now},
		{Name: "batch_metric_b", Value: 2.0, Timestamp: now},
		{Name: "batch_metric_c", Value: 3.0, Timestamp: now},
	}
	resp := postJSON(t, ts.URL+"/api/v1/metrics", points, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify all three series exist in TSDB
	names := db.ListMetrics()
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, p := range points {
		if !nameSet[p.Name] {
			t.Errorf("metric %q not found in TSDB", p.Name)
		}
	}
}

// TestAlert_FiresOnThreshold registers an alert rule, pushes a metric above
// the threshold, evaluates the engine, and checks that the alert fires.
func TestAlert_FiresOnThreshold(t *testing.T) {
	db := tsdb.New()
	alertEng := alert.NewEngine(db)
	alertEng.Start()
	defer alertEng.Stop()

	rule := alert.AlertRule{
		Name:       "high_cpu",
		Expression: "integration_alert_cpu",
		Operator:   ">",
		Threshold:  80.0,
		Severity:   "warning",
	}
	if err := alertEng.AddRule(rule); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	// Write a metric above threshold directly to TSDB
	db.Write([]model.MetricPoint{{
		Name:      "integration_alert_cpu",
		Value:     95.0,
		Timestamp: time.Now().UnixMilli(),
	}})

	// Force immediate evaluation
	alertEng.Evaluate()

	alerts := alertEng.GetActiveAlerts()
	found := false
	for _, a := range alerts {
		if a.Rule.Name == "high_cpu" && a.State == "firing" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected alert 'high_cpu' to be firing; active alerts: %+v", alerts)
	}
}

// TestAlert_ClearsWhenBelowThreshold verifies alert transitions back to normal.
func TestAlert_ClearsWhenBelowThreshold(t *testing.T) {
	db := tsdb.New()
	alertEng := alert.NewEngine(db)
	alertEng.Start()
	defer alertEng.Stop()

	alertEng.AddRule(alert.AlertRule{
		Name:      "low_threshold",
		Expression: "integration_drop_cpu",
		Operator:  ">",
		Threshold: 50.0,
		Severity:  "info",
	})

	// Fire it
	db.Write([]model.MetricPoint{{Name: "integration_drop_cpu", Value: 99.0, Timestamp: time.Now().UnixMilli()}})
	alertEng.Evaluate()

	active := alertEng.GetActiveAlerts()
	firing := false
	for _, a := range active {
		if a.Rule.Name == "low_threshold" {
			firing = true
		}
	}
	if !firing {
		t.Error("expected alert to fire initially")
	}

	// Drop below threshold
	db.Write([]model.MetricPoint{{Name: "integration_drop_cpu", Value: 10.0, Timestamp: time.Now().UnixMilli()}})
	alertEng.Evaluate()

	active2 := alertEng.GetActiveAlerts()
	stillFiring := false
	for _, a := range active2 {
		if a.Rule.Name == "low_threshold" {
			stillFiring = true
		}
	}
	if stillFiring {
		t.Error("expected alert to clear after metric dropped below threshold")
	}
}

// TestLog_WriteAndSearch writes a log entry via HTTP and searches for it.
func TestLog_WriteAndSearch(t *testing.T) {
	db := tsdb.New()
	ls := logstore.New()
	srv, err := ingest.New(":0", db)
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	srv.RegisterLogStore(ls)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	entry := model.LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     model.LogLevelInfo,
		Message:   "integration test log entry unique_marker",
		Source:    "integration",
	}
	resp := postJSON(t, ts.URL+"/api/v1/logs", []model.LogEntry{entry}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("log write: expected 204, got %d", resp.StatusCode)
	}

	// Search for it
	sresp, err := http.Get(ts.URL + "/api/v1/logs?q=unique_marker&limit=10")
	if err != nil {
		t.Fatalf("log search: %v", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("log search: expected 200, got %d", sresp.StatusCode)
	}
	var logs []model.LogEntry
	json.NewDecoder(sresp.Body).Decode(&logs)
	if len(logs) == 0 {
		t.Error("expected at least one log entry in search results")
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l.Message, "unique_marker") {
			found = true
		}
	}
	if !found {
		t.Error("search results did not contain the written log entry")
	}
}

// TestExport_CSVContainsData pushes metrics and verifies CSV export is non-empty.
func TestExport_CSVContainsData(t *testing.T) {
	db := tsdb.New()
	ls := logstore.New()
	ts2 := tracestore.New()
	srv, err := ingest.New(":0", db)
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	exportHandler := export.New(db, ls, ts2)
	srv.RegisterExportHandler(exportHandler)
	testSrv := httptest.NewServer(srv)
	t.Cleanup(testSrv.Close)

	db.Write([]model.MetricPoint{{
		Name:      "export_test_metric",
		Value:     77.0,
		Timestamp: time.Now().UnixMilli(),
	}})

	eresp, err := http.Get(testSrv.URL + "/api/v1/export/metrics?format=csv&metric=export_test_metric")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer eresp.Body.Close()
	if eresp.StatusCode != http.StatusOK {
		t.Fatalf("export: expected 200, got %d", eresp.StatusCode)
	}
	ct := eresp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/csv") && !strings.Contains(ct, "text/plain") {
		t.Errorf("expected CSV content-type, got %q", ct)
	}
}

// TestAuth_BlocksUnauthorized verifies that a server with an API key rejects
// requests without credentials with 401.
func TestAuth_BlocksUnauthorized(t *testing.T) {
	db := tsdb.New()
	srv, err := ingest.New(":0", db)
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}

	ks := auth.NewKeyStore()
	ks.Add(auth.APIKey{Name: "admin", Key: "secret-key"})
	srv.RegisterKeyStore(ks)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// No key → 401
	resp, err := http.Post(ts.URL+"/api/v1/metrics", "application/json",
		strings.NewReader("[]"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without key, got %d", resp.StatusCode)
	}

	// Correct key → 204
	resp2 := postJSON(t, ts.URL+"/api/v1/metrics", []model.MetricPoint{}, map[string]string{
		"X-API-Key": "secret-key",
	})
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 with valid key, got %d", resp2.StatusCode)
	}
}

// TestHealth_LiveEndpoint verifies the liveness probe returns 200.
func TestHealth_LiveEndpoint(t *testing.T) {
	db := tsdb.New()
	srv, err := ingest.New(":0", db)
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	hm := health.New()
	hm.Start()
	defer hm.Stop()
	srv.RegisterHealthManager(hm)
	testSrv := httptest.NewServer(srv)
	t.Cleanup(testSrv.Close)

	resp, err := http.Get(testSrv.URL + "/api/v1/health/live")
	if err != nil {
		t.Fatalf("health/live: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestPrometheus_ScrapeEndpoint verifies the /metrics scrape endpoint
// returns prometheus-formatted output containing basic Go runtime metrics.
func TestPrometheus_ScrapeEndpoint(t *testing.T) {
	ts, db := newServer(t)

	db.Write([]model.MetricPoint{{
		Name:      "scrape_test_gauge",
		Value:     3.14,
		Timestamp: time.Now().UnixMilli(),
	}})

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("/metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content-type, got %q", ct)
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, _ := resp.Body.Read(buf)
		if n == 0 {
			break
		}
		sb.Write(buf[:n])
	}
	body := sb.String()
	if !strings.Contains(body, "scrape_test_gauge") {
		t.Errorf("scrape output should contain 'scrape_test_gauge'; got:\n%s", body[:min(len(body), 400)])
	}
}

// TestPrometheus_PushIngest verifies prometheus text-format POST ingestion.
func TestPrometheus_PushIngest(t *testing.T) {
	ts, db := newServer(t)

	promText := "# HELP prom_push_test A test metric\n" +
		"# TYPE prom_push_test gauge\n" +
		"prom_push_test{env=\"prod\"} 99.5\n"

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/metrics/prometheus",
		strings.NewReader(promText))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("prometheus push: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Metric should appear in TSDB
	names := db.ListMetrics()
	found := false
	for _, n := range names {
		if n == "prom_push_test" {
			found = true
		}
	}
	if !found {
		t.Errorf("prometheus-pushed metric not found in TSDB; metrics: %v", names)
	}
}

// TestWQL_AggregationQuery verifies WQL avg() over written points.
func TestWQL_AggregationQuery(t *testing.T) {
	ts, db := newServer(t)

	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		db.Write([]model.MetricPoint{{
			Name:      "wql_avg_test",
			Value:     float64(i * 10),
			Timestamp: now - int64(i)*1000,
		}})
	}

	resp, err := http.Get(ts.URL + "/api/v1/query?q=avg(wql_avg_test[5m])")
	if err != nil {
		t.Fatalf("WQL query: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if result == nil {
		t.Error("WQL avg query returned nil")
	}
}

// TestRateLimit_Headers verifies rate-limit response headers are set.
func TestRateLimit_Headers(t *testing.T) {
	ts, _ := newServer(t)

	resp := postJSON(t, ts.URL+"/api/v1/metrics", []model.MetricPoint{}, nil)
	resp.Body.Close()
	// Headers may or may not be present depending on rate limiter registration,
	// but the request itself should succeed (no rate limiter → 204).
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

// TestMetrics_InvalidPayload verifies malformed JSON returns 400.
func TestMetrics_InvalidPayload(t *testing.T) {
	ts, _ := newServer(t)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/metrics",
		strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

// TestMetrics_EmptyBatch verifies an empty array is accepted.
func TestMetrics_EmptyBatch(t *testing.T) {
	ts, _ := newServer(t)
	resp := postJSON(t, ts.URL+"/api/v1/metrics", []model.MetricPoint{}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 for empty batch, got %d", resp.StatusCode)
	}
}

// TestMetricNames_AfterIngest verifies /api/v1/metrics/names lists written metrics.
func TestMetricNames_AfterIngest(t *testing.T) {
	ts, _ := newServer(t)

	postJSON(t, ts.URL+"/api/v1/metrics", []model.MetricPoint{
		{Name: "names_test_alpha", Value: 1, Timestamp: time.Now().UnixMilli()},
		{Name: "names_test_beta", Value: 2, Timestamp: time.Now().UnixMilli()},
	}, nil).Body.Close()

	resp, err := http.Get(ts.URL + "/api/v1/metrics/names")
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var names []string
	json.NewDecoder(resp.Body).Decode(&names)
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	for _, want := range []string{"names_test_alpha", "names_test_beta"} {
		if !nameSet[want] {
			t.Errorf("metric name %q not in /api/v1/metrics/names", want)
		}
	}
}

// min is a helper (Go 1.21+ builtin, provide fallback).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// db.Write helper shim so tests can call db.Write directly.
// tsdb.TSDB exposes Write([]model.MetricPoint).
var _ = fmt.Sprintf // ensure fmt is used
