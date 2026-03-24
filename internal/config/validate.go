package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Report 收集設定檔驗證時的警告與錯誤。
type Report struct {
	Warnings []string
	Errors   []string
}

// HasErrors 回傳是否存在阻擋啟動的驗證錯誤。
func (r Report) HasErrors() bool {
	return len(r.Errors) > 0
}

// Validate 驗證 WatchTower 設定檔的結構與數值是否合法。
func Validate(cfg *Config) Report {
	var report Report
	if cfg == nil {
		report.Errors = append(report.Errors, "config 為 nil")
		return report
	}

	validatePort(&report, "server.ingest_port", cfg.Server.IngestPort)
	validatePort(&report, "server.dashboard_port", cfg.Server.DashboardPort)
	if cfg.Server.IngestPort == cfg.Server.DashboardPort {
		report.Warnings = append(report.Warnings, "server.ingest_port 與 server.dashboard_port 使用相同連接埠")
	}

	validatePositiveInt(&report, "agent.interval_seconds", cfg.Agent.IntervalSeconds)
	validatePositiveInt(&report, "retention.metrics_hours", cfg.Retention.MetricsHours)
	validatePositiveInt(&report, "retention.logs_hours", cfg.Retention.LogsHours)

	for i, ep := range cfg.Endpoints {
		prefix := fmt.Sprintf("endpoints[%d]", i)
		requireString(&report, prefix+".name", ep.Name)
		requireString(&report, prefix+".url", ep.URL)
		validateURL(&report, prefix+".url", ep.URL)
		validatePositiveInt(&report, prefix+".interval_seconds", ep.IntervalSeconds)
		validatePositiveInt(&report, prefix+".timeout_ms", ep.TimeoutMs)
		if ep.ExpectedStatus < 100 || ep.ExpectedStatus > 599 {
			report.Errors = append(report.Errors, fmt.Sprintf("%s.expected_status 必須介於 100 到 599", prefix))
		}
	}
	if len(cfg.Endpoints) == 0 {
		report.Warnings = append(report.Warnings, "未設定任何 endpoints，HTTP 探測功能將不會執行")
	}

	for i, alert := range cfg.Alerts {
		prefix := fmt.Sprintf("alerts[%d]", i)
		requireString(&report, prefix+".name", alert.Name)
		requireString(&report, prefix+".wql_expression", alert.Expression)
		if alert.WebhookURL != "" {
			validateURL(&report, prefix+".webhook_url", alert.WebhookURL)
		}
	}

	for i, ch := range cfg.Notifications.Channels {
		prefix := fmt.Sprintf("notifications.channels[%d]", i)
		requireString(&report, prefix+".type", ch.Type)
		if needsURL(ch.Type) {
			requireString(&report, prefix+".url", ch.URL)
			validateURL(&report, prefix+".url", ch.URL)
		}
		if ch.Type == "email" {
			requireString(&report, prefix+".smtp_host", ch.SMTPHost)
			requireString(&report, prefix+".from", ch.From)
			if len(ch.To) == 0 {
				report.Errors = append(report.Errors, fmt.Sprintf("%s.to 為必填", prefix))
			}
			if ch.SMTPPort != 0 {
				validatePort(&report, prefix+".smtp_port", ch.SMTPPort)
			}
		}
	}

	for i, wh := range cfg.Webhooks {
		prefix := fmt.Sprintf("webhooks[%d]", i)
		requireString(&report, prefix+".name", wh.Name)
		requireString(&report, prefix+".path", wh.Path)
		if wh.Path != "" && !strings.HasPrefix(wh.Path, "/") {
			report.Errors = append(report.Errors, fmt.Sprintf("%s.path 必須以 / 開頭", prefix))
		}
	}

	for i, test := range cfg.SyntheticTests {
		prefix := fmt.Sprintf("synthetic_tests[%d]", i)
		requireString(&report, prefix+".name", test.Name)
		validatePositiveInt(&report, prefix+".interval_seconds", test.Interval)
		validatePositiveInt(&report, prefix+".timeout_ms", test.Timeout)
		if len(test.Steps) == 0 {
			report.Errors = append(report.Errors, fmt.Sprintf("%s.steps 至少需要一個步驟", prefix))
		}
		for j, step := range test.Steps {
			stepPrefix := fmt.Sprintf("%s.steps[%d]", prefix, j)
			requireString(&report, stepPrefix+".name", step.Name)
			requireString(&report, stepPrefix+".url", step.URL)
			validateURL(&report, stepPrefix+".url", step.URL)
			if step.ExpectedStatus != 0 && (step.ExpectedStatus < 100 || step.ExpectedStatus > 599) {
				report.Errors = append(report.Errors, fmt.Sprintf("%s.expected_status 必須介於 100 到 599", stepPrefix))
			}
		}
	}

	return report
}

func requireString(report *Report, field, value string) {
	if strings.TrimSpace(value) == "" {
		report.Errors = append(report.Errors, fmt.Sprintf("%s 為必填", field))
	}
}

func validatePositiveInt(report *Report, field string, value int) {
	if value <= 0 {
		report.Errors = append(report.Errors, fmt.Sprintf("%s 必須大於 0", field))
	}
}

func validatePort(report *Report, field string, port int) {
	if port < 1 || port > 65535 {
		report.Errors = append(report.Errors, fmt.Sprintf("%s 必須介於 1 到 65535", field))
	}
}

func validateURL(report *Report, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s 不是有效 URL: %v", field, err))
		return
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		report.Errors = append(report.Errors, fmt.Sprintf("%s 需要包含 scheme 與 host", field))
	}
}

func needsURL(channelType string) bool {
	switch channelType {
	case "webhook", "slack", "discord":
		return true
	default:
		return false
	}
}
