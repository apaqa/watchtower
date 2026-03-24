package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/apaqa/watchtower/internal/admin"
	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/wql"
)

const (
	defaultServer = "http://localhost:9090"
	formatTable   = "table"
	formatJSON    = "json"
)

type globalOptions struct {
	server string
	apiKey string
	format string
}

type commandSpec struct {
	name       string
	configPath string
	httpMethod string
	httpPath   string
	query      map[string]string
	body       any
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, cmd, err := parseArgs(args)
	if err != nil {
		return err
	}

	switch cmd.name {
	case "config_validate":
		report, err := validateConfigFile(cmd.configPath)
		if err != nil {
			return err
		}
		return printConfigReport(stdout, opts.format, report)
	default:
		client := newCLIClient(opts.server, opts.apiKey)
		return executeRemoteCommand(client, cmd, opts.format, stdout)
	}
}

func parseArgs(args []string) (globalOptions, commandSpec, error) {
	opts := globalOptions{
		server: defaultServer,
		format: formatTable,
	}

	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--server 缺少值")
			}
			opts.server = args[i]
		case "--api-key":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--api-key 缺少值")
			}
			opts.apiKey = args[i]
		case "--format":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--format 缺少值")
			}
			opts.format = args[i]
		default:
			filtered = append(filtered, args[i])
		}
	}

	if opts.format != formatTable && opts.format != formatJSON {
		return opts, commandSpec{}, fmt.Errorf("不支援的輸出格式: %s", opts.format)
	}
	if len(filtered) == 0 {
		return opts, commandSpec{}, errors.New(usageText())
	}

	switch filtered[0] {
	case "status":
		if len(filtered) != 1 {
			return opts, commandSpec{}, errors.New("status 不接受額外參數")
		}
		return opts, commandSpec{name: "status", httpMethod: http.MethodGet, httpPath: "/api/v1/admin/status"}, nil

	case "metrics":
		if len(filtered) == 2 && filtered[1] == "list" {
			return opts, commandSpec{name: "metrics_list", httpMethod: http.MethodGet, httpPath: "/api/v1/metrics/names"}, nil
		}
		return opts, commandSpec{}, errors.New("metrics 僅支援子命令 list")

	case "query":
		if len(filtered) != 2 {
			return opts, commandSpec{}, errors.New(`query 用法: watchtower-cli query "avg(cpu_usage_percent[5m])"`)
		}
		return opts, commandSpec{
			name:       "query",
			httpMethod: http.MethodGet,
			httpPath:   "/api/v1/query",
			query:      map[string]string{"q": filtered[1]},
		}, nil

	case "alerts":
		return parseAlertsCommand(opts, filtered[1:])

	case "logs":
		return parseLogsCommand(opts, filtered[1:])

	case "config":
		if len(filtered) == 3 && filtered[1] == "validate" {
			return opts, commandSpec{name: "config_validate", configPath: filtered[2]}, nil
		}
		return opts, commandSpec{}, errors.New("config 用法: watchtower-cli config validate watchtower.yaml")
	}

	return opts, commandSpec{}, fmt.Errorf("未知命令: %s", filtered[0])
}

func parseAlertsCommand(opts globalOptions, args []string) (globalOptions, commandSpec, error) {
	if len(args) == 1 && args[0] == "list" {
		return opts, commandSpec{name: "alerts_list", httpMethod: http.MethodGet, httpPath: "/api/v1/alerts/active"}, nil
	}
	if len(args) == 0 || args[0] != "create" {
		return opts, commandSpec{}, errors.New("alerts 僅支援 list 或 create")
	}

	create := alert.AlertRule{Severity: "warning", Operator: ">"}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--name 缺少值")
			}
			create.Name = args[i]
		case "--expr":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--expr 缺少值")
			}
			create.Expression = args[i]
		case "--threshold":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--threshold 缺少值")
			}
			value, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				return opts, commandSpec{}, fmt.Errorf("無法解析 --threshold: %w", err)
			}
			create.Threshold = value
		case "--severity":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--severity 缺少值")
			}
			create.Severity = args[i]
		default:
			return opts, commandSpec{}, fmt.Errorf("未知 alerts create 參數: %s", args[i])
		}
	}

	if create.Name == "" || create.Expression == "" {
		return opts, commandSpec{}, errors.New("alerts create 需要 --name 與 --expr")
	}
	return opts, commandSpec{
		name:       "alerts_create",
		httpMethod: http.MethodPost,
		httpPath:   "/api/v1/alerts/rules",
		body:       create,
	}, nil
}

