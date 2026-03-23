// Package ingest — Prometheus 文本格式解析和抓取端点
package ingest

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/apaqa/watchtower/internal/model"
)

// ── Prometheus 文本格式解析器 ─────────────────────────────────────────────────

// parsePrometheusText 将 Prometheus 文本格式解析为 MetricPoint 切片。
//
// 支持的格式：
//   - # HELP <name> <description>  （注释行，跳过）
//   - # TYPE <name> <type>         （类型声明，跳过；所有类型均存为常规指标）
//   - <name>[{<labels>}] <value> [<timestamp_ms>]
//
// 直方图/摘要的各桶/分位数各自存为独立时间序列。
func parsePrometheusText(text string) ([]model.MetricPoint, error) {
	var points []model.MetricPoint
	scanner := bufio.NewScanner(strings.NewReader(text))
	now := time.Now().UnixMilli()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行
		if line == "" {
			continue
		}

		// 跳过注释行（# HELP 和 # TYPE）
		if strings.HasPrefix(line, "#") {
			continue
		}

		mp, err := parseMetricLine(line, now)
		if err != nil {
			// 容错：跳过无法解析的行，不中断整批解析
			continue
		}
		points = append(points, mp)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("扫描 Prometheus 文本失败: %w", err)
	}
	return points, nil
}

// parseMetricLine 解析单行 Prometheus 指标，格式：
//
//	metric_name[{label1="v1",label2="v2"}] <value> [<timestamp_ms>]
func parseMetricLine(line string, defaultTS int64) (model.MetricPoint, error) {
	// 找到指标名称的结束位置（遇到空格、{、换行）
	nameEnd := strings.IndexAny(line, "{ \t")
	if nameEnd < 0 {
		nameEnd = len(line)
	}
	name := line[:nameEnd]
	if name == "" {
		return model.MetricPoint{}, fmt.Errorf("指标名称为空")
	}

	rest := strings.TrimLeftFunc(line[nameEnd:], unicode.IsSpace)
	labels := map[string]string{}

	// 解析标签集合 {k="v",...}
	if strings.HasPrefix(rest, "{") {
		closeBrace := strings.Index(rest, "}")
		if closeBrace < 0 {
			return model.MetricPoint{}, fmt.Errorf("标签花括号未闭合")
		}
		labelStr := rest[1:closeBrace]
		var err error
		labels, err = parseLabels(labelStr)
		if err != nil {
			return model.MetricPoint{}, fmt.Errorf("解析标签失败: %w", err)
		}
		rest = strings.TrimLeftFunc(rest[closeBrace+1:], unicode.IsSpace)
	}

	// 分割剩余部分（值 + 可选时间戳）
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return model.MetricPoint{}, fmt.Errorf("缺少数值")
	}

	// 解析数值（支持 NaN、+Inf、-Inf）
	value, err := parseFloat(fields[0])
	if err != nil {
		return model.MetricPoint{}, fmt.Errorf("解析数值 %q 失败: %w", fields[0], err)
	}

	// 解析可选时间戳（Prometheus 文本格式使用毫秒整数）
	ts := defaultTS
	if len(fields) >= 2 {
		tsVal, err := strconv.ParseFloat(fields[1], 64)
		if err == nil {
			ts = int64(tsVal)
		}
	}

	return model.MetricPoint{
		Name:      name,
		Labels:    labels,
		Value:     value,
		Timestamp: ts,
	}, nil
}

