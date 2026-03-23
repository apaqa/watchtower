// Package forecast 提供基于线性回归的资源预测和容量规划能力。
// 对 TSDB 中的指标序列拟合 y = mx + b，预测未来 1h/24h/7d 的值，
// 并对有界指标（如磁盘/内存使用率）估算何时达到阈值。
package forecast

import (
	"errors"
	"math"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const (
	// minPoints 是执行预测所需的最少数据点数
	minPoints = 5
	// forecastWindow 是从 TSDB 读取的历史数据窗口
	forecastWindow = 30 * time.Minute
	// trendThreshold 趋势判断阈值：每小时相对变化率超过此值视为趋势显著
	trendThreshold = 0.02 // 2% / hour
)

// Trend 描述指标的变化方向
type Trend string

const (
	TrendIncreasing Trend = "increasing"
	TrendDecreasing Trend = "decreasing"
	TrendStable     Trend = "stable"
)

// ForecastResult 存储对某指标的线性回归预测结果
type ForecastResult struct {
	MetricName      string  `json:"metric_name"`
	CurrentValue    float64 `json:"current_value"`
	Predicted1h     float64 `json:"predicted_value_1h"`
	Predicted24h    float64 `json:"predicted_value_24h"`
	Predicted7d     float64 `json:"predicted_value_7d"`
	Trend           Trend   `json:"trend"`
	ConfidenceScore float64 `json:"confidence_score"` // R²，范围 0–1
}

// ExhaustionEstimate 对有界指标预测何时达到指定阈值（如磁盘占用率达到 90%）
type ExhaustionEstimate struct {
	MetricName     string  `json:"metric_name"`
	CurrentValue   float64 `json:"current_value"`
	Threshold      float64 `json:"threshold"`
	HoursUntilFull float64 `json:"hours_until_threshold"`
	DaysUntilFull  float64 `json:"days_until_threshold"`
	ExhaustedAtMs  int64   `json:"exhausted_at_ms"` // Unix 毫秒；0 表示不会达到阈值
	Reachable      bool    `json:"reachable"`        // 按当前趋势是否会到达阈值
}

// regressionParams 存储线性回归拟合参数
type regressionParams struct {
	slope     float64 // 斜率 m（单位：value/second）
	intercept float64 // 截距 b（对应 t0 时刻的预测值）
	r2        float64 // 决定系数 R²（置信度）
	t0        int64   // 参考时间戳（第一个数据点的 Unix 毫秒）
}

// Forecaster 持有 TSDB 引用，提供指标预测和容量估算能力
type Forecaster struct {
	db *tsdb.TSDB
}

// New 创建预测引擎实例
func New(db *tsdb.TSDB) *Forecaster {
	return &Forecaster{db: db}
}

// Forecast 对指定指标执行线性回归，返回 1h/24h/7d 预测值及趋势方向
func (f *Forecaster) Forecast(metricName string) (*ForecastResult, error) {
	points, err := f.getPoints(metricName)
	if err != nil {
		return nil, err
	}

	params, err := linearRegression(points)
	if err != nil {
		return nil, err
	}

	// 最后一个数据点距 t0 的秒数，作为"当前 x"
	lastXSec := float64(points[len(points)-1].Timestamp-params.t0) / 1000.0
	current := points[len(points)-1].Value

	// 在 lastX 基础上各加时间偏移量得到预测值
	pred1h := params.slope*(lastXSec+3600) + params.intercept
	pred24h := params.slope*(lastXSec+86400) + params.intercept
	pred7d := params.slope*(lastXSec+7*86400) + params.intercept

	return &ForecastResult{
		MetricName:      metricName,
		CurrentValue:    roundTo(current, 4),
		Predicted1h:     roundTo(pred1h, 4),
		Predicted24h:    roundTo(pred24h, 4),
		Predicted7d:     roundTo(pred7d, 4),
		Trend:           classifyTrend(params.slope, current),
		ConfidenceScore: roundTo(params.r2, 4),
	}, nil
}

// Exhaustion 预测有界指标（如使用率百分比）何时达到指定阈值
func (f *Forecaster) Exhaustion(metricName string, threshold float64) (*ExhaustionEstimate, error) {
	points, err := f.getPoints(metricName)
	if err != nil {
		return nil, err
	}

	params, err := linearRegression(points)
	if err != nil {
		return nil, err
	}

	current := points[len(points)-1].Value
	lastXSec := float64(points[len(points)-1].Timestamp-params.t0) / 1000.0

	est := &ExhaustionEstimate{
		MetricName:   metricName,
		CurrentValue: roundTo(current, 4),
		Threshold:    threshold,
	}

	// 斜率 <= 0 时指标不会上涨至阈值
	if params.slope <= 0 {
		est.Reachable = false
		return est, nil
	}

	// 求解 threshold = slope*x + intercept → x = (threshold - intercept) / slope
	// x 是相对于 t0 的秒数
	xThresholdSec := (threshold - params.intercept) / params.slope
	secondsUntil := xThresholdSec - lastXSec

	if secondsUntil <= 0 {
		// 已经超过阈值或恰好处于阈值
		est.Reachable = true
		est.HoursUntilFull = 0
		est.DaysUntilFull = 0
		est.ExhaustedAtMs = points[len(points)-1].Timestamp
		return est, nil
	}

	est.Reachable = true
	est.HoursUntilFull = roundTo(secondsUntil/3600, 2)
	est.DaysUntilFull = roundTo(secondsUntil/86400, 2)
	est.ExhaustedAtMs = params.t0 + int64(xThresholdSec*1000)
	return est, nil
}

// getPoints 从 TSDB 查询指定指标最近 forecastWindow 内的数据点
// 若不足 minPoints 则返回错误
func (f *Forecaster) getPoints(metricName string) ([]model.DataPoint, error) {
	now := time.Now()
	end := now.UnixMilli()
	start := now.Add(-forecastWindow).UnixMilli()

	series := f.db.GetSeries(metricName)
	if len(series) == 0 {
		return nil, errors.New("metric not found: " + metricName)
	}

	// 使用标签最少的第一条序列（通常系统指标只有一条）
	points := series[0].QueryRange(start, end)
	if len(points) < minPoints {
		return nil, errors.New("insufficient data for forecasting: " + metricName)
	}

	return points, nil
}

// linearRegression 对数据点序列拟合 y = m*x + b，其中 x 为相对于第一个点的秒数
// 返回斜率、截距（基于 t0 坐标系）、R² 以及参考时间 t0
func linearRegression(points []model.DataPoint) (regressionParams, error) {
	n := float64(len(points))
	if n < 2 {
		return regressionParams{}, errors.New("线性回归至少需要 2 个数据点")
	}

	t0 := points[0].Timestamp // 以第一个点为时间原点（减少数值误差）

	var sumX, sumY, sumXY, sumX2 float64
	for _, p := range points {
		x := float64(p.Timestamp-t0) / 1000.0 // 转换为秒
		y := p.Value
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	denom := n*sumX2 - sumX*sumX
	if math.Abs(denom) < 1e-10 {
		// 所有 x 值相同（极少情况），返回水平线，R²=0
		return regressionParams{slope: 0, intercept: sumY / n, r2: 0, t0: t0}, nil
	}

	slope := (n*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / n

	// 计算 R²（决定系数）
	meanY := sumY / n
	var ssTot, ssRes float64
	for _, p := range points {
		x := float64(p.Timestamp-t0) / 1000.0
		predicted := slope*x + intercept
		residual := p.Value - predicted
		ssRes += residual * residual
		diff := p.Value - meanY
		ssTot += diff * diff
	}

	r2 := 0.0
	if ssTot > 1e-10 {
		r2 = 1.0 - ssRes/ssTot
		if r2 < 0 {
			r2 = 0
		}
	}

	return regressionParams{
		slope:     slope,
		intercept: intercept,
		r2:        r2,
		t0:        t0,
	}, nil
}

// classifyTrend 根据斜率与当前值计算每小时相对变化率，判断趋势方向
func classifyTrend(slope, currentValue float64) Trend {
	if math.Abs(currentValue) < 1e-10 {
		if slope > 1e-10 {
			return TrendIncreasing
		} else if slope < -1e-10 {
			return TrendDecreasing
		}
		return TrendStable
	}
	// 每小时相对变化率：slope(value/s) * 3600s / |current|
	relChange := slope * 3600 / math.Abs(currentValue)
	switch {
	case relChange > trendThreshold:
		return TrendIncreasing
	case relChange < -trendThreshold:
		return TrendDecreasing
	default:
		return TrendStable
	}
}

// roundTo 将浮点数四舍五入到指定小数位数
func roundTo(v float64, decimals int) float64 {
	factor := math.Pow(10, float64(decimals))
	return math.Round(v*factor) / factor
}
