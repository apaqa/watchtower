package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// ── Mock Plugin ──────────────────────────────────────────────────────────────

// mockPlugin 是用于测试的固定指标插件
type mockPlugin struct {
	name      string
	initErr   error
	points    []model.MetricPoint
	stopCalled bool
	mu        sync.Mutex
}

func newMock(name string, points []model.MetricPoint) *mockPlugin {
	return &mockPlugin{name: name, points: points}
}

func (p *mockPlugin) Name() string    { return p.name }
func (p *mockPlugin) Version() string { return "0.1.0" }
func (p *mockPlugin) Init(_ map[string]string) error { return p.initErr }
func (p *mockPlugin) Collect() []model.MetricPoint   { return p.points }
func (p *mockPlugin) Interval() time.Duration        { return 50 * time.Millisecond }
func (p *mockPlugin) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopCalled = true
}

// ── Manager Tests ─────────────────────────────────────────────────────────────

// TestPluginManager_Register 注册后插件应出现在 List 结果中
func TestPluginManager_Register(t *testing.T) {
	var collected []model.MetricPoint
	m := New(func(pts []model.MetricPoint) { collected = append(collected, pts...) })

	pts := []model.MetricPoint{{Name: "test_metric", Value: 42}}
	if err := m.Register(newMock("mock1", pts)); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}

	list := m.List()
	if len(list) != 1 || list[0].Name != "mock1" {
		t.Errorf("期望列表包含 mock1，实际 %+v", list)
	}
	_ = collected
}

// TestPluginManager_DuplicateRegister 重复注册同名插件应返回错误
func TestPluginManager_DuplicateRegister(t *testing.T) {
	m := New(nil)
	m.Register(newMock("dup", nil))
	if err := m.Register(newMock("dup", nil)); err == nil {
		t.Error("期望重复注册返回错误")
	}
}

// TestPluginManager_StartCollect 启动后插件应在 Interval 内采集并调用 postFn
func TestPluginManager_StartCollect(t *testing.T) {
	var mu sync.Mutex
	var collected []model.MetricPoint

	m := New(func(pts []model.MetricPoint) {
		mu.Lock()
		collected = append(collected, pts...)
		mu.Unlock()
	})

	pts := []model.MetricPoint{{Name: "net_bytes", Value: 100}}
	m.Register(newMock("net", pts))
	m.StartAll(nil)
	defer m.StopAll()

	// 等待至少一次采集（Interval=50ms，等待 200ms）
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	count := len(collected)
	mu.Unlock()

	if count == 0 {
		t.Error("期望至少采集到 1 条指标")
	}
}

// TestPluginManager_StopPlugin 停止后不再产生新指标
func TestPluginManager_StopPlugin(t *testing.T) {
	var mu sync.Mutex
	var collected []model.MetricPoint

	m := New(func(pts []model.MetricPoint) {
		mu.Lock()
		collected = append(collected, pts...)
		mu.Unlock()
	})

	pts := []model.MetricPoint{{Name: "cpu", Value: 1}}
	m.Register(newMock("cpu_plugin", pts))
	m.StartAll(nil)

	time.Sleep(120 * time.Millisecond)

	if err := m.StopPlugin("cpu_plugin"); err != nil {
		t.Fatalf("StopPlugin 失败: %v", err)
	}

	info, ok := m.Get("cpu_plugin")
	if !ok || info.Status != StatusStopped {
		t.Errorf("期望状态 stopped，实际 %v", info.Status)
	}

	// 确保停止后不再有新数据
	mu.Lock()
	before := len(collected)
	mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	after := len(collected)
	mu.Unlock()

	if after > before {
		t.Errorf("停止后仍在采集：before=%d after=%d", before, after)
	}
}

