// Package synthetic 提供多步骤 HTTP 事务的合成监控
package synthetic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// SyntheticStep 描述合成测试中的单个 HTTP 步骤
type SyntheticStep struct {
	Name              string            `json:"name"`
	Method            string            `json:"method"`             // HTTP 方法，默认 GET
	URL               string            `json:"url"`               // 目标 URL；可使用 {varName} 引用前序步骤提取的变量
	Headers           map[string]string `json:"headers,omitempty"`
	Body              string            `json:"body,omitempty"`    // 请求体；可含 {varName} 占位符
	ExpectedStatus    int               `json:"expected_status"`   // 期望的 HTTP 状态码，0=不检查
	AssertContains    string            `json:"assert_body_contains,omitempty"` // 断言响应体包含此字符串
	ExtractVars       map[string]string `json:"extract_vars,omitempty"` // varName → json_path（从响应 JSON 中提取）
}

// SyntheticTest 描述一个完整的多步骤合成测试
type SyntheticTest struct {
	Name     string          `json:"name"`             // 测试唯一名称
	Steps    []SyntheticStep `json:"steps"`
	Interval int             `json:"interval_seconds"` // 两次运行之间的间隔，默认 60
	Timeout  int             `json:"timeout_ms"`       // 单步超时毫秒，默认 10000
}

