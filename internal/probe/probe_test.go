// Package probe 单元测试：验证 HTTP 探测逻辑、历史存储和状态统计
package probe

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/tsdb"
)

// newTestManager 创建附带测试 TSDB 的探针管理器
func newTestManager(t *testing.T) (*Manager, *tsdb.TSDB) {
	t.Helper()
	db := tsdb.New()
	t.Cleanup(func() { db.Stop() })
	pm := NewManager(db)
	t.Cleanup(func() { pm.Stop() })
	return pm, db
}

// TestDoHTTPSuccess 验证正常响应时探测结果为 up
func TestDoHTTPSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pm, db := newTestManager(t)
	_ = db

	result := pm.doHTTP(ProbeConfig{
		Name:           "ok-test",
		URL:            srv.URL,
		Method:         "GET",
		ExpectedStatus: 200,
		TimeoutMs:      5000,
	})

	if result.Status != "up" {
		t.Errorf("期望 up，实际 %q（错误: %s）", result.Status, result.Error)
	}
	if result.StatusCode != 200 {
		t.Errorf("期望状态码 200，实际 %d", result.StatusCode)
	}
	if result.ResponseMs < 0 {
		t.Error("响应时间应为非负数")
	}
}

// TestDoHTTPWrongStatus 验证响应状态码与期望不符时探测结果为 down
func TestDoHTTPWrongStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // 503
	}))
	defer srv.Close()

	pm, _ := newTestManager(t)
	result := pm.doHTTP(ProbeConfig{
		Name:           "wrong-status",
		URL:            srv.URL,
		Method:         "GET",
		ExpectedStatus: 200,
		TimeoutMs:      5000,
	})

	if result.Status != "down" {
		t.Errorf("期望 down，实际 %q", result.Status)
	}
	if result.StatusCode != 503 {
		t.Errorf("期望状态码 503，实际 %d", result.StatusCode)
	}
	if result.Error == "" {
		t.Error("down 状态时 Error 字段不应为空")
	}
}

// TestDoHTTPTimeout 验证请求超时时探测结果为 down
func TestDoHTTPTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // 永久阻塞，直到测试结束
		w.WriteHeader(200)
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	pm, _ := newTestManager(t)
	result := pm.doHTTP(ProbeConfig{
		Name:           "timeout-test",
		URL:            srv.URL,
		Method:         "GET",
		ExpectedStatus: 200,
		TimeoutMs:      80, // 80ms 超时
	})

	if result.Status != "down" {
		t.Errorf("超时应返回 down，实际 %q", result.Status)
	}
	if result.Error == "" {
		t.Error("超时时 Error 字段不应为空")
	}
}

// TestDoHTTPNetworkError 验证无效 URL 时探测结果为 down
func TestDoHTTPNetworkError(t *testing.T) {
	pm, _ := newTestManager(t)
	result := pm.doHTTP(ProbeConfig{
		Name:           "net-err",
		URL:            "http://127.0.0.1:19999", // 未监听的端口
		Method:         "GET",
		ExpectedStatus: 200,
		TimeoutMs:      500,
	})

	if result.Status != "down" {
		t.Errorf("连接失败应返回 down，实际 %q", result.Status)
	}
}

// TestExecProbeStoresHistory 验证 execProbe 将结果追加到历史记录
func TestExecProbeStoresHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pm, _ := newTestManager(t)
	e := &entry{
		cfg: ProbeConfig{
			Name: "hist-test", URL: srv.URL, Method: "GET",
			ExpectedStatus: 200, TimeoutMs: 5000, IntervalSecs: 60,
		},
		stopCh: make(chan struct{}),
	}
	pm.mu.Lock()
	pm.probes["hist-test"] = e
	pm.mu.Unlock()

	pm.execProbe(e)
	pm.execProbe(e)

	history := e.getHistory()
	if len(history) != 2 {
		t.Fatalf("期望 2 条历史记录，实际 %d 条", len(history))
	}
	if history[0].Status != "up" {
		t.Errorf("期望 up，实际 %q", history[0].Status)
	}
}

