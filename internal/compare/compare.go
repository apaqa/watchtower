// Package compare 提供指标窗口对比与变化检测能力。
package compare

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

var windowDurations = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"12h": 12 * time.Hour,
	"1d":  24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// Direction 表示指标变化方向。
type Direction string

const (
	DirectionUp     Direction = "up"
	DirectionDown   Direction = "down"
	DirectionStable Direction = "stable"
)

// CompareResult 描述两个时间窗口之间的平均值变化。
type CompareResult struct {
	Metric        string    `json:"metric"`
	CurrentAvg    float64   `json:"current_avg"`
	PreviousAvg   float64   `json:"previous_avg"`
	ChangePercent float64   `json:"change_percent"`
	Direction     Direction `json:"direction"`
}

// Report 汇总所有指标的窗口对比结果。
type Report struct {
	GeneratedAtMs  int64           `json:"generated_at_ms"`
	CurrentWindow  string          `json:"current_window"`
	PreviousWindow string          `json:"previous_window"`
	Results        []CompareResult `json:"results"`
}

// Engine 负责指标对比计算。
type Engine struct {
	db *tsdb.TSDB
}

// New 创建指标对比引擎。
func New(db *tsdb.TSDB) *Engine {
	return &Engine{db: db}
}

// Compare 对比指定指标在两个相邻时间窗口中的平均值。
func (e *Engine) Compare(metric, currentWindow, previousWindow string) (*CompareResult, error) {
	if metric == "" {
		return nil, errors.New("metric parameter required")
	}

	durCurrent, ok := windowDurations[currentWindow]
	if !ok {
		return nil, errors.New("invalid current window: " + currentWindow)
	}
	durPrevious, ok := windowDurations[previousWindow]
	if !ok {
		return nil, errors.New("invalid previous window: " + previousWindow)
	}

	now := time.Now()
	currentEnd := now.UnixMilli()
	currentStart := now.Add(-durCurrent).UnixMilli()
	previousEnd := currentStart
	previousStart := time.UnixMilli(previousEnd).Add(-durPrevious).UnixMilli()

	currentAvg, err := e.average(metric, currentStart, currentEnd)
	if err != nil {
		return nil, err
	}
	previousAvg, err := e.average(metric, previousStart, previousEnd)
	if err != nil {
		return nil, err
	}

	changePercent := percentChange(previousAvg, currentAvg)
	return &CompareResult{
		Metric:        metric,
		CurrentAvg:    roundTo(currentAvg, 4),
		PreviousAvg:   roundTo(previousAvg, 4),
		ChangePercent: roundTo(changePercent, 4),
		Direction:     detectDirection(changePercent),
	}, nil
}

// Report 对全部已知指标生成对比报告。
func (e *Engine) Report(currentWindow, previousWindow string) (*Report, error) {
	if _, ok := windowDurations[currentWindow]; !ok {
		return nil, errors.New("invalid current window: " + currentWindow)
	}
	if _, ok := windowDurations[previousWindow]; !ok {
		return nil, errors.New("invalid previous window: " + previousWindow)
	}

	metrics := e.db.ListMetrics()
	sort.Strings(metrics)

	results := make([]CompareResult, 0, len(metrics))
	for _, metric := range metrics {
		result, err := e.Compare(metric, currentWindow, previousWindow)
		if err != nil {
			continue
		}
		results = append(results, *result)
	}

	return &Report{
		GeneratedAtMs:  time.Now().UnixMilli(),
		CurrentWindow:  currentWindow,
		PreviousWindow: previousWindow,
		Results:        results,
	}, nil
}

func (e *Engine) average(metric string, start, end int64) (float64, error) {
	series := e.db.GetSeries(metric)
	if len(series) == 0 {
		return 0, errors.New("metric not found: " + metric)
	}

	points := mergePoints(series, start, end)
	if len(points) == 0 {
		return 0, errors.New("no data in requested window: " + metric)
	}

	sum := 0.0
	for _, p := range points {
		sum += p.Value
	}
	return sum / float64(len(points)), nil
}

func mergePoints(series []*tsdb.Series, start, end int64) []model.DataPoint {
	var points []model.DataPoint
	for _, s := range series {
		points = append(points, s.QueryRange(start, end)...)
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp < points[j].Timestamp
	})
	return points
}

func percentChange(previous, current float64) float64 {
	switch {
	case math.Abs(previous) < 1e-9 && math.Abs(current) < 1e-9:
		return 0
	case math.Abs(previous) < 1e-9:
		return 100
	default:
		return ((current - previous) / math.Abs(previous)) * 100
	}
}

func detectDirection(changePercent float64) Direction {
	const stableThreshold = 0.5
	switch {
	case changePercent > stableThreshold:
		return DirectionUp
	case changePercent < -stableThreshold:
		return DirectionDown
	default:
		return DirectionStable
	}
}

func roundTo(v float64, decimals int) float64 {
	factor := math.Pow(10, float64(decimals))
	return math.Round(v*factor) / factor
}