// StepResult 单步骤的执行结果
type StepResult struct {
	StepName   string `json:"step_name"`
	StatusCode int    `json:"status_code"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Passed     bool   `json:"passed"`
}

// TestResult 一次完整测试运行的结果
type TestResult struct {
	TestName   string       `json:"test_name"`
	StartedAt  int64        `json:"started_at_ms"`
	DurationMs int64        `json:"duration_ms"`
	Passed     bool         `json:"passed"`
	Steps      []StepResult `json:"steps"`
	Error      string       `json:"error,omitempty"`
}

const maxHistory = 20 // 每个测试保留最近 N 条历史

// testEntry 是 Monitor 内部维护的单个测试运行时状态
type testEntry struct {
	test    SyntheticTest
	history []TestResult
	stopCh  chan struct{}
	running bool
}

// Monitor 管理所有合成测试的生命周期
type Monitor struct {
	mu      sync.RWMutex
	tests   map[string]*testEntry
	postFn  func([]model.MetricPoint) // 每次运行后写入 TSDB
	client  *http.Client
}

// New 创建一个 Monitor；postFn 用于将测试结果写入 TSDB
func New(postFn func([]model.MetricPoint)) *Monitor {
	return &Monitor{
		tests:  make(map[string]*testEntry),
		postFn: postFn,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Add 注册并立即启动一个合成测试
func (m *Monitor) Add(t SyntheticTest) error {
	if t.Name == "" {
		return fmt.Errorf("测试名称不能为空")
	}
	if len(t.Steps) == 0 {
		return fmt.Errorf("测试必须包含至少 1 个步骤")
	}
	if t.Interval <= 0 {
		t.Interval = 60
	}
	if t.Timeout <= 0 {
		t.Timeout = 10000
	}
	for i := range t.Steps {
		if t.Steps[i].Method == "" {
			t.Steps[i].Method = http.MethodGet
		}
		if t.Steps[i].ExpectedStatus == 0 {
			t.Steps[i].ExpectedStatus = http.StatusOK
		}
	}

	m.mu.Lock()
	if _, exists := m.tests[t.Name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("测试 %q 已存在", t.Name)
	}
	e := &testEntry{
		test:   t,
		stopCh: make(chan struct{}),
	}
	m.tests[t.Name] = e
	m.mu.Unlock()

	go m.runLoop(e)
	return nil
}

// runLoop 在独立 goroutine 中按 Interval 定期执行测试
func (m *Monitor) runLoop(e *testEntry) {
	e.running = true
	// 立即执行一次
	m.runTest(e)

	ticker := time.NewTicker(time.Duration(e.test.Interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.runTest(e)
		case <-e.stopCh:
			e.running = false
			return
		}
	}
}

// runTest 执行一次完整的合成测试并记录结果
func (m *Monitor) runTest(e *testEntry) {
	start := time.Now()
	result := TestResult{
		TestName:  e.test.Name,
		StartedAt: start.UnixMilli(),
		Passed:    true,
	}

	// vars 保存各步骤提取的变量，供后续步骤使用
	vars := make(map[string]string)

	for _, step := range e.test.Steps {
		sr := m.runStep(step, vars, time.Duration(e.test.Timeout)*time.Millisecond)
		result.Steps = append(result.Steps, sr)
		if !sr.Passed {
			result.Passed = false
			result.Error = fmt.Sprintf("步骤 %q 失败: %s", step.Name, sr.Error)
			break
		}
	}

	result.DurationMs = time.Since(start).Milliseconds()

	// 写入历史记录
	m.mu.Lock()
	e.history = append([]TestResult{result}, e.history...)
	if len(e.history) > maxHistory {
		e.history = e.history[:maxHistory]
	}
	m.mu.Unlock()

	// 将结果发布为 TSDB 指标
	if m.postFn != nil {
		now := time.Now().UnixMilli()
		statusVal := 0.0
		if result.Passed {
			statusVal = 1.0
		}
		m.postFn([]model.MetricPoint{
			{Name: "synthetic_duration_ms", Labels: map[string]string{"test": e.test.Name},
				Value: float64(result.DurationMs), Timestamp: now},
			{Name: "synthetic_status", Labels: map[string]string{"test": e.test.Name},
				Value: statusVal, Timestamp: now},
		})
	}
}

// runStep 执行单个步骤并返回结果；vars 是当前变量表
func (m *Monitor) runStep(step SyntheticStep, vars map[string]string, timeout time.Duration) StepResult {
	sr := StepResult{StepName: step.Name}

	// 替换 URL 和 Body 中的变量占位符
	url := substituteVars(step.URL, vars)
	body := substituteVars(step.Body, vars)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, step.Method, url, bodyReader)
	if err != nil {
		sr.Error = fmt.Sprintf("创建请求失败: %v", err)
		return sr
	}

	// 设置请求头
	for k, v := range step.Headers {
		req.Header.Set(k, substituteVars(v, vars))
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	start := time.Now()
	resp, err := m.client.Do(req)
	sr.DurationMs = time.Since(start).Milliseconds()

	if err != nil {
		sr.Error = fmt.Sprintf("请求失败: %v", err)
		return sr
	}
	defer resp.Body.Close()

	sr.StatusCode = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// 检查期望状态码
	if step.ExpectedStatus != 0 && resp.StatusCode != step.ExpectedStatus {
		sr.Error = fmt.Sprintf("期望状态码 %d，实际 %d", step.ExpectedStatus, resp.StatusCode)
		return sr
	}

	// 断言响应体包含指定字符串
	if step.AssertContains != "" && !strings.Contains(string(respBody), step.AssertContains) {
		sr.Error = fmt.Sprintf("响应体不包含期望字符串 %q", step.AssertContains)
		return sr
	}

	// 从响应 JSON 中提取变量
	if len(step.ExtractVars) > 0 {
		var respJSON map[string]interface{}
		if err := json.Unmarshal(respBody, &respJSON); err == nil {
			for varName, jsonPath := range step.ExtractVars {
				if val, err := extractJSONPath(respJSON, jsonPath); err == nil {
					vars[varName] = fmt.Sprintf("%v", val)
				}
			}
		}
	}

	sr.Passed = true
	return sr
}

// substituteVars 将字符串中的 {varName} 替换为 vars 中对应的值
func substituteVars(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// extractJSONPath 从 map 中按点分路径提取值（与 webhook 包相同逻辑，内部复用）
func extractJSONPath(data map[string]interface{}, path string) (interface{}, error) {
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	if path == "" {
		return data, nil
	}
	parts := strings.SplitN(path, ".", 2)
	val, ok := data[parts[0]]
	if !ok {
		return nil, fmt.Errorf("字段 %q 不存在", parts[0])
	}
	if len(parts) == 1 {
		return val, nil
	}
	nested, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("字段 %q 不是对象", parts[0])
	}
	return extractJSONPath(nested, parts[1])
}

// StopAll 停止所有正在运行的测试
func (m *Monitor) StopAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.tests {
		if e.running {
			select {
			case <-e.stopCh:
			default:
				close(e.stopCh)
			}
		}
	}
}

// List 返回所有测试的简要信息（最新历史结果）
func (m *Monitor) List() []TestSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]TestSummary, 0, len(m.tests))
	for _, e := range m.tests {
		s := TestSummary{
			Name:     e.test.Name,
			Steps:    len(e.test.Steps),
			Interval: e.test.Interval,
		}
		if len(e.history) > 0 {
			s.LastResult = &e.history[0]
		}
		result = append(result, s)
	}
	return result
}

// TestSummary 是对外暴露的测试摘要
type TestSummary struct {
	Name       string      `json:"name"`
	Steps      int         `json:"step_count"`
	Interval   int         `json:"interval_seconds"`
	LastResult *TestResult `json:"last_result,omitempty"`
}

// History 返回指定测试的历史结果列表
func (m *Monitor) History(name string) ([]TestResult, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.tests[name]
	if !ok {
		return nil, false
	}
	cp := make([]TestResult, len(e.history))
	copy(cp, e.history)
	return cp, true
}

// RunNow 立即执行指定测试（同步；测试结束后返回结果）
func (m *Monitor) RunNow(name string) (*TestResult, bool) {
	m.mu.RLock()
	e, ok := m.tests[name]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	m.runTest(e)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(e.history) > 0 {
		r := e.history[0]
		return &r, true
	}
	return nil, true
}

// ── HTTP API 路由 ─────────────────────────────────────────────────────────────

// RegisterRoutes 注册合成监控 API 路由
func RegisterRoutes(mux *http.ServeMux, m *Monitor) {
	mux.HandleFunc("/api/v1/synthetic", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			list := m.List()
			if list == nil {
				list = []TestSummary{}
			}
			json.NewEncoder(w).Encode(list)
		case http.MethodPost:
			var t SyntheticTest
			if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
				http.Error(w, `{"error":"JSON 解析失败"}`, http.StatusBadRequest)
				return
			}
			if err := m.Add(t); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(t)
		default:
			http.Error(w, `{"error":"不支持的方法"}`, http.StatusMethodNotAllowed)
		}
	})

	// GET /api/v1/synthetic/{name}/history
	mux.HandleFunc("/api/v1/synthetic/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"仅支持 GET"}`, http.StatusMethodNotAllowed)
			return
		}

		// 路径格式：/api/v1/synthetic/{name}/history
		tail := strings.TrimPrefix(r.URL.Path, "/api/v1/synthetic/")
		parts := strings.SplitN(tail, "/", 2)
		name := parts[0]
		if name == "" {
			http.Error(w, `{"error":"缺少测试名称"}`, http.StatusBadRequest)
			return
		}

		history, ok := m.History(name)
		if !ok {
			http.Error(w, `{"error":"测试不存在"}`, http.StatusNotFound)
			return
		}
		if history == nil {
			history = []TestResult{}
		}
		json.NewEncoder(w).Encode(history)
	})
}