// TestExecProbeWritesTSDB 验证 execProbe 将指标写入 TSDB
func TestExecProbeWritesTSDB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pm, db := newTestManager(t)
	e := &entry{
		cfg: ProbeConfig{
			Name: "tsdb-test", URL: srv.URL, Method: "GET",
			ExpectedStatus: 200, TimeoutMs: 5000, IntervalSecs: 60,
		},
		stopCh: make(chan struct{}),
	}
	pm.mu.Lock()
	pm.probes["tsdb-test"] = e
	pm.mu.Unlock()

	before := time.Now().UnixMilli()
	pm.execProbe(e)
	after := time.Now().UnixMilli()

	labels := map[string]string{"name": "tsdb-test", "url": srv.URL}
	pts := db.QueryRange("probe_status", labels, before-100, after+100)
	if len(pts) == 0 {
		t.Error("probe_status 指标未写入 TSDB")
	}
	if pts[0].Value != 1.0 {
		t.Errorf("probe_status 期望 1.0（up），实际 %f", pts[0].Value)
	}

	rts := db.QueryRange("probe_response_ms", labels, before-100, after+100)
	if len(rts) == 0 {
		t.Error("probe_response_ms 指标未写入 TSDB")
	}
}

// TestHistoryLimit 验证超过 maxHistory 条时旧结果被丢弃
func TestHistoryLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pm, _ := newTestManager(t)
	e := &entry{
		cfg: ProbeConfig{
			Name: "limit-test", URL: srv.URL, Method: "GET",
			ExpectedStatus: 200, TimeoutMs: 5000, IntervalSecs: 60,
		},
		stopCh: make(chan struct{}),
	}
	pm.mu.Lock()
	pm.probes["limit-test"] = e
	pm.mu.Unlock()

	// 写入超过上限的结果
	for i := 0; i < maxHistory+20; i++ {
		pm.execProbe(e)
	}

	history := e.getHistory()
	if len(history) != maxHistory {
		t.Errorf("历史记录应被截断到 %d 条，实际 %d 条", maxHistory, len(history))
	}
}

// TestUptimePct 验证 uptime_pct 计算正确
func TestUptimePct(t *testing.T) {
	e := &entry{
		cfg: ProbeConfig{Name: "pct-test", ExpectedStatus: 200},
	}
	// 手动添加 8 up + 2 down = 80%
	for i := 0; i < 8; i++ {
		e.addResult(&ProbeResult{Status: "up", Timestamp: int64(i)})
	}
	for i := 0; i < 2; i++ {
		e.addResult(&ProbeResult{Status: "down", Timestamp: int64(8 + i)})
	}

	ps := e.currentStatus()
	if ps.UptimePct != 80.0 {
		t.Errorf("可用率期望 80%%，实际 %.1f%%", ps.UptimePct)
	}
	if ps.Status != "down" { // 最新一条是 down
		t.Errorf("当前状态期望 down，实际 %q", ps.Status)
	}
}

// TestAddRemoveProbe 验证运行时添加和移除探针
func TestAddRemoveProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pm, _ := newTestManager(t)

	if err := pm.AddProbe(ProbeConfig{
		Name: "runtime-probe", URL: srv.URL, Method: "GET",
		ExpectedStatus: 200, IntervalSecs: 60, TimeoutMs: 5000,
	}); err != nil {
		t.Fatalf("AddProbe 失败: %v", err)
	}

	// 再次添加同名探针应报错
	if err := pm.AddProbe(ProbeConfig{Name: "runtime-probe", URL: srv.URL}); err == nil {
		t.Error("重复添加同名探针应返回错误")
	}

	// 移除探针
	if err := pm.RemoveProbe("runtime-probe"); err != nil {
		t.Fatalf("RemoveProbe 失败: %v", err)
	}

	// 移除不存在的探针应报错
	if err := pm.RemoveProbe("nonexistent"); err == nil {
		t.Error("移除不存在的探针应返回错误")
	}
}

