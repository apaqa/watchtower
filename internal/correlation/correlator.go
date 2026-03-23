// Package correlation 计算两条 TSDB 指标序列之间的 Pearson 相关系数，
// 并支持对给定指标自动发现最相关的其他指标。
package correlation

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 支持的时间窗口 ────────────────────────────────────────────────────────────

var windowDurations = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"1d":  24 * time.Hour,
}

// ── 结果类型 ──────────────────────────────────────────────────────────────────

// CorrelationPoint 是散点图中的一个数据对 (x=metric_a 值, y=metric_b 值)
type CorrelationPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// CorrelationResult 描述两条指标序列的 Pearson 相关分析结果
type CorrelationResult struct {
	MetricA        string             `json:"metric_a"`
	MetricB        string             `json:"metric_b"`
	Coefficient    float64            `json:"coefficient"`     // Pearson r：-1 到 1
	Interpretation string             `json:"interpretation"`  // 语义标签
	SampleCount    int                `json:"sample_count"`
	TimeRange      string             `json:"time_range"`
	Points         []CorrelationPoint `json:"points,omitempty"` // 散点图数据（按需返回）
}

// AutoCorrelationResult 描述对某指标的自动相关分析结果
type AutoCorrelationResult struct {
	SourceMetric string              `json:"source_metric"`
	Related      []CorrelationResult `json:"related"`
}

// Correlator 持有对 TSDB 的引用，提供相关分析方法
type Correlator struct {
	db *tsdb.TSDB
}

// New 创建相关分析器实例
func New(db *tsdb.TSDB) *Correlator {
	return &Correlator{db: db}
}

// Correlate 计算两条指标在指定时间窗口内的 Pearson 相关系数。
// window 参数支持 5m/15m/30m/1h/6h/1d；空字符串默认 30m。
// 返回带散点图数据的完整结果。
func (c *Correlator) Correlate(metricA, metricB, window string) CorrelationResult {
	dur, ok := windowDurations[window]
	if !ok {
		dur = windowDurations["30m"]
		window = "30m"
	}

	now := time.Now()
	end := now.UnixMilli()
	start := now.Add(-dur).UnixMilli()

	// 聚合指标 A 和 B 的全部序列（多标签组合求均值对齐）
	aVals := aggregateSeries(c.db.GetSeries(metricA), start, end)
	bVals := aggregateSeries(c.db.GetSeries(metricB), start, end)

	r, pts := pearsonWithPoints(aVals, bVals)

	return CorrelationResult{
		MetricA:        metricA,
		MetricB:        metricB,
		Coefficient:    r,
		Interpretation: interpret(r),
		SampleCount:    len(pts),
		TimeRange:      window,
		Points:         pts,
	}
}

// AutoCorrelate 遍历所有其他指标，找出与 sourceMetric 相关系数绝对值最高的前 5 条。
func (c *Correlator) AutoCorrelate(sourceMetric, window string) AutoCorrelationResult {
	allMetrics := c.db.ListMetrics()

	type candidate struct {
		result CorrelationResult
		absR   float64
	}
	var candidates []candidate

	for _, m := range allMetrics {
		if m == sourceMetric {
			continue
		}
		// AutoCorrelate 不返回散点图数据以减少响应体积
		res := c.correlateNoPoints(sourceMetric, m, window)
		if res.SampleCount < 3 {
			continue // 样本不足，忽略
		}
		candidates = append(candidates, candidate{result: res, absR: math.Abs(res.Coefficient)})
	}

	// 按 |r| 降序取前 5
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].absR > candidates[j].absR
	})
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}

	related := make([]CorrelationResult, len(candidates))
	for i, c := range candidates {
		related[i] = c.result
	}

	return AutoCorrelationResult{
		SourceMetric: sourceMetric,
		Related:      related,
	}
}

// correlateNoPoints 与 Correlate 相同，但不附带散点图数据
func (c *Correlator) correlateNoPoints(metricA, metricB, window string) CorrelationResult {
	res := c.Correlate(metricA, metricB, window)
	res.Points = nil
	return res
}

// ── 内部计算函数 ───────────────────────────────────────────────────────────────

// aggregateSeries 将某指标的所有序列按时间桶（1分钟）对齐，
// 返回一个 bucket_ms → 均值 的有序映射（以切片对表示）
func aggregateSeries(series []*tsdb.Series, start, end int64) []bucketVal {
	const bucketMs = 60_000 // 1 分钟
	sums := make(map[int64]float64)
	cnts := make(map[int64]int)

	for _, s := range series {
		for _, dp := range s.QueryRange(start, end) {
			b := (dp.Timestamp / bucketMs) * bucketMs
			sums[b] += dp.Value
			cnts[b]++
		}
	}

	result := make([]bucketVal, 0, len(sums))
	for b, sum := range sums {
		result = append(result, bucketVal{t: b, v: sum / float64(cnts[b])})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].t < result[j].t })
	return result
}

type bucketVal struct {
	t int64
	v float64
}

// pearsonWithPoints 计算两组对齐时间序列的 Pearson r 值，并返回散点图数据对。
// 仅计算两组都存在的时间桶。
func pearsonWithPoints(a, b []bucketVal) (r float64, pts []CorrelationPoint) {
	// 构建时间桶交集
	bmap := make(map[int64]float64, len(b))
	for _, bv := range b {
		bmap[bv.t] = bv.v
	}

	var xs, ys []float64
	for _, av := range a {
		if bv, ok := bmap[av.t]; ok {
			xs = append(xs, av.v)
			ys = append(ys, bv)
		}
	}

	n := len(xs)
	if n < 2 {
		pts = make([]CorrelationPoint, n)
		for i := range xs {
			pts[i] = CorrelationPoint{X: xs[i], Y: ys[i]}
		}
		return 0, pts
	}

	// 计算均值
	var sumX, sumY float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
	}
	meanX := sumX / float64(n)
	meanY := sumY / float64(n)

	// 计算协方差和方差
	var cov, varX, varY float64
	for i := range xs {
		dx := xs[i] - meanX
		dy := ys[i] - meanY
		cov += dx * dy
		varX += dx * dx
		varY += dy * dy
	}

	denom := math.Sqrt(varX * varY)
	if denom < 1e-10 {
		r = 0
	} else {
		r = cov / denom
		// 将结果夹在 [-1, 1] 内，防止浮点误差溢出
		r = math.Max(-1, math.Min(1, r))
	}

	pts = make([]CorrelationPoint, n)
	for i := range xs {
		pts[i] = CorrelationPoint{X: roundF(xs[i]), Y: roundF(ys[i])}
	}
	return r, pts
}

// interpret 将 Pearson r 值转换为人类可读的语义标签
func interpret(r float64) string {
	abs := math.Abs(r)
	switch {
	case abs >= 0.7 && r > 0:
		return "strong_positive"
	case abs >= 0.3 && r > 0:
		return "weak_positive"
	case abs >= 0.7 && r < 0:
		return "strong_negative"
	case abs >= 0.3 && r < 0:
		return "weak_negative"
	default:
		return "none"
	}
}

// roundF 将浮点数四舍五入到小数点后 4 位，减小 JSON 体积
func roundF(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 4, 64), 64)
	return f
}