// parseLabels 将 `key="value",key2="value2"` 形式的字符串解析为 map。
// 支持值中的简单转义序列（\\、\"、\n）。
func parseLabels(s string) (map[string]string, error) {
	labels := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return labels, nil
	}

	for len(s) > 0 {
		// 解析键
		eqIdx := strings.Index(s, "=")
		if eqIdx < 0 {
			break
		}
		key := strings.TrimSpace(s[:eqIdx])
		s = s[eqIdx+1:]

		// 解析以 " 开头的值
		if !strings.HasPrefix(s, "\"") {
			return nil, fmt.Errorf("标签值未以引号开头")
		}
		s = s[1:] // 跳过开头引号

		// 读取到未转义的闭合引号
		var sb strings.Builder
		for {
			if len(s) == 0 {
				return nil, fmt.Errorf("标签值引号未闭合")
			}
			c := s[0]
			s = s[1:]
			if c == '"' {
				break
			}
			if c == '\\' && len(s) > 0 {
				switch s[0] {
				case 'n':
					sb.WriteByte('\n')
				case '\\':
					sb.WriteByte('\\')
				case '"':
					sb.WriteByte('"')
				default:
					sb.WriteByte('\\')
					sb.WriteByte(s[0])
				}
				s = s[1:]
			} else {
				sb.WriteByte(c)
			}
		}
		labels[key] = sb.String()

		// 跳过逗号和空格，继续解析下一对
		s = strings.TrimLeftFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	}
	return labels, nil
}

// parseFloat 解析 Prometheus 数值，支持 NaN、+Inf、-Inf
func parseFloat(s string) (float64, error) {
	switch strings.ToLower(s) {
	case "nan":
		return math.NaN(), nil
	case "+inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(s, 64)
}

// ── HTTP 处理器 ───────────────────────────────────────────────────────────────

// handlePrometheusIngest 处理 POST /api/v1/metrics/prometheus。
// 接受 Prometheus 文本格式（text/plain），解析后写入 TSDB。
func (s *Server) handlePrometheusIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// 限制请求体大小为 4MB
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)

	var sb strings.Builder
	scanner := bufio.NewScanner(r.Body)
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
		return
	}

	points, err := parsePrometheusText(sb.String())
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusBadRequest)
		return
	}

	if len(points) > 0 {
		s.db.Write(points)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handlePrometheusScrape 处理 GET /metrics —— 将 TSDB 最新数据以 Prometheus 文本格式导出。
// 此端点供其他 Prometheus 实例抓取 WatchTower 自身的指标。
func (s *Server) handlePrometheusScrape(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	now := time.Now().UnixMilli()
	fiveMinAgo := now - 5*60*1000

	// 获取所有指标名称并排序，确保输出稳定
	names := s.db.ListMetrics()
	sort.Strings(names)

	var sb strings.Builder
	for _, name := range names {
		seriesList := s.db.GetSeries(name)
		if len(seriesList) == 0 {
			continue
		}

		// 每个指标名输出一次 TYPE 声明
		sb.WriteString("# TYPE ")
		sb.WriteString(promEscapeName(name))
		sb.WriteString(" gauge\n")

		for _, series := range seriesList {
			// 取最近 5 分钟内的最新数据点
			pts := series.QueryRange(fiveMinAgo, now)
			if len(pts) == 0 {
				continue
			}
			latest := pts[len(pts)-1]

			sb.WriteString(promEscapeName(name))
			sb.WriteString(promFormatLabels(series.Labels))
			sb.WriteByte(' ')
			sb.WriteString(strconv.FormatFloat(latest.Value, 'f', -1, 64))
			sb.WriteByte(' ')
			sb.WriteString(strconv.FormatInt(latest.Timestamp, 10))
			sb.WriteByte('\n')
		}
	}

	fmt.Fprint(w, sb.String())
}

// promEscapeName 将指标名中不合法的字符替换为下划线
func promEscapeName(name string) string {
	var sb strings.Builder
	for i, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == ':' ||
			(i > 0 && c >= '0' && c <= '9') {
			sb.WriteRune(c)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

// promFormatLabels 将标签 map 格式化为 Prometheus 标签字符串 {k="v",...}
func promFormatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	// 对键排序，确保输出稳定
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteString(`="`)
		sb.WriteString(promEscapeLabelValue(labels[k]))
		sb.WriteByte('"')
	}
	sb.WriteByte('}')
	return sb.String()
}

// promEscapeLabelValue 转义标签值中的特殊字符
func promEscapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}
