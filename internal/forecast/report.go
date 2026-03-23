// Package forecast — 容量报告：汇总系统核心指标的当前状态、趋势及预测，
// 评估各指标的健康状态（healthy / warning / critical）。
package forecast

import (
	"time"
)

const (
	// 容量报告涵盖的系统指标
	metricCPU    = "cpu_usage_percent"
	metricMemory = "memory_usage_percent"
	metricDisk   = "disk_usage_percent"

	// 默认耗尽阈值（%）
	exhaustionThreshold = 90.0
)

// HealthStatus 描述指标当前的容量健康程度
type HealthStatus string

const (
	HealthHealthy  HealthStatus = "healthy"
	HealthWarning  HealthStatus = "warning"
	HealthCritical HealthStatus = "critical"
)

// MetricCapacity 存储单个系统指标的容量分析结果
type MetricCapacity struct {
	Name          string       `json:"name"`
	CurrentAvg    float64      `json:"current_avg"`
	Peak          float64      `json:"peak"`
	Trend         Trend        `json:"trend"`
	Predicted24h  float64      `json:"predicted_24h"`
	GrowthPerDay  float64      `json:"growth_per_day"`  // 每天增长量（与指标单位相同）
	DaysUntilFull float64      `json:"days_until_full"` // 0 表示不适用或不会耗尽
	HealthStatus  HealthStatus `json:"health_status"`
}

// CapacityReport 包含所有系统指标的容量规划摘要
type CapacityReport struct {
	GeneratedAtMs int64          `json:"generated_at_ms"`
	CPU           MetricCapacity `json:"cpu"`
	Memory        MetricCapacity `json:"memory"`
	Disk          MetricCapacity `json:"disk"`
}

// GenerateReport 构建完整的系统容量报告。
// 若某指标数据不足，对应字段以零值填充并标注 HealthUnknown。
func (f *Forecaster) GenerateReport() CapacityReport {
	return CapacityReport{
		GeneratedAtMs: time.Now().UnixMilli(),
		CPU:           f.buildMetricCapacity(metricCPU, cpuHealth),
		Memory:        f.buildMetricCapacity(metricMemory, memoryHealth),
		Disk:          f.buildMetricCapacity(metricDisk, diskHealth),
	}
}

// buildMetricCapacity 为单个指标构建 MetricCapacity，healthFn 决定如何判定健康状态
func (f *Forecaster) buildMetricCapacity(
	name string,
	healthFn func(predicted24h, daysUntilFull float64, reachable bool) HealthStatus,
) MetricCapacity {
	mc := MetricCapacity{Name: name}

	// 获取预测结果
	fr, err := f.Forecast(name)
	if err != nil {
		mc.HealthStatus = HealthHealthy // 数据不足时不报警
		return mc
	}

	// 获取原始数据以计算均值和峰值
	points, err := f.getPoints(name)
	if err == nil && len(points) > 0 {
		var sum, peak float64
		for _, p := range points {
			sum += p.Value
			if p.Value > peak {
				peak = p.Value
			}
		}
		mc.CurrentAvg = roundTo(sum/float64(len(points)), 2)
		mc.Peak = roundTo(peak, 2)
	}

	mc.Trend = fr.Trend
	mc.Predicted24h = fr.Predicted24h
	// 每天增长量 = slope(value/s) * 86400s
	mc.GrowthPerDay = roundTo(f.slopeFor(name)*86400, 4)

	// 预测耗尽时间
	est, err := f.Exhaustion(name, exhaustionThreshold)
	if err == nil {
		mc.DaysUntilFull = est.DaysUntilFull
		mc.HealthStatus = healthFn(fr.Predicted24h, est.DaysUntilFull, est.Reachable)
	} else {
		mc.HealthStatus = healthFn(fr.Predicted24h, 0, false)
	}

	return mc
}

// slopeFor 返回指定指标的线性回归斜率（value/s）；若数据不足则返回 0
func (f *Forecaster) slopeFor(name string) float64 {
	points, err := f.getPoints(name)
	if err != nil {
		return 0
	}
	params, err := linearRegression(points)
	if err != nil {
		return 0
	}
	return params.slope
}

// cpuHealth 根据 24h 预测值判断 CPU 健康状态
func cpuHealth(predicted24h, _ float64, _ bool) HealthStatus {
	switch {
	case predicted24h >= 90:
		return HealthCritical
	case predicted24h >= 80:
		return HealthWarning
	default:
		return HealthHealthy
	}
}

// memoryHealth 根据 24h 预测值判断内存健康状态
func memoryHealth(predicted24h, _ float64, _ bool) HealthStatus {
	switch {
	case predicted24h >= 90:
		return HealthCritical
	case predicted24h >= 80:
		return HealthWarning
	default:
		return HealthHealthy
	}
}

// diskHealth 根据距耗尽天数判断磁盘健康状态
func diskHealth(predicted24h, daysUntilFull float64, reachable bool) HealthStatus {
	if !reachable {
		// 磁盘不增长或在减少：按预测值判断
		switch {
		case predicted24h >= 90:
			return HealthCritical
		case predicted24h >= 80:
			return HealthWarning
		default:
			return HealthHealthy
		}
	}
	switch {
	case daysUntilFull <= 7:
		return HealthCritical
	case daysUntilFull <= 30:
		return HealthWarning
	default:
		return HealthHealthy
	}
}