// SetHTTPClient 替换用于测试步骤的 HTTP 客户端（测试专用）
func (m *Monitor) SetHTTPClient(c *http.Client) {
	m.client = c
}

// RunTestSync 同步执行指定测试（测试专用，无需后台循环）
func (m *Monitor) RunTestSync(name string) (*TestResult, bool) {
	return m.RunNow(name)
}

// addForTest 仅添加不启动后台循环（测试专用）
func (m *Monitor) addForTest(t SyntheticTest) error {
	if t.Name == "" {
		return fmt.Errorf("测试名称不能为空")
	}
	if len(t.Steps) == 0 {
		return fmt.Errorf("至少需要 1 个步骤")
	}
	if t.Interval <= 0 {
		t.Interval = 60
	}
	if t.Timeout <= 0 {
		t.Timeout = 10000
	}
	for i := range t.Steps {
		if t.Steps[i].Method == "" {
			t.Steps[i].Method = http.MethodGet
		}
		if t.Steps[i].ExpectedStatus == 0 {
			t.Steps[i].ExpectedStatus = http.StatusOK
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tests[t.Name]; exists {
		return fmt.Errorf("测试 %q 已存在", t.Name)
	}
	m.tests[t.Name] = &testEntry{
		test:   t,
		stopCh: make(chan struct{}),
	}
	return nil
}

// ExecSync 不通过后台协程直接同步执行（测试专用）
func (m *Monitor) ExecSync(name string) (*TestResult, bool) {
	m.mu.RLock()
	e, ok := m.tests[name]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	m.runTest(e)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(e.history) > 0 {
		r := e.history[0]
		return &r, true
	}
	return nil, true
}

// AddForTest exposes addForTest for testing
func (m *Monitor) AddForTest(t SyntheticTest) error {
	return m.addForTest(t)
}

// bytes buffer reader helper used in runStep (inlined)
var _ = bytes.NewReader