func parseLogsCommand(opts globalOptions, args []string) (globalOptions, commandSpec, error) {
	if len(args) == 0 || args[0] != "search" {
		return opts, commandSpec{}, errors.New("logs 僅支援子命令 search")
	}

	query := map[string]string{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--query":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--query 缺少值")
			}
			query["q"] = args[i]
		case "--level":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--level 缺少值")
			}
			query["level"] = args[i]
		case "--limit":
			i++
			if i >= len(args) {
				return opts, commandSpec{}, errors.New("--limit 缺少值")
			}
			if _, err := strconv.Atoi(args[i]); err != nil {
				return opts, commandSpec{}, fmt.Errorf("無法解析 --limit: %w", err)
			}
			query["limit"] = args[i]
		default:
			return opts, commandSpec{}, fmt.Errorf("未知 logs search 參數: %s", args[i])
		}
	}

	if query["q"] == "" {
		return opts, commandSpec{}, errors.New("logs search 需要 --query")
	}
	return opts, commandSpec{
		name:       "logs_search",
		httpMethod: http.MethodGet,
		httpPath:   "/api/v1/logs",
		query:      query,
	}, nil
}

func usageText() string {
	return strings.TrimSpace(`
watchtower-cli 用法:
  watchtower-cli [--server URL] [--api-key KEY] [--format table|json] status
  watchtower-cli [--server URL] [--api-key KEY] [--format table|json] metrics list
  watchtower-cli [--server URL] [--api-key KEY] [--format table|json] query "avg(cpu_usage_percent[5m])"
  watchtower-cli [--server URL] [--api-key KEY] [--format table|json] alerts list
  watchtower-cli [--server URL] [--api-key KEY] [--format table|json] alerts create --name NAME --expr EXPR --threshold N --severity LEVEL
  watchtower-cli [--server URL] [--api-key KEY] [--format table|json] logs search --query TEXT [--level LEVEL] [--limit N]
  watchtower-cli [--format table|json] config validate watchtower.yaml`)
}

type cliClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newCLIClient(baseURL, apiKey string) *cliClient {
	return &cliClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *cliClient) doJSON(method, path string, query map[string]string, body any, out any) error {
	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		values := endpoint.Query()
		for k, v := range query {
			values.Set(k, v)
		}
		endpoint.RawQuery = values.Encode()
	}

	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, endpoint.String(), requestBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("請求失敗 (%d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func executeRemoteCommand(client *cliClient, cmd commandSpec, format string, stdout io.Writer) error {
	switch cmd.name {
	case "status":
		var status admin.SystemStatus
		if err := client.doJSON(cmd.httpMethod, cmd.httpPath, cmd.query, cmd.body, &status); err != nil {
			return err
		}
		return printStatus(stdout, format, status)

	case "metrics_list":
		var names []string
		if err := client.doJSON(cmd.httpMethod, cmd.httpPath, cmd.query, cmd.body, &names); err != nil {
			return err
		}
		return printMetricNames(stdout, format, names)

	case "query":
		var result wql.Result
		if err := client.doJSON(cmd.httpMethod, cmd.httpPath, cmd.query, cmd.body, &result); err != nil {
			return err
		}
		return printQueryResult(stdout, format, result)

	case "alerts_list":
		var alerts []alert.Alert
		if err := client.doJSON(cmd.httpMethod, cmd.httpPath, cmd.query, cmd.body, &alerts); err != nil {
			return err
		}
		return printAlerts(stdout, format, alerts)

	case "alerts_create":
		var response map[string]any
		if err := client.doJSON(cmd.httpMethod, cmd.httpPath, cmd.query, cmd.body, &response); err != nil {
			return err
		}
		return printJSONOrTable(stdout, format, response, []string{"status", "name"})

	case "logs_search":
		var entries []model.LogEntry
		if err := client.doJSON(cmd.httpMethod, cmd.httpPath, cmd.query, cmd.body, &entries); err != nil {
			return err
		}
		return printLogs(stdout, format, entries)
	}
	return fmt.Errorf("未實作的命令: %s", cmd.name)
}

func validateConfigFile(path string) (config.Report, error) {
	_, report, err := config.LoadAndValidate(path)
	return report, err
}

