package procmon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 基础采集 ───────────────────────────────────────────────────────────────────

func TestCollect_ReturnsProcesses(t *testing.T) {
	m := New(nil)
	m.collect()
	procs := m.GetProcesses("cpu")
	if len(procs) == 0 {
		t.Fatal("expected at least 1 process from real OS")
	}
}

func TestCollect_SortByCPU(t *testing.T) {
	m := New(nil)
	m.collect()
	procs := m.GetProcesses("cpu")
	for i := 1; i < len(procs); i++ {
		if procs[i].CPUPercent > procs[i-1].CPUPercent {
			t.Errorf("CPU sort broken at index %d: %.2f > %.2f", i, procs[i].CPUPercent, procs[i-1].CPUPercent)
		}
	}
}

func TestCollect_SortByMemory(t *testing.T) {
	m := New(nil)
	m.collect()
	procs := m.GetProcesses("memory")
	for i := 1; i < len(procs); i++ {
		if procs[i].MemoryMB > procs[i-1].MemoryMB {
			t.Errorf("memory sort broken at index %d: %.2f > %.2f", i, procs[i].MemoryMB, procs[i-1].MemoryMB)
		}
	}
}

func TestCollect_LimitToMaxProcesses(t *testing.T) {
	m := New(nil)
	m.collect()
	procs := m.GetProcesses("cpu")
	if len(procs) > maxProcesses {
		t.Errorf("expected at most %d processes, got %d", maxProcesses, len(procs))
	}
}

func TestCollect_ProcessInfo_Fields(t *testing.T) {
	m := New(nil)
	m.collect()
	procs := m.GetProcesses("cpu")
	if len(procs) == 0 {
		t.Skip("no processes collected")
	}
	p := procs[0]
	if p.PID <= 0 {
		t.Errorf("expected positive PID, got %d", p.PID)
	}
	if p.Name == "" {
		t.Error("expected non-empty process name")
	}
}

func TestCollect_WritesMetricsToDB(t *testing.T) {
	db := tsdb.New()
	m := New(db)
	m.collect()

	// 应当有 process_cpu 和 process_memory_mb 指标被写入
	cpuSeries := db.GetSeries("process_cpu")
	memSeries := db.GetSeries("process_memory_mb")
	if len(cpuSeries) == 0 {
		t.Error("expected process_cpu metric written to TSDB")
	}
	if len(memSeries) == 0 {
		t.Error("expected process_memory_mb metric written to TSDB")
	}
}

func TestStartStop(t *testing.T) {
	m := New(nil)
	m.Start()
	time.Sleep(20 * time.Millisecond)
	m.Stop()
	// 停止后应可正常读取（不 panic）
	_ = m.GetProcesses("cpu")
}

// ── HTTP API ──────────────────────────────────────────────────────────────────

func TestAPIGetProcesses(t *testing.T) {
	m := New(nil)
	m.collect()
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/processes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var procs []ProcessInfo
	if err := json.NewDecoder(rec.Body).Decode(&procs); err != nil {
		t.Fatalf("json decode error: %v", err)
	}
}

func TestAPIGetProcesses_SortMemory(t *testing.T) {
	m := New(nil)
	m.collect()
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/processes?sort=memory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var procs []ProcessInfo
	json.NewDecoder(rec.Body).Decode(&procs)
	for i := 1; i < len(procs); i++ {
		if procs[i].MemoryMB > procs[i-1].MemoryMB {
			t.Errorf("memory sort broken at index %d", i)
		}
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	m := New(nil)
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/processes", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
