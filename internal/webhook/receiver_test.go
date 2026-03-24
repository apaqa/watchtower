package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apaqa/watchtower/internal/model"
)

func newTestHandler() (*Handler, *[]model.MetricPoint, *[]model.LogEntry) {
	var points []model.MetricPoint
	var logs []model.LogEntry
	h := New(
		func(pts []model.MetricPoint) { points = append(points, pts...) },
		func(entries []model.LogEntry) { logs = append(logs, entries...) },
	)
	return h, &points, &logs
}

// TestGitHubWebhook_Push Push 事件应生成一条日志
func TestGitHubWebhook_Push(t *testing.T) {
	h, _, logs := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	payload := map[string]interface{}{
		"ref": "refs/heads/main",
		"repository": map[string]interface{}{"full_name": "acme/app"},
		"pusher":     map[string]interface{}{"name": "alice"},
		"commits":    []interface{}{map[string]interface{}{"id": "abc123", "message": "fix bug"}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("期望 204，实际 %d", rr.Code)
	}
	if len(*logs) == 0 {
		t.Error("期望生成至少 1 条日志")
	}
	if !strings.Contains((*logs)[0].Message, "push") {
		t.Errorf("日志消息应包含 push，实际: %s", (*logs)[0].Message)
	}
}

// TestGitHubWebhook_PR PR 事件应生成包含 action 的日志
func TestGitHubWebhook_PR(t *testing.T) {
	h, _, logs := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	payload := map[string]interface{}{
		"action": "opened",
		"number": 42,
		"pull_request": map[string]interface{}{
			"title": "Add feature X",
			"user":  map[string]interface{}{"login": "bob"},
		},
		"repository": map[string]interface{}{"full_name": "acme/app"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("期望 204，实际 %d", rr.Code)
	}
	if len(*logs) == 0 || !strings.Contains((*logs)[0].Message, "PR#42") {
		t.Errorf("日志应包含 PR#42，实际 %v", *logs)
	}
}

// TestGitHubWebhook_Issues Issues 事件应生成正确日志
func TestGitHubWebhook_Issues(t *testing.T) {
	h, _, logs := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	payload := map[string]interface{}{
		"action": "closed",
		"issue": map[string]interface{}{
			"number": 7,
			"title":  "Fix crash",
			"user":   map[string]interface{}{"login": "carol"},
		},
		"repository": map[string]interface{}{"full_name": "acme/app"},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if len(*logs) == 0 || !strings.Contains((*logs)[0].Message, "issue#7") {
		t.Errorf("日志应包含 issue#7，实际 %v", *logs)
	}
}

// TestGenericWebhook_ExtractMetrics 通用 Webhook 应提取数值字段为指标
func TestGenericWebhook_ExtractMetrics(t *testing.T) {
	h, points, _ := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	payload := map[string]interface{}{
		"latency_ms": 120.5,
		"error_rate": 0.02,
		"label_field": "ignore", // 非数值字段不应提取
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/generic?metric_name=svc_metric", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
	if len(*points) != 2 {
		t.Errorf("期望 2 条指标（数值字段），实际 %d", len(*points))
	}
	for _, p := range *points {
		if p.Name != "svc_metric" {
			t.Errorf("期望指标名 svc_metric，实际 %s", p.Name)
		}
	}
}

// TestGenericWebhook_InvalidPayload 无效 JSON 应返回 400
func TestGenericWebhook_InvalidPayload(t *testing.T) {
	h, _, _ := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/generic", strings.NewReader("not json"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("期望 400，实际 %d", rr.Code)
	}
}

// TestConfiguredWebhook_ExtractRule 配置的 ExtractRule 应正确提取指标
func TestConfiguredWebhook_ExtractRule(t *testing.T) {
	h, points, _ := newTestHandler()
	h.AddConfig(WebhookConfig{
		Name: "myservice",
		Path: "/api/v1/webhook/myservice",
		Rules: []ExtractRule{
			{JSONPath: "$.response_time", MetricName: "myservice_response_ms",
				LabelMappings: map[string]string{"env": "$.environment"}},
		},
	})
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	payload := map[string]interface{}{
		"response_time": 42.0,
		"environment":   "prod",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/myservice", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
	if len(*points) == 0 {
		t.Fatal("期望提取到指标")
	}
	if (*points)[0].Name != "myservice_response_ms" || (*points)[0].Value != 42.0 {
		t.Errorf("指标值不正确: %+v", (*points)[0])
	}
	if (*points)[0].Labels["env"] != "prod" {
		t.Errorf("标签 env 应为 prod，实际 %q", (*points)[0].Labels["env"])
	}
}

// TestExtractJSONPath 路径提取器的各种情形
func TestExtractJSONPath(t *testing.T) {
	data := map[string]interface{}{
		"status": "ok",
		"metrics": map[string]interface{}{
			"count": 42.0,
		},
	}

	v, err := extractJSONPath(data, "$.status")
	if err != nil || v != "ok" {
		t.Errorf("期望 ok，得到 %v err=%v", v, err)
	}

	v2, err2 := extractJSONPath(data, "$.metrics.count")
	if err2 != nil || v2 != 42.0 {
		t.Errorf("期望 42，得到 %v err=%v", v2, err2)
	}

	_, err3 := extractJSONPath(data, "$.nonexistent")
	if err3 == nil {
		t.Error("期望不存在字段返回错误")
	}
}