func printStatus(w io.Writer, format string, status admin.SystemStatus) error {
	if format == formatJSON {
		return printJSON(w, status)
	}
	rows := [][]string{
		{"version", status.Version},
		{"go_version", status.GoVersion},
		{"uptime_seconds", fmt.Sprintf("%.2f", status.UptimeSecs)},
		{"goroutines", strconv.Itoa(status.Goroutines)},
		{"heap_alloc_mb", fmt.Sprintf("%.2f", status.HeapAllocMB)},
		{"tsdb_series_count", strconv.Itoa(status.TSDBSeriesCount)},
		{"tsdb_total_points", strconv.Itoa(status.TSDBTotalPoints)},
		{"timestamp_ms", strconv.FormatInt(status.TimestampMs, 10)},
	}
	return printTable(w, []string{"FIELD", "VALUE"}, rows)
}

func printMetricNames(w io.Writer, format string, names []string) error {
	if format == formatJSON {
		return printJSON(w, names)
	}
	rows := make([][]string, 0, len(names))
	for _, name := range names {
		rows = append(rows, []string{name})
	}
	return printTable(w, []string{"NAME"}, rows)
}

func printQueryResult(w io.Writer, format string, result wql.Result) error {
	if format == formatJSON {
		return printJSON(w, result)
	}

	switch result.Type {
	case wql.ResultScalar:
		value := ""
		if result.Scalar != nil {
			value = fmt.Sprintf("%g", *result.Scalar)
		}
		return printTable(w, []string{"TYPE", "VALUE"}, [][]string{{string(result.Type), value}})
	case wql.ResultBool:
		value := ""
		if result.Bool != nil {
			value = strconv.FormatBool(*result.Bool)
		}
		return printTable(w, []string{"TYPE", "VALUE"}, [][]string{{string(result.Type), value}})
	case wql.ResultVector:
		rows := make([][]string, 0, len(result.Vector))
		for _, sample := range result.Vector {
			rows = append(rows, []string{formatLabels(sample.Labels), fmt.Sprintf("%g", sample.Value)})
		}
		return printTable(w, []string{"LABELS", "VALUE"}, rows)
	default:
		return printTable(w, []string{"TYPE"}, [][]string{{string(result.Type)}})
	}
}

func printAlerts(w io.Writer, format string, alerts []alert.Alert) error {
	if format == formatJSON {
		return printJSON(w, alerts)
	}
	rows := make([][]string, 0, len(alerts))
	for _, item := range alerts {
		rows = append(rows, []string{
			item.Rule.Name,
			item.Rule.Severity,
			string(item.State),
			fmt.Sprintf("%g", item.LastValue),
			item.Message,
		})
	}
	return printTable(w, []string{"NAME", "SEVERITY", "STATE", "VALUE", "MESSAGE"}, rows)
}

func printLogs(w io.Writer, format string, entries []model.LogEntry) error {
	if format == formatJSON {
		return printJSON(w, entries)
	}
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []string{
			time.UnixMilli(entry.Timestamp).Format(time.RFC3339),
			string(entry.Level),
			entry.Source,
			entry.Message,
		})
	}
	return printTable(w, []string{"TIME", "LEVEL", "SOURCE", "MESSAGE"}, rows)
}

func printConfigReport(w io.Writer, format string, report config.Report) error {
	if format == formatJSON {
		return printJSON(w, report)
	}
	rows := make([][]string, 0, len(report.Errors)+len(report.Warnings)+1)
	for _, item := range report.Errors {
		rows = append(rows, []string{"error", item})
	}
	for _, item := range report.Warnings {
		rows = append(rows, []string{"warning", item})
	}
	if len(rows) == 0 {
		rows = append(rows, []string{"ok", "設定檔驗證通過"})
	}
	return printTable(w, []string{"LEVEL", "MESSAGE"}, rows)
}

func printJSONOrTable(w io.Writer, format string, value map[string]any, orderedKeys []string) error {
	if format == formatJSON {
		return printJSON(w, value)
	}
	rows := make([][]string, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if v, ok := value[key]; ok {
			rows = append(rows, []string{key, fmt.Sprint(v)})
		}
	}
	return printTable(w, []string{"FIELD", "VALUE"}, rows)
}

func printJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func printTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if len(headers) > 0 {
		if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}
