package replay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

func newReplayManager(t *testing.T) (*Manager, *tsdb.TSDB, *http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	db := tsdb.New()
	t.Cleanup(db.Stop)

	m, err := New(db, dir)
	if err != nil {
		t.Fatalf("new replay manager: %v", err)
	}
	m.nowFn = func() time.Time {
		return time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
	}

	mux := http.NewServeMux()
	RegisterRoutes(mux, m)
	return m, db, mux, dir
}

func TestRecordStartStopCreatesFile(t *testing.T) {
	m, db, _, _ := newReplayManager(t)

	path, err := m.StartRecording()
	if err != nil {
		t.Fatalf("start recording: %v", err)
	}

	db.Write([]model.MetricPoint{{
		Name:      "cpu_usage_percent",
		Labels:    map[string]string{"host": "node-a"},
		Value:     42,
		Timestamp: 1000,
	}})

	stoppedPath, err := m.StopRecording()
	if err != nil {
		t.Fatalf("stop recording: %v", err)
	}
	if stoppedPath != path {
		t.Fatalf("expected %q, got %q", path, stoppedPath)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat recording: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("expected recording file to contain data")
	}
}

func TestListRecordings(t *testing.T) {
	m, db, mux, _ := newReplayManager(t)

	if _, err := m.StartRecording(); err != nil {
		t.Fatalf("start recording: %v", err)
	}
	db.Write([]model.MetricPoint{{Name: "metric_one", Value: 1, Timestamp: 1000}})
	if _, err := m.StopRecording(); err != nil {
		t.Fatalf("stop recording: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/replay/recordings", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var recordings []RecordingInfo
	if err := json.NewDecoder(rr.Body).Decode(&recordings); err != nil {
		t.Fatalf("decode recordings: %v", err)
	}
	if len(recordings) != 1 {
		t.Fatalf("expected 1 recording, got %d", len(recordings))
	}
}

func TestReplayInjectsMetrics(t *testing.T) {
	m, db, _, dir := newReplayManager(t)

	recordingsDir := filepath.Join(dir, "recordings")
	content := `{"name":"demo_metric","labels":{"host":"demo"},"value":11,"timestamp":1000}` + "\n" +
		`{"name":"demo_metric","labels":{"host":"demo"},"value":13,"timestamp":2000}` + "\n"
	if err := os.WriteFile(filepath.Join(recordingsDir, "demo.rec"), []byte(content), 0o644); err != nil {
		t.Fatalf("write demo recording: %v", err)
	}

	result, err := m.Play("demo.rec", "1x")
	if err != nil {
		t.Fatalf("play recording: %v", err)
	}
	if result.ReplayedCount != 2 {
		t.Fatalf("expected 2 replayed points, got %d", result.ReplayedCount)
	}

	points := db.QueryRange("demo_metric", map[string]string{"host": "demo"}, 0, 3000)
	if len(points) != 2 {
		t.Fatalf("expected 2 points in TSDB, got %d", len(points))
	}
}

func TestReplaySpeedMultiplierWorks(t *testing.T) {
	m, _, _, dir := newReplayManager(t)

	var slept []time.Duration
	m.sleepFn = func(d time.Duration) {
		slept = append(slept, d)
	}

	recordingsDir := filepath.Join(dir, "recordings")
	content := `{"name":"demo_metric","value":1,"timestamp":1000}` + "\n" +
		`{"name":"demo_metric","value":2,"timestamp":3000}` + "\n"
	if err := os.WriteFile(filepath.Join(recordingsDir, "speed.rec"), []byte(content), 0o644); err != nil {
		t.Fatalf("write recording: %v", err)
	}

	result, err := m.Play("speed.rec", "2x")
	if err != nil {
		t.Fatalf("play recording: %v", err)
	}
	if result.Multiplier != 2 {
		t.Fatalf("expected multiplier 2, got %v", result.Multiplier)
	}
	if len(slept) != 1 {
		t.Fatalf("expected 1 sleep call, got %d", len(slept))
	}
	if slept[0] != time.Second {
		t.Fatalf("expected 1s sleep, got %v", slept[0])
	}
}

func TestReplayAPIRoutes(t *testing.T) {
	_, db, mux, _ := newReplayManager(t)

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/replay/record/start", nil)
	startRR := httptest.NewRecorder()
	mux.ServeHTTP(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", startRR.Code)
	}

	var started map[string]string
	if err := json.NewDecoder(startRR.Body).Decode(&started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	db.Write([]model.MetricPoint{{Name: "http_requests_total", Value: 9, Timestamp: 1000}})

	stopReq := httptest.NewRequest(http.MethodPost, "/api/v1/replay/record/stop", nil)
	stopRR := httptest.NewRecorder()
	mux.ServeHTTP(stopRR, stopReq)
	if stopRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", stopRR.Code)
	}

	playReq := httptest.NewRequest(http.MethodPost, "/api/v1/replay/play?file="+started["file"]+"&speed=4x", nil)
	playRR := httptest.NewRecorder()
	mux.ServeHTTP(playRR, playReq)
	if playRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", playRR.Code, playRR.Body.String())
	}

	var result PlayResult
	if err := json.NewDecoder(playRR.Body).Decode(&result); err != nil {
		t.Fatalf("decode play response: %v", err)
	}
	if result.ReplayedCount == 0 {
		t.Fatal("expected replayed_count > 0")
	}
}
