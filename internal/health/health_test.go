package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthManager_HealthyChecks 所有检查健康时整体状态应为 healthy
func TestHealthManager_HealthyChecks(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("db", func() HealthResult {
		return HealthResult{Status: StatusHealthy, Message: "ok"}
	}))
	m.Register(NewFuncCheck("cache", func() HealthResult {
		return HealthResult{Status: StatusHealthy, Message: "ok"}
	}))
	m.runChecks()

	report := m.Report()
	if report.Status != StatusHealthy {
		t.Errorf("期望整体状态 healthy，实际 %s", report.Status)
	}
	if len(report.Checks) != 2 {
		t.Errorf("期望 2 个检查项，实际 %d", len(report.Checks))
	}
}

// TestHealthManager_UnhealthyCheck 有检查不健康时整体状态应为 unhealthy
func TestHealthManager_UnhealthyCheck(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("db", func() HealthResult {
		return HealthResult{Status: StatusUnhealthy, Message: "连接超时"}
	}))
	m.Register(NewFuncCheck("cache", func() HealthResult {
		return HealthResult{Status: StatusHealthy, Message: "ok"}
	}))
	m.runChecks()

	report := m.Report()
	if report.Status != StatusUnhealthy {
		t.Errorf("期望整体状态 unhealthy，实际 %s", report.Status)
	}
}

// TestHealthManager_DegradedCheck 有检查降级时整体状态应为 degraded
func TestHealthManager_DegradedCheck(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("tsdb", func() HealthResult {
		return HealthResult{Status: StatusDegraded, Message: "写入延迟高"}
	}))
	m.runChecks()

	report := m.Report()
	if report.Status != StatusDegraded {
		t.Errorf("期望整体状态 degraded，实际 %s", report.Status)
	}
}

// TestHealthManager_IsReady 仅当所有检查 healthy 时 IsReady 返回 true
func TestHealthManager_IsReady(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("svc", func() HealthResult {
		return HealthResult{Status: StatusHealthy}
	}))
	m.runChecks()

	if !m.IsReady() {
		t.Error("期望 IsReady() = true")
	}

	// 替换为不健康检查
	m2 := New()
	m2.Register(NewFuncCheck("svc", func() HealthResult {
		return HealthResult{Status: StatusUnhealthy, Message: "宕机"}
	}))
	m2.runChecks()
	if m2.IsReady() {
		t.Error("期望 IsReady() = false")
	}
}

// TestHealthHTTP_LiveEndpoint liveness 端点始终返回 200
func TestHealthHTTP_LiveEndpoint(t *testing.T) {
	m := New()
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
}

// TestHealthHTTP_ReadyEndpoint_Healthy 所有检查通过时 readiness 返回 200
func TestHealthHTTP_ReadyEndpoint_Healthy(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("ok", func() HealthResult {
		return HealthResult{Status: StatusHealthy}
	}))
	m.runChecks()

	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
}

// TestHealthHTTP_ReadyEndpoint_Unhealthy 有检查失败时 readiness 返回 503
func TestHealthHTTP_ReadyEndpoint_Unhealthy(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("broken", func() HealthResult {
		return HealthResult{Status: StatusUnhealthy, Message: "错误"}
	}))
	m.runChecks()

	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("期望 503，实际 %d", rr.Code)
	}
}

// TestHealthHTTP_FullReport 完整健康报告端点返回正确 JSON
func TestHealthHTTP_FullReport(t *testing.T) {
	m := New()
	m.Register(NewFuncCheck("api", func() HealthResult {
		return HealthResult{Status: StatusHealthy, Message: "正常"}
	}))
	m.runChecks()

	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}

	var report HealthReport
	if err := json.NewDecoder(rr.Body).Decode(&report); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if report.Status != StatusHealthy {
		t.Errorf("期望整体状态 healthy，实际 %s", report.Status)
	}
	if len(report.Checks) != 1 || report.Checks[0].Name != "api" {
		t.Errorf("期望包含 'api' 检查项，实际 %+v", report.Checks)
	}
}

// TestHealthManager_LatencyRecorded 确保延迟被记录到检查结果中
func TestHealthManager_LatencyRecorded(t *testing.T) {
	m := New()
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	callCount := 0
	m.SetNowFn(func() time.Time {
		callCount++
		// 每次调用前进 5ms，模拟检查耗时
		return fixed.Add(time.Duration(callCount*5) * time.Millisecond)
	})
	m.Register(NewFuncCheck("slow", func() HealthResult {
		// 不预填 LatencyMs，由 Manager 自动测量
		return HealthResult{Status: StatusHealthy, Message: "ok"}
	}))
	m.runChecks()

	report := m.Report()
	if len(report.Checks) == 0 {
		t.Fatal("期望至少一个检查项")
	}
	// 延迟应该被测量（大于等于 0）
	if report.Checks[0].Result.LatencyMs < 0 {
		t.Errorf("延迟不应为负数: %d", report.Checks[0].Result.LatencyMs)
	}
}

// TestHealthManager_EmptyChecks 无检查项时报告应为 healthy
func TestHealthManager_EmptyChecks(t *testing.T) {
	m := New()
	m.runChecks()

	report := m.Report()
	if report.Status != StatusHealthy {
		t.Errorf("无检查项时期望 healthy，实际 %s", report.Status)
	}
}
