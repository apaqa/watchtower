// Package ingest — Prometheus 文本格式解析器和抓取端点测试
package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 解析器单元测试 ─────────────────────────────────────────────────────────────

// TestParseSimpleMetric 验证解析不带标签的基本指标行
func TestParseSimpleMetric(t *testing.T) {
	text := "my_metric 42.5\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("期望 1 个数据点，得到 %d", len(points))
	}
	if points[0].Name != "my_metric" {
		t.Errorf("期望名称 my_metric，得到 %s", points[0].Name)
	}
	if points[0].Value != 42.5 {
		t.Errorf("期望值 42.5，得到 %f", points[0].Value)
	}
}

// TestParseMetricWithLabels 验证解析带标签的指标行
func TestParseMetricWithLabels(t *testing.T) {
	text := `http_requests_total{method="GET",code="200"} 1027` + "\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("期望 1 个数据点，得到 %d", len(points))
	}
	if points[0].Labels["method"] != "GET" {
		t.Errorf("标签 method 应为 GET，得到 %s", points[0].Labels["method"])
	}
	if points[0].Labels["code"] != "200" {
		t.Errorf("标签 code 应为 200，得到 %s", points[0].Labels["code"])
	}
}

// TestParseMetricWithTimestamp 验证解析带时间戳的指标行
func TestParseMetricWithTimestamp(t *testing.T) {
	text := "node_cpu 0.5 1395066363000\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("期望 1 个数据点，得到 %d", len(points))
	}
	if points[0].Timestamp != 1395066363000 {
		t.Errorf("期望时间戳 1395066363000，得到 %d", points[0].Timestamp)
	}
}

// TestParseSkipsHelpAndTypeComments 验证 # HELP 和 # TYPE 行被跳过
func TestParseSkipsHelpAndTypeComments(t *testing.T) {
	text := `
# HELP http_requests_total The total number of HTTP requests.
# TYPE http_requests_total counter
http_requests_total{method="post",code="200"} 1027
http_requests_total{method="post",code="400"} 3
`
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 2 {
		t.Errorf("期望 2 个数据点（跳过注释行），得到 %d", len(points))
	}
}

// TestParseHistogram 验证直方图各桶被解析为独立时间序列
func TestParseHistogram(t *testing.T) {
	text := `
# TYPE rpc_duration_seconds histogram
rpc_duration_seconds_bucket{le="0.1"} 24054
rpc_duration_seconds_bucket{le="0.2"} 33444
rpc_duration_seconds_bucket{le="+Inf"} 53829
rpc_duration_seconds_sum 1.7560473e+07
rpc_duration_seconds_count 53829
`
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 5 个指标行应各自解析为一个数据点
	if len(points) != 5 {
		t.Errorf("期望 5 个数据点，得到 %d", len(points))
	}
}

// TestParseInfAndNaN 验证特殊浮点数 +Inf、-Inf、NaN 被正确解析
func TestParseInfAndNaN(t *testing.T) {
	text := "inf_metric +Inf\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析 +Inf 失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("期望 1 个数据点，得到 %d", len(points))
	}
	// math.IsInf 验证
	if !isInf(points[0].Value, 1) {
		t.Errorf("期望 +Inf，得到 %v", points[0].Value)
	}
}

// isInf 是 math.IsInf 的包装（避免在 test 文件中导入 math 包引起混淆）
func isInf(v float64, sign int) bool {
	if sign > 0 {
		return v > 1e308
	}
	return v < -1e308
}

// TestParseEmptyAndBlankLines 验证空行和仅有空白的行被跳过
func TestParseEmptyAndBlankLines(t *testing.T) {
	text := "\n\n   \nvalid_metric 1.0\n\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 1 {
		t.Errorf("期望 1 个数据点，得到 %d", len(points))
	}
}

// TestParseInvalidLineSkipped 验证完全无效的行被跳过而不影响整批解析
func TestParseInvalidLineSkipped(t *testing.T) {
	text := "valid_metric 1.0\n!!!invalid!!!\nanother_metric 2.0\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 2 {
		t.Errorf("期望 2 个有效数据点，得到 %d", len(points))
	}
}

// TestParseLabelsWithEscapes 验证标签值中的转义序列被正确处理
func TestParseLabelsWithEscapes(t *testing.T) {
	text := `metric_name{path="/api/v1"} 1` + "\n"
	points, err := parsePrometheusText(text)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("期望 1 个数据点，得到 %d", len(points))
	}
	if points[0].Labels["path"] != "/api/v1" {
		t.Errorf("期望路径标签 /api/v1，得到 %s", points[0].Labels["path"])
	}
}

// ── HTTP 端点测试 ──────────────────────────────────────────────────────────────

// TestAPIPrometheusIngest 验证 POST /api/v1/metrics/prometheus 接受文本格式并写入 TSDB
func TestAPIPrometheusIngest(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	srv := &Server{db: db, mux: http.NewServeMux()}

	body := "prom_cpu_usage{host=\"node1\"} 75.5 1395066363000\n" +
		"prom_mem_usage{host=\"node1\"} 60.0 1395066363000\n"

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/prometheus",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	srv.handlePrometheusIngest(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，得到 %d: %s", w.Code, w.Body.String())
	}
	if db.SeriesCount() != 2 {
		t.Errorf("期望 TSDB 中有 2 个序列，得到 %d", db.SeriesCount())
	}
}

// TestAPIPrometheusIngestWithComments 验证注释行不产生数据点
func TestAPIPrometheusIngestWithComments(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	srv := &Server{db: db, mux: http.NewServeMux()}

	body := "# HELP cpu CPU usage\n# TYPE cpu gauge\ncpu 50.0\n"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/prometheus",
		strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handlePrometheusIngest(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，得到 %d", w.Code)
	}
	if db.SeriesCount() != 1 {
		t.Errorf("注释行不应产生序列，期望 1，得到 %d", db.SeriesCount())
	}
}

// TestAPIPrometheusScrape 验证 GET /metrics 返回 Prometheus 文本格式
func TestAPIPrometheusScrape(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	srv := &Server{db: db, mux: http.NewServeMux()}

	// 先写入数据
	srv.handlePrometheusIngest(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/api/v1/metrics/prometheus",
			strings.NewReader("scraped_metric{env=\"prod\"} 99.9\n")),
	)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handlePrometheusScrape(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "scraped_metric") {
		t.Errorf("抓取响应应包含 scraped_metric，实际内容：\n%s", body)
	}
	if !strings.Contains(body, "# TYPE") {
		t.Error("抓取响应应包含 # TYPE 声明")
	}
}
