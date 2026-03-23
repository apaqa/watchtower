// Package config 负责加载和解析 WatchTower 的 YAML 配置文件
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是顶级配置结构，对应 watchtower.yaml 文件
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Agent         AgentConfig         `yaml:"agent"`
	Retention     RetentionConfig     `yaml:"retention"`
	Endpoints     []EndpointConfig    `yaml:"endpoints"`
	Alerts        []AlertConfig       `yaml:"alerts"`
	APIKeys       []APIKeyConfig      `yaml:"api_keys"`
	Notifications NotificationsConfig `yaml:"notifications"`
}

// ServerConfig 控制 HTTP 服务监听端口
type ServerConfig struct {
	IngestPort    int `yaml:"ingest_port"`    // 摄入 API 端口，默认 9090
	DashboardPort int `yaml:"dashboard_port"` // 仪表板端口，默认 8080
}

// AgentConfig 控制系统指标采集代理的行为
type AgentConfig struct {
	Enabled         bool `yaml:"enabled"`          // 是否启用指标采集，默认 true
	IntervalSeconds int  `yaml:"interval_seconds"` // 采集间隔秒数，默认 5
}

// RetentionConfig 控制数据保留策略
type RetentionConfig struct {
	MetricsHours int `yaml:"metrics_hours"` // 时序数据保留时长（小时），默认 1
	LogsHours    int `yaml:"logs_hours"`    // 日志数据保留时长（小时），默认 24
}

// EndpointConfig 描述一个 HTTP 端点探针的配置
type EndpointConfig struct {
	Name            string            `yaml:"name"`             // 探针唯一名称
	URL             string            `yaml:"url"`              // 目标 URL
	Method          string            `yaml:"method"`           // HTTP 方法，默认 GET
	ExpectedStatus  int               `yaml:"expected_status"`  // 期望状态码，默认 200
	IntervalSeconds int               `yaml:"interval_seconds"` // 探测间隔秒数，默认 30
	TimeoutMs       int               `yaml:"timeout_ms"`       // 超时毫秒数，默认 10000
	Headers         map[string]string `yaml:"headers"`          // 附加请求头（可选）
}

// APIKeyConfig 描述一个预配置的 API 密钥
type APIKeyConfig struct {
	Name        string   `yaml:"name"`        // 人类可读标识
	Key         string   `yaml:"key"`         // 密钥明文（建议在生产中使用环境变量替换）
	Permissions []string `yaml:"permissions"` // "read" | "write" | "admin"
}

// NotificationsConfig 描述通知系统的顶级配置
type NotificationsConfig struct {
	Channels []ChannelConfig `yaml:"channels"` // 通知渠道列表
}

// ChannelConfig 描述单个通知渠道
type ChannelConfig struct {
	Type       string   `yaml:"type"`       // "console" | "webhook" | "slack" | "discord" | "email"
	Severities []string `yaml:"severities"` // 过滤的严重级别；为空时接收所有级别
	URL        string   `yaml:"url"`        // Webhook/Slack/Discord 的目标 URL

	// Email 专用字段
	SMTPHost     string   `yaml:"smtp_host"`
	SMTPPort     int      `yaml:"smtp_port"`
	From         string   `yaml:"from"`
	To           []string `yaml:"to"`
	SMTPUsername string   `yaml:"smtp_username"`
	SMTPPassword string   `yaml:"smtp_password"`
}

// AlertConfig 描述一条预定义告警规则
type AlertConfig struct {
	Name       string  `yaml:"name"`           // 规则唯一名称
	Expression string  `yaml:"wql_expression"` // WQL 查询表达式
	Operator   string  `yaml:"operator"`       // 比较运算符
	Threshold  float64 `yaml:"threshold"`      // 比较阈值
	Duration   string  `yaml:"duration"`       // 持续时长（如 "5m"），空或 "0" 表示立即
	Severity   string  `yaml:"severity"`       // 严重级别：critical/warning/info
	WebhookURL string  `yaml:"webhook_url"`    // Webhook 通知 URL（可选）
}

// Default 返回全量默认配置（无配置文件时使用）
func Default() *Config {
	return &Config{
		Server:    ServerConfig{IngestPort: 9090, DashboardPort: 8080},
		Agent:     AgentConfig{Enabled: true, IntervalSeconds: 5},
		Retention: RetentionConfig{MetricsHours: 1, LogsHours: 24},
	}
}

// Load 从指定路径读取并解析 YAML 配置文件
//
// 若文件不存在，返回默认配置（不报错）；
// 若文件存在但解析失败，返回错误。
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // 文件不存在时静默使用默认值
		}
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}

	// YAML 中的零值字段会覆盖 Default() 的值，在此补充
	cfg.applyDefaults()
	return cfg, nil
}

// IngestAddr 返回摄入服务的监听地址字符串
func (c *Config) IngestAddr() string {
	return fmt.Sprintf(":%d", c.Server.IngestPort)
}

// DashboardAddr 返回仪表板服务的监听地址字符串
func (c *Config) DashboardAddr() string {
	return fmt.Sprintf(":%d", c.Server.DashboardPort)
}

// applyDefaults 对已解析配置中的零值字段填充合理默认值
func (c *Config) applyDefaults() {
	if c.Server.IngestPort == 0 {
		c.Server.IngestPort = 9090
	}
	if c.Server.DashboardPort == 0 {
		c.Server.DashboardPort = 8080
	}
	if c.Agent.IntervalSeconds == 0 {
		c.Agent.IntervalSeconds = 5
	}
	if c.Retention.MetricsHours == 0 {
		c.Retention.MetricsHours = 1
	}
	if c.Retention.LogsHours == 0 {
		c.Retention.LogsHours = 24
	}
	for i := range c.Endpoints {
		if c.Endpoints[i].Method == "" {
			c.Endpoints[i].Method = "GET"
		}
		if c.Endpoints[i].ExpectedStatus == 0 {
			c.Endpoints[i].ExpectedStatus = 200
		}
		if c.Endpoints[i].IntervalSeconds == 0 {
			c.Endpoints[i].IntervalSeconds = 30
		}
		if c.Endpoints[i].TimeoutMs == 0 {
			c.Endpoints[i].TimeoutMs = 10000
		}
	}
	for i := range c.Alerts {
		if c.Alerts[i].Duration == "" {
			c.Alerts[i].Duration = "0"
		}
		if c.Alerts[i].Severity == "" {
			c.Alerts[i].Severity = "warning"
		}
	}
}