// TestPluginManager_RestartPlugin 停止后可以重新启动
func TestPluginManager_RestartPlugin(t *testing.T) {
	var mu sync.Mutex
	var collected []model.MetricPoint

	m := New(func(pts []model.MetricPoint) {
		mu.Lock()
		collected = append(collected, pts...)
		mu.Unlock()
	})

	m.Register(newMock("svc", []model.MetricPoint{{Name: "req", Value: 5}}))
	m.StartAll(nil)
	time.Sleep(120 * time.Millisecond)
	m.StopPlugin("svc")

	mu.Lock()
	before := len(collected)
	mu.Unlock()

	if err := m.StartPlugin("svc"); err != nil {
		t.Fatalf("StartPlugin 失败: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	m.StopAll()

	mu.Lock()
	after := len(collected)
	mu.Unlock()

	if after <= before {
		t.Error("重启后期望产生新指标")
	}
}

// TestPluginManager_InitError 初始化失败时状态应为 error
func TestPluginManager_InitError(t *testing.T) {
	m := New(nil)
	errP := &errInitPlugin{}
	m.Register(errP)
	m.StartAll(nil)
	time.Sleep(50 * time.Millisecond)

	info, ok := m.Get("error_plugin")
	if !ok {
		t.Fatal("期望找到 error_plugin")
	}
	if info.Status != StatusError {
		t.Errorf("期望状态 error，实际 %s", info.Status)
	}
	if info.ErrorMessage == "" {
		t.Error("期望包含错误信息")
	}
}

// errInitPlugin 总是 Init 失败的测试插件
type errInitPlugin struct{}

func (p *errInitPlugin) Name() string                        { return "error_plugin" }
func (p *errInitPlugin) Version() string                     { return "1.0.0" }
func (p *errInitPlugin) Init(_ map[string]string) error      { return errBadInit }
func (p *errInitPlugin) Collect() []model.MetricPoint        { return nil }
func (p *errInitPlugin) Interval() time.Duration             { return 10 * time.Second }
func (p *errInitPlugin) Stop()                               {}

var errBadInit = errors.New("初始化失败：缺少必要配置")

// TestPluginAPI_List GET /api/v1/plugins 应返回插件列表
func TestPluginAPI_List(t *testing.T) {
	m := New(nil)
	m.Register(newMock("p1", nil))
	m.Register(newMock("p2", nil))

	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
	var list []PluginInfo
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("期望 2 个插件，实际 %d", len(list))
	}
}

// TestPluginAPI_StopStart POST start/stop 应改变插件状态
func TestPluginAPI_StopStart(t *testing.T) {
	m := New(nil)
	m.Register(newMock("toggle", nil))
	m.StartAll(nil)
	time.Sleep(30 * time.Millisecond)

	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	// 停止
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/toggle/stop", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("stop 期望 204，实际 %d", rr.Code)
	}

	info, _ := m.Get("toggle")
	if info.Status != StatusStopped {
		t.Errorf("期望状态 stopped，实际 %s", info.Status)
	}

	// 启动
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/toggle/start", nil)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNoContent {
		t.Errorf("start 期望 204，实际 %d", rr2.Code)
	}
	m.StopAll()
}

// TestPluginAPI_NotFound 操作不存在的插件应返回 404
func TestPluginAPI_NotFound(t *testing.T) {
	m := New(nil)
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/nonexistent/stop", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("期望 404，实际 %d", rr.Code)
	}
}

// TestNetworkPlugin_Collect 网络插件应返回有效指标（系统一定有网络接口）
func TestNetworkPlugin_Collect(t *testing.T) {
	p := NewNetworkPlugin()
	if err := p.Init(map[string]string{}); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	pts := p.Collect()
	if len(pts) == 0 {
		t.Skip("系统无网络接口，跳过测试")
	}
	for _, pt := range pts {
		if pt.Name == "" {
			t.Error("指标名称不应为空")
		}
		if pt.Labels["interface"] == "" {
			t.Error("network 指标应有 interface 标签")
		}
	}
}

// TestDockerPlugin_CollectNoDocker Docker 不可用时 Collect 应静默返回 nil
func TestDockerPlugin_CollectNoDocker(t *testing.T) {
	p := NewDockerPlugin()
	p.available = false // 直接设置不可用
	pts := p.Collect()
	if pts != nil {
		t.Error("Docker 不可用时期望返回 nil")
	}
}

// TestGPUPlugin_CollectNoGPU GPU 不可用时 Collect 应静默返回 nil
func TestGPUPlugin_CollectNoGPU(t *testing.T) {
	p := NewGPUPlugin()
	p.available = false // 直接设置不可用
	pts := p.Collect()
	if pts != nil {
		t.Error("GPU 不可用时期望返回 nil")
	}
}

// TestParseMemMB 内存字符串解析
func TestParseMemMB(t *testing.T) {
	cases := []struct {
		input string
		wantMB float64
		wantOK bool
	}{
		{"128MiB", 128, true},
		{"1GiB", 1024, true},
		{"512MB", 512, true},
		{"--", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		mb, ok := parseMemMB(tc.input)
		if ok != tc.wantOK {
			t.Errorf("parseMemMB(%q) ok=%v，期望 %v", tc.input, ok, tc.wantOK)
		}
		if ok && mb != tc.wantMB {
			t.Errorf("parseMemMB(%q) = %.1f，期望 %.1f", tc.input, mb, tc.wantMB)
		}
	}
}
