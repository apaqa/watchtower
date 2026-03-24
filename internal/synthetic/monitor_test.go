package synthetic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// newTestMon 创建不带后台协程、可注入 HTTP 客户端的 Monitor
func newTestMon(srv *httptest.Server) *Monitor {
	m := New(nil)
	if srv != nil {
		m.SetHTTPClient(srv.Client())
	}
	return m
}

// TestSingleStep_Pass 单步骤 GET 成功
func TestSingleStep_Pass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	m := newTestMon(srv)
	if err := m.AddForTest(SyntheticTest{
		Name: "single_pass",
		Steps: []SyntheticStep{
			{Name: "step1", Method: "GET", URL: srv.URL, ExpectedStatus: 200},
		},
	}); err != nil {
		t.Fatalf("AddForTest: %v", err)
	}

	result, ok := m.ExecSync("single_pass")
	if !ok {
		t.Fatal("ExecSync 返回 false")
	}
	if !result.Passed {
		t.Errorf("期望通过，实际失败: %s", result.Error)
	}
	if len(result.Steps) != 1 || !result.Steps[0].Passed {
		t.Errorf("步骤结果不正确: %+v", result.Steps)
	}
}

// TestSingleStep_WrongStatus 状态码不符时应失败
func TestSingleStep_WrongStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := newTestMon(srv)
	m.AddForTest(SyntheticTest{
		Name: "wrong_status",
		Steps: []SyntheticStep{
			{Name: "step1", Method: "GET", URL: srv.URL, ExpectedStatus: 200},
		},
	})

	result, ok := m.ExecSync("wrong_status")
	if !ok {
		t.Fatal("ExecSync 返回 false")
	}
	if result.Passed {
		t.Error("期望失败（状态码不符），但通过了")
	}
	if len(result.Steps) == 0 {
		t.Fatal("应有步骤结果")
	}
	if !strings.Contains(result.Steps[0].Error, "500") {
		t.Errorf("错误消息应包含 500，实际: %s", result.Steps[0].Error)
	}
}

// TestMultiStep_VarExtraction 第一步提取变量，第二步在 Header 中使用
func TestMultiStep_VarExtraction(t *testing.T) {
	// step1：返回 JSON，含 token 字段
	step1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": "abc-123"})
	}))
	defer step1Srv.Close()

	// step2：验证 Authorization 请求头
	var receivedAuth string
	step2Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer step2Srv.Close()

	m := New(nil)
	// 两台测试服务器均为本地 httptest，使用 http.DefaultClient 即可访问
	m.SetHTTPClient(&http.Client{})

	err := m.AddForTest(SyntheticTest{
		Name: "multi_step_vars",
		Steps: []SyntheticStep{
			{
				Name:           "login",
				Method:         "GET",
				URL:            step1Srv.URL,
				ExpectedStatus: 200,
				ExtractVars:    map[string]string{"token": "$.token"},
			},
			{
				Name:           "use_token",
				Method:         "GET",
				URL:            step2Srv.URL,
				ExpectedStatus: 200,
				Headers:        map[string]string{"Authorization": "Bearer {token}"},
			},
		},
	})
	if err != nil {
		t.Fatalf("AddForTest: %v", err)
	}

	result, ok := m.ExecSync("multi_step_vars")
	if !ok {
		t.Fatal("ExecSync 返回 false")
	}
	if !result.Passed {
		t.Errorf("期望通过，失败原因: %s", result.Error)
	}
	if len(result.Steps) != 2 {
		t.Errorf("期望 2 步，实际 %d", len(result.Steps))
	}
	if receivedAuth != "Bearer abc-123" {
		t.Errorf("期望 Authorization: Bearer abc-123，实际: %q", receivedAuth)
	}
}

// TestTimeout_Handling 请求超时应产生失败结果
func TestTimeout_Handling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newTestMon(srv)
	m.AddForTest(SyntheticTest{
		Name:    "timeout_test",
		Timeout: 50, // 50ms，小于服务端 200ms sleep
		Steps: []SyntheticStep{
			{Name: "slow_step", Method: "GET", URL: srv.URL, ExpectedStatus: 200},
		},
	})

	result, ok := m.ExecSync("timeout_test")
	if !ok {
		t.Fatal("ExecSync 返回 false")
	}
	if result.Passed {
		t.Error("期望因超时失败，但结果为通过")
	}
	if len(result.Steps) == 0 {
		t.Fatal("应有步骤结果")
	}
	if result.Steps[0].Error == "" {
		t.Error("超时步骤的 error 字段不应为空")
	}
}

// TestAssertContains_Fail 响应体断言失败
func TestAssertContains_Fail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"degraded"}`))
	}))
	defer srv.Close()

	m := newTestMon(srv)
	m.AddForTest(SyntheticTest{
		Name: "assert_fail",
		Steps: []SyntheticStep{
			{Name: "check", Method: "GET", URL: srv.URL,
				ExpectedStatus: 200, AssertContains: "healthy"},
		},
	})

	result, _ := m.ExecSync("assert_fail")
	if result.Passed {
		t.Error("期望断言失败，但通过了")
	}
	if !strings.Contains(result.Steps[0].Error, "healthy") {
		t.Errorf("错误消息应提及期望字符串 'healthy'，实际: %s", result.Steps[0].Error)
	}
}

// TestMetricsPublished 测试通过/失败后应发布 synthetic_duration_ms 和 synthetic_status 指标
func TestMetricsPublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var published []model.MetricPoint
	m := New(func(pts []model.MetricPoint) {
		published = append(published, pts...)
	})
	m.SetHTTPClient(srv.Client())

	m.AddForTest(SyntheticTest{
		Name: "metric_test",
		Steps: []SyntheticStep{
			{Name: "s1", Method: "GET", URL: srv.URL, ExpectedStatus: 200},
		},
	})
	m.ExecSync("metric_test")

	if len(published) < 2 {
		t.Fatalf("期望至少 2 条指标，实际 %d", len(published))
	}

	names := make(map[string]float64)
	for _, p := range published {
		names[p.Name] = p.Value
	}
	if _, ok := names["synthetic_duration_ms"]; !ok {
		t.Error("缺少 synthetic_duration_ms 指标")
	}
	if v, ok := names["synthetic_status"]; !ok || v != 1.0 {
		t.Errorf("synthetic_status 应为 1.0（通过），实际 %v", v)
	}
}

// TestHistory_Stored 执行后历史记录应可查询
func TestHistory_Stored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := newTestMon(srv)
	m.AddForTest(SyntheticTest{
		Name: "history_test",
		Steps: []SyntheticStep{
			{Name: "s1", Method: "GET", URL: srv.URL, ExpectedStatus: 200},
		},
	})

	m.ExecSync("history_test")
	m.ExecSync("history_test")

	history, ok := m.History("history_test")
	if !ok {
		t.Fatal("History 返回 false")
	}
	if len(history) != 2 {
		t.Errorf("期望 2 条历史，实际 %d", len(history))
	}
}