// TestAPIListProbes 验证 GET /api/v1/probes 返回探针列表
func TestAPIListProbes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	db := tsdb.New()
	defer db.Stop()
	pm := NewManager(db)
	defer pm.Stop()

	// 手动添加 entry（不启动 goroutine，避免并发）
	e := &entry{
		cfg:    ProbeConfig{Name: "api-test", URL: srv.URL, Method: "GET", ExpectedStatus: 200},
		stopCh: make(chan struct{}),
	}
	pm.mu.Lock()
	pm.probes["api-test"] = e
	pm.mu.Unlock()

	mux := http.NewServeMux()
	RegisterRoutes(mux, pm)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var statuses []ProbeStatus
	if err := json.NewDecoder(w.Body).Decode(&statuses); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(statuses) != 1 {
		t.Errorf("期望 1 个探针，实际 %d 个", len(statuses))
	}
}

// TestAPICreateProbe 验证 POST /api/v1/probes 创建探针
func TestAPICreateProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	db := tsdb.New()
	defer db.Stop()
	pm := NewManager(db)
	defer pm.Stop()

	mux := http.NewServeMux()
	RegisterRoutes(mux, pm)

	body, _ := json.Marshal(ProbeConfig{
		Name: "created-probe", URL: srv.URL, Method: "GET",
		ExpectedStatus: 200, IntervalSecs: 60, TimeoutMs: 5000,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d: %s", w.Code, w.Body.String())
	}

	// 等待初始探测完成
	time.Sleep(150 * time.Millisecond)

	statuses := pm.GetStatus()
	if len(statuses) != 1 {
		t.Errorf("期望 1 个探针，实际 %d 个", len(statuses))
	}
}

// TestAPIProbeHistory 验证 GET /api/v1/probes/{name}/history 返回历史记录
func TestAPIProbeHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	db := tsdb.New()
	defer db.Stop()
	pm := NewManager(db)
	defer pm.Stop()

	e := &entry{
		cfg:    ProbeConfig{Name: "hist-api", URL: srv.URL, Method: "GET", ExpectedStatus: 200, TimeoutMs: 5000},
		stopCh: make(chan struct{}),
	}
	pm.execProbe(e) // 生成一条历史
	pm.mu.Lock()
	pm.probes["hist-api"] = e
	pm.mu.Unlock()

	mux := http.NewServeMux()
	RegisterRoutes(mux, pm)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probes/hist-api/history", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var history []ProbeResult
	json.NewDecoder(w.Body).Decode(&history)
	if len(history) == 0 {
		t.Error("历史记录不应为空")
	}
}

// TestIntegrationConfigToMetrics 验证从配置创建探针 → 运行 → 指标写入 TSDB 的完整链路
func TestIntegrationConfigToMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	db := tsdb.New()
	defer db.Stop()
	pm := NewManager(db)
	defer pm.Stop()

	// 模拟从配置文件加载端点并添加探针
	if err := pm.AddProbe(ProbeConfig{
		Name:           "integration-probe",
		URL:            srv.URL,
		Method:         "GET",
		ExpectedStatus: 200,
		IntervalSecs:   60,
		TimeoutMs:      5000,
	}); err != nil {
		t.Fatalf("AddProbe 失败: %v", err)
	}

	// 等待初始探测完成
	time.Sleep(200 * time.Millisecond)

	// 验证指标已写入 TSDB
	labels := map[string]string{"name": "integration-probe", "url": srv.URL}
	before := time.Now().Add(-1 * time.Second).UnixMilli()
	after := time.Now().Add(1 * time.Second).UnixMilli()
	pts := db.QueryRange("probe_status", labels, before, after)

	if len(pts) == 0 {
		t.Error("集成测试：探测后 TSDB 中应有 probe_status 指标")
	}
}
