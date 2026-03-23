package slo

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apaqa/watchtower/internal/tsdb"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(tsdb.New())
}

// ── Store operations ──────────────────────────────────────────────────────────

func TestAddAndCount(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(SLO{Name: "avail", TargetPercent: 99.9, WQLQuery: "last(cpu_usage_percent[5m])", Window: "7d"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if s.Count() != 1 {
		t.Errorf("expected count 1, got %d", s.Count())
	}
}

func TestAddValidation(t *testing.T) {
	s := newTestStore(t)

	if err := s.Add(SLO{Name: "", TargetPercent: 99, WQLQuery: "x"}); err == nil {
		t.Error("expected error for empty name")
	}
	if err := s.Add(SLO{Name: "x", TargetPercent: 0, WQLQuery: "x"}); err == nil {
		t.Error("expected error for target=0")
	}
	if err := s.Add(SLO{Name: "x", TargetPercent: 100, WQLQuery: "x"}); err == nil {
		t.Error("expected error for target=100")
	}
	if err := s.Add(SLO{Name: "x", TargetPercent: 99, WQLQuery: ""}); err == nil {
		t.Error("expected error for empty wql_query")
	}
}

func TestRemove(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(SLO{Name: "svc", TargetPercent: 99, WQLQuery: "last(x[5m])", Window: "1d"})
	if err := s.Remove("svc"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.Count() != 0 {
		t.Error("expected count 0 after remove")
	}
}

func TestRemoveNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Remove("nonexistent"); err == nil {
		t.Error("expected error for nonexistent SLO")
	}
}

// ── SLI and error budget calculation ─────────────────────────────────────────

func TestComputeStatusNoData(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(SLO{Name: "svc", TargetPercent: 99.9, WQLQuery: "avg(nonexistent_metric[5m])", Window: "30d"})
	statuses := s.ListStatuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	st := statuses[0]
	// No data → SLI = 0 → not healthy
	if st.Healthy {
		t.Error("expected not healthy when no data")
	}
	if st.CurrentSLI != 0 {
		t.Errorf("expected SLI 0, got %f", st.CurrentSLI)
	}
}

func TestErrorBudgetCalculation(t *testing.T) {
	s := newTestStore(t)
	// 30d window, 99.9% target → budget = 0.001 * 43200 = 43.2 min
	_ = s.Add(SLO{Name: "svc", TargetPercent: 99.9, WQLQuery: "avg(nonexistent[5m])", Window: "30d"})
	statuses := s.ListStatuses()
	st := statuses[0]

	expected := 0.001 * 43200.0 // 43.2 minutes
	if diff := st.ErrorBudgetTotal - expected; diff < -0.01 || diff > 0.01 {
		t.Errorf("expected budget total ~43.2, got %f", st.ErrorBudgetTotal)
	}
}

func TestWindowDefaultsTo30d(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(SLO{Name: "svc", TargetPercent: 99, WQLQuery: "last(x[5m])", Window: "invalid"})
	statuses := s.ListStatuses()
	// 30d = 43200 minutes, target 99% → budget = 432 minutes
	st := statuses[0]
	expected := 0.01 * 43200.0
	if diff := st.ErrorBudgetTotal - expected; diff < -0.1 || diff > 0.1 {
		t.Errorf("expected default 30d budget ~432, got %f", st.ErrorBudgetTotal)
	}
}

func TestListStatusesSortedByName(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(SLO{Name: "zzz", TargetPercent: 99, WQLQuery: "last(x[5m])", Window: "1d"})
	_ = s.Add(SLO{Name: "aaa", TargetPercent: 99, WQLQuery: "last(x[5m])", Window: "1d"})
	statuses := s.ListStatuses()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].SLO.Name != "aaa" || statuses[1].SLO.Name != "zzz" {
		t.Errorf("expected sorted order aaa, zzz — got %s, %s", statuses[0].SLO.Name, statuses[1].SLO.Name)
	}
}

// ── HTTP API ──────────────────────────────────────────────────────────────────

func TestSLOAPIGet(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(SLO{Name: "avail", TargetPercent: 99.9, WQLQuery: "last(cpu_usage_percent[5m])", Window: "7d"})

	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/slos", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var statuses []SLOStatus
	if err := json.NewDecoder(rec.Body).Decode(&statuses); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(statuses) != 1 {
		t.Errorf("expected 1 status, got %d", len(statuses))
	}
}

func TestSLOAPIPost(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	body, _ := json.Marshal(SLO{Name: "latency", TargetPercent: 95, WQLQuery: "avg(latency_ms[5m])", Window: "1d"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/slos", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if s.Count() != 1 {
		t.Errorf("expected 1 SLO after POST, got %d", s.Count())
	}
}

func TestSLOAPIPostValidationError(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	body, _ := json.Marshal(SLO{Name: "", TargetPercent: 99, WQLQuery: "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/slos", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestSLOAPIDelete(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(SLO{Name: "to-delete", TargetPercent: 99, WQLQuery: "x", Window: "1d"})

	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/slos/to-delete", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if s.Count() != 0 {
		t.Error("expected 0 SLOs after delete")
	}
}

func TestSLOAPIDeleteNotFound(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/slos/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}
