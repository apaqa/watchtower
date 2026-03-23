package statuspage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/slo"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 辅助函数 ───────────────────────────────────────────────────────────────────

func newTestHandler() *Handler {
	return New(nil, nil, nil)
}

func doGet(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// ── 状态页 HTML ────────────────────────────────────────────────────────────────

func TestStatusPage_Returns200(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestStatusPage_ContainsHTML(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status")
	body := rec.Body.String()
	if !strings.Contains(body, "<html") {
		t.Error("response should contain HTML")
	}
	if !strings.Contains(body, "WatchTower Status") {
		t.Error("response should contain 'WatchTower Status'")
	}
}

func TestStatusPage_ContentType(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status")
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %s", ct)
	}
}

func TestStatusPage_MethodNotAllowed(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// ── 整体状态计算 ───────────────────────────────────────────────────────────────

func TestOverall_Operational_NoProbes(t *testing.T) {
	h := newTestHandler()
	if h.overall() != StatusOperational {
		t.Errorf("expected operational with no probes, got %s", h.overall())
	}
}

func TestOverall_Operational_AllUp(t *testing.T) {
	db := tsdb.New()
	pm := probe.NewManager(db)
	pm.AddProbe(probe.ProbeConfig{Name: "svc", URL: "http://example.com", Method: "GET", ExpectedStatus: 200, IntervalSecs: 30})
	h := New(pm, nil, nil)
	// 没有历史记录时为 unknown，unknown → operational（没有 down）
	status := h.overall()
	// 无 down 则视为 operational
	if status == StatusDown {
		t.Errorf("expected not down, got %s", status)
	}
}

func TestOverall_Degraded_WhenSomeDown(t *testing.T) {
	// 直接注入一个含有 down 状态的模拟 probe manager
	// 通过构造预置结果验证
	h := &Handler{probeMgr: &mockProbeMgr{statuses: []probe.ProbeStatus{
		{ProbeConfig: probe.ProbeConfig{Name: "a"}, Status: "up"},
		{ProbeConfig: probe.ProbeConfig{Name: "b"}, Status: "down"},
	}}}
	if h.overall() != StatusDegraded {
		t.Errorf("expected degraded, got %s", h.overall())
	}
}

func TestOverall_Down_WhenAllDown(t *testing.T) {
	h := &Handler{probeMgr: &mockProbeMgr{statuses: []probe.ProbeStatus{
		{ProbeConfig: probe.ProbeConfig{Name: "a"}, Status: "down"},
		{ProbeConfig: probe.ProbeConfig{Name: "b"}, Status: "down"},
	}}}
	if h.overall() != StatusDown {
		t.Errorf("expected down, got %s", h.overall())
	}
}

// ── 徽章 ──────────────────────────────────────────────────────────────────────

func TestBadge_ReturnsSVG(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status/badge")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "svg") {
		t.Errorf("expected SVG content type, got %s", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<svg") {
		t.Error("expected SVG body")
	}
}

func TestBadge_ContainsStatus(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status/badge")
	body := rec.Body.String()
	if !strings.Contains(body, "operational") {
		t.Errorf("badge should contain 'operational', got: %s", body[:200])
	}
}

func TestBadge_DegradedColor(t *testing.T) {
	h := &Handler{probeMgr: &mockProbeMgr{statuses: []probe.ProbeStatus{
		{ProbeConfig: probe.ProbeConfig{Name: "a"}, Status: "down"},
		{ProbeConfig: probe.ProbeConfig{Name: "b"}, Status: "up"},
	}}}
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status/badge")
	body := rec.Body.String()
	if !strings.Contains(body, "#d29922") {
		t.Error("expected yellow color for degraded status")
	}
}

// ── SLO 和告警整合 ────────────────────────────────────────────────────────────

func TestStatusPage_WithSLOs(t *testing.T) {
	db := tsdb.New()
	ss := slo.NewStore(db)
	ss.Add(slo.SLO{Name: "api-avail", TargetPercent: 99.9, WQLQuery: "avg(cpu_usage_percent[1m])", Window: "30d"})
	h := New(nil, ss, nil)
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	rec := doGet(mux, "/status")
	body := rec.Body.String()
	if !strings.Contains(body, "api-avail") {
		t.Error("status page should show SLO name")
	}
}

func TestStatusPage_WithAlertHistory(t *testing.T) {
	db := tsdb.New()
	ae := alert.NewEngine(db)
	ae.AddRule(alert.AlertRule{
		Name: "high_cpu", Expression: "avg(cpu_usage_percent[1m])",
		Operator: ">", Threshold: 0, Severity: "warning",
	})
	ae.Start()
	defer ae.Stop()
	h := New(nil, nil, ae)
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)
	// 无历史时应显示 "No incidents"
	rec := doGet(mux, "/status")
	body := rec.Body.String()
	if !strings.Contains(body, "No incidents") {
		t.Error("expected 'No incidents' when no history")
	}
}

// ── mockProbeMgr 实现 Handler 所需的 probeMgr 接口 ────────────────────────────

type mockProbeMgr struct {
	statuses []probe.ProbeStatus
}

func (m *mockProbeMgr) GetStatus() []probe.ProbeStatus {
	return m.statuses
}
