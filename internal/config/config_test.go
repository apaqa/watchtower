// Package config 单元测试：验证配置加载与默认值行为
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemp 在临时目录写入 YAML 文件并返回路径
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "watchtower.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入临时配置文件失败: %v", err)
	}
	return path
}

// TestLoadNonexistent 验证配置文件不存在时返回默认配置（不报错）
func TestLoadNonexistent(t *testing.T) {
	cfg, err := Load("/nonexistent/path/watchtower.yaml")
	if err != nil {
		t.Fatalf("文件不存在时期望无错误，实际: %v", err)
	}
	if cfg.Server.IngestPort != 9090 {
		t.Errorf("默认 IngestPort 期望 9090，实际 %d", cfg.Server.IngestPort)
	}
	if cfg.Agent.Enabled != true {
		t.Error("默认 Agent.Enabled 应为 true")
	}
}

// TestLoadValidYAML 验证合法 YAML 文件被正确解析
func TestLoadValidYAML(t *testing.T) {
	path := writeTemp(t, `
server:
  ingest_port: 9091
  dashboard_port: 8081
agent:
  enabled: false
  interval_seconds: 10
retention:
  metrics_hours: 2
  logs_hours: 48
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("解析合法配置失败: %v", err)
	}
	if cfg.Server.IngestPort != 9091 {
		t.Errorf("IngestPort 期望 9091，实际 %d", cfg.Server.IngestPort)
	}
	if cfg.Server.DashboardPort != 8081 {
		t.Errorf("DashboardPort 期望 8081，实际 %d", cfg.Server.DashboardPort)
	}
	if cfg.Agent.Enabled != false {
		t.Error("Agent.Enabled 期望 false")
	}
	if cfg.Agent.IntervalSeconds != 10 {
		t.Errorf("IntervalSeconds 期望 10，实际 %d", cfg.Agent.IntervalSeconds)
	}
	if cfg.Retention.MetricsHours != 2 {
		t.Errorf("MetricsHours 期望 2，实际 %d", cfg.Retention.MetricsHours)
	}
}

// TestLoadInvalidYAML 验证无效 YAML 文件返回解析错误
func TestLoadInvalidYAML(t *testing.T) {
	path := writeTemp(t, `
server: {
  this is: [not valid yaml
`)

	_, err := Load(path)
	if err == nil {
		t.Error("期望解析错误，实际无错误")
	}
}

// TestLoadEndpoints 验证端点配置被正确解析
func TestLoadEndpoints(t *testing.T) {
	path := writeTemp(t, `
endpoints:
  - name: test-api
    url: http://localhost:9090/health
    method: GET
    expected_status: 200
    interval_seconds: 30
    timeout_ms: 5000
  - name: test-post
    url: http://example.com/api
    method: POST
    expected_status: 201
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("解析端点配置失败: %v", err)
	}
	if len(cfg.Endpoints) != 2 {
		t.Fatalf("期望 2 个端点，实际 %d 个", len(cfg.Endpoints))
	}
	if cfg.Endpoints[0].Name != "test-api" {
		t.Errorf("第一个端点名称期望 test-api，实际 %q", cfg.Endpoints[0].Name)
	}
	if cfg.Endpoints[0].TimeoutMs != 5000 {
		t.Errorf("timeout_ms 期望 5000，实际 %d", cfg.Endpoints[0].TimeoutMs)
	}
	// 第二个端点未设置 timeout_ms，应使用默认值 10000
	if cfg.Endpoints[1].TimeoutMs != 10000 {
		t.Errorf("默认 timeout_ms 期望 10000，实际 %d", cfg.Endpoints[1].TimeoutMs)
	}
}

// TestLoadAlerts 验证预定义告警规则被正确解析
func TestLoadAlerts(t *testing.T) {
	path := writeTemp(t, `
alerts:
  - name: high_cpu
    wql_expression: avg(cpu_usage_percent[5m])
    operator: ">"
    threshold: 85
    duration: 5m
    severity: warning
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("解析告警配置失败: %v", err)
	}
	if len(cfg.Alerts) != 1 {
		t.Fatalf("期望 1 条告警规则，实际 %d 条", len(cfg.Alerts))
	}
	a := cfg.Alerts[0]
	if a.Name != "high_cpu" {
		t.Errorf("告警名称期望 high_cpu，实际 %q", a.Name)
	}
	if a.Threshold != 85 {
		t.Errorf("阈值期望 85，实际 %f", a.Threshold)
	}
	if a.Duration != "5m" {
		t.Errorf("持续时长期望 5m，实际 %q", a.Duration)
	}
}

// TestDefaultValues 验证 Default() 返回正确的默认值
func TestDefaultValues(t *testing.T) {
	cfg := Default()
	if cfg.Server.IngestPort != 9090 {
		t.Errorf("默认 IngestPort 期望 9090，实际 %d", cfg.Server.IngestPort)
	}
	if cfg.Server.DashboardPort != 8080 {
		t.Errorf("默认 DashboardPort 期望 8080，实际 %d", cfg.Server.DashboardPort)
	}
	if !cfg.Agent.Enabled {
		t.Error("默认 Agent.Enabled 应为 true")
	}
	if cfg.Agent.IntervalSeconds != 5 {
		t.Errorf("默认 IntervalSeconds 期望 5，实际 %d", cfg.Agent.IntervalSeconds)
	}
	if cfg.Retention.MetricsHours != 1 {
		t.Errorf("默认 MetricsHours 期望 1，实际 %d", cfg.Retention.MetricsHours)
	}
	if cfg.Retention.LogsHours != 24 {
		t.Errorf("默认 LogsHours 期望 24，实际 %d", cfg.Retention.LogsHours)
	}
}

// TestApplyDefaults 验证零值字段被 applyDefaults 补充为合理值
func TestApplyDefaults(t *testing.T) {
	path := writeTemp(t, `
endpoints:
  - name: no-defaults
    url: http://example.com
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	ep := cfg.Endpoints[0]
	if ep.Method != "GET" {
		t.Errorf("默认 Method 期望 GET，实际 %q", ep.Method)
	}
	if ep.ExpectedStatus != 200 {
		t.Errorf("默认 ExpectedStatus 期望 200，实际 %d", ep.ExpectedStatus)
	}
	if ep.IntervalSeconds != 30 {
		t.Errorf("默认 IntervalSeconds 期望 30，实际 %d", ep.IntervalSeconds)
	}
	if ep.TimeoutMs != 10000 {
		t.Errorf("默认 TimeoutMs 期望 10000，实际 %d", ep.TimeoutMs)
	}
}

// TestIngestAddrDashboardAddr 验证地址格式化方法
func TestIngestAddrDashboardAddr(t *testing.T) {
	cfg := Default()
	if cfg.IngestAddr() != ":9090" {
		t.Errorf("IngestAddr 期望 :9090，实际 %q", cfg.IngestAddr())
	}
	if cfg.DashboardAddr() != ":8080" {
		t.Errorf("DashboardAddr 期望 :8080，实际 %q", cfg.DashboardAddr())
	}
}
