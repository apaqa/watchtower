// Package alert - 告警 HTTP API 测试
package alert

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// newTestEngineWithDB 创建一个带有高 CPU 数据的测试引擎
func newTestEngineWithDB(t *testing.T) (*Engine, *tsdb.TSDB) {
	t.Helper()
	db := tsdb.New()
	db.Write([]model.MetricPoint{
		{Name: "cpu_usage_percent", Labels: map[string]string{"host": "test"},
			Value: 90, Timestamp: time.Now().UnixMilli()},
	})
	eng := NewEngine(db)
	return eng, db
}

// newTestMux 创建一个注册了告警路由的 ServeMux
func newTestMux(eng *Engine) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterRoutes(mux, eng)
	return mux
}

// TestAPICreateRule 验证 POST /api/v1/alerts/rules 成功创建规则
func TestAPICreateRule(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()
	mux := newTestMux(eng)

	body := AlertRule{
		Name:       "api_test_rule",
		Expression: "avg(cpu_usage_percent[5m])",
		Operator:   ">",
		Threshold:  80,
		Duration:   "0",
		Severity:   "critical",
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/rules", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d，响应: %s", rec.Code, rec.Body.String())
	}
	if len(eng.GetAlerts()) != 1 {
		t.Error("创建规则后 GetAlerts 应返回 1 条")
	}
}

// TestAPICreateRule_InvalidBody 验证非法请求体被拒绝
func TestAPICreateRule_InvalidBody(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()
	mux := newTestMux(eng)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/rules",
		bytes.NewReader([]byte("not-json")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("期望 400，实际 %d", rec.Code)
	}
}

// TestAPICreateRule_InvalidRule 验证非法规则（空名称）被拒绝
func TestAPICreateRule_InvalidRule(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()
	mux := newTestMux(eng)

	body := AlertRule{
		// Name 为空 → 校验失败
		Expression: "avg(cpu[5m])",
		Operator:   ">",
		Threshold:  80,
		Severity:   "info",
	}
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/rules", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("期望 400，实际 %d", rec.Code)
	}
}

// TestAPIListRules 验证 GET /api/v1/alerts/rules 返回正确的规则列表
func TestAPIListRules(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()

	// 添加两条规则
	eng.AddRule(AlertRule{Name: "rule1", Expression: "avg(cpu[5m])", Operator: ">", Threshold: 80, Severity: "warning"})
	eng.AddRule(AlertRule{Name: "rule2", Expression: "avg(mem[5m])", Operator: ">", Threshold: 90, Severity: "critical"})

	mux := newTestMux(eng)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/rules", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}

	var alerts []Alert
	if err := json.NewDecoder(rec.Body).Decode(&alerts); err != nil {
		t.Fatalf("响应 JSON 解析失败: %v", err)
	}
	if len(alerts) != 2 {
		t.Errorf("期望 2 条告警，实际 %d 条", len(alerts))
	}
}

// TestAPIDeleteRule 验证 DELETE /api/v1/alerts/rules/{name} 成功删除规则
func TestAPIDeleteRule(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()
	eng.AddRule(AlertRule{Name: "to_delete", Expression: "avg(cpu[5m])", Operator: ">", Threshold: 80, Severity: "info"})

	mux := newTestMux(eng)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/alerts/rules/to_delete", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("期望 204，实际 %d，响应: %s", rec.Code, rec.Body.String())
	}
	if len(eng.GetAlerts()) != 0 {
		t.Error("删除后 GetAlerts 应为空")
	}
}

// TestAPIDeleteRule_NotFound 验证删除不存在的规则返回 404
func TestAPIDeleteRule_NotFound(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()
	mux := newTestMux(eng)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/alerts/rules/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("期望 404，实际 %d", rec.Code)
	}
}

// TestAPIActiveAlerts 验证 GET /api/v1/alerts/active 返回 Firing 状态的告警
func TestAPIActiveAlerts(t *testing.T) {
	eng, db := newTestEngineWithDB(t) // CPU=90
	defer db.Stop()

	// 添加立即触发规则
	eng.AddRule(AlertRule{
		Name:       "active_test",
		Expression: `avg(cpu_usage_percent{host="test"}[5m])`,
		Operator:   ">",
		Threshold:  80,
		Duration:   "0",
		Severity:   "critical",
	})

	// 触发告警
	eng.Evaluate()

	mux := newTestMux(eng)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/active", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}

	var active []Alert
	if err := json.NewDecoder(rec.Body).Decode(&active); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("期望 1 条 Firing 告警，实际 %d 条", len(active))
	}
	if active[0].State != StateFiring {
		t.Errorf("期望 Firing 状态，实际 %q", active[0].State)
	}
}

// TestAPIHistory 验证 GET /api/v1/alerts/history 返回历史记录
func TestAPIHistory(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()

	eng.AddRule(AlertRule{
		Name:       "history_test",
		Expression: `avg(cpu_usage_percent{host="test"}[5m])`,
		Operator:   ">",
		Threshold:  80,
		Duration:   "0",
		Severity:   "warning",
	})
	eng.Evaluate() // 产生状态变化历史

	mux := newTestMux(eng)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/history", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	var history []AlertEvent
	if err := json.NewDecoder(rec.Body).Decode(&history); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(history) == 0 {
		t.Error("评估后应有历史记录")
	}
}

// TestAPIActiveAlerts_Empty 验证无活跃告警时返回空数组（而非 null）
func TestAPIActiveAlerts_Empty(t *testing.T) {
	eng, db := newTestEngineWithDB(t)
	defer db.Stop()
	mux := newTestMux(eng)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/active", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	// 验证返回 [] 而非 null
	body := rec.Body.String()
	if body == "null\n" || body == "null" {
		t.Error("无告警时应返回空数组 []，而非 null")
	}
}
