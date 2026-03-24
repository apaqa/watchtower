package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/config"
)

func TestParseArgsStatusDefaults(t *testing.T) {
	opts, cmd, err := parseArgs([]string{"status"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}
	if opts.server != defaultServer {
		t.Fatalf("expected default server %q, got %q", defaultServer, opts.server)
	}
	if opts.format != formatTable {
		t.Fatalf("expected default format %q, got %q", formatTable, opts.format)
	}
	if cmd.name != "status" || cmd.httpPath != "/api/v1/admin/status" {
		t.Fatalf("unexpected command: %+v", cmd)
	}
}

func TestParseArgsQueryWithJSONFormat(t *testing.T) {
	opts, cmd, err := parseArgs([]string{"--format", "json", "query", "avg(cpu_usage_percent[5m])"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}
	if opts.format != formatJSON {
		t.Fatalf("expected json format, got %q", opts.format)
	}
	if cmd.name != "query" || cmd.query["q"] != "avg(cpu_usage_percent[5m])" {
		t.Fatalf("unexpected command: %+v", cmd)
	}
}

func TestParseArgsAlertsCreate(t *testing.T) {
	_, cmd, err := parseArgs([]string{
		"alerts", "create",
		"--name", "high_cpu",
		"--expr", "avg(cpu_usage_percent[5m])",
		"--threshold", "80",
		"--severity", "warning",
	})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}
	rule, ok := cmd.body.(alert.AlertRule)
	if !ok {
		t.Fatalf("expected alert rule body, got %T", cmd.body)
	}
	if rule.Name != "high_cpu" || rule.Expression != "avg(cpu_usage_percent[5m])" || rule.Threshold != 80 {
		t.Fatalf("unexpected alert rule: %+v", rule)
	}
}

func TestParseArgsLogsSearch(t *testing.T) {
	_, cmd, err := parseArgs([]string{"logs", "search", "--query", "error", "--level", "error", "--limit", "20"})
	if err != nil {
		t.Fatalf("parseArgs returned error: %v", err)
	}
	if cmd.name != "logs_search" {
		t.Fatalf("unexpected command name: %s", cmd.name)
	}
	if cmd.query["q"] != "error" || cmd.query["level"] != "error" || cmd.query["limit"] != "20" {
		t.Fatalf("unexpected logs query: %+v", cmd.query)
	}
}

func TestPrintConfigReportTable(t *testing.T) {
	var out bytes.Buffer
	report := config.Report{
		Warnings: []string{"warn one"},
		Errors:   []string{"err one"},
	}
	if err := printConfigReport(&out, formatTable, report); err != nil {
		t.Fatalf("printConfigReport returned error: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "warning") || !strings.Contains(text, "error") {
		t.Fatalf("expected warning and error rows, got %q", text)
	}
}

func TestRunConfigValidateJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watchtower.yaml")
	content := strings.TrimSpace(`
server:
  ingest_port: 9090
  dashboard_port: 8080
agent:
  enabled: true
  interval_seconds: 5
retention:
  metrics_hours: 1
  logs_hours: 24
`)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"--format", "json", "config", "validate", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "\"Warnings\"") && !strings.Contains(stdout.String(), "\"warnings\"") {
		t.Fatalf("expected JSON output, got %q", stdout.String())
	}
}
