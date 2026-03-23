// Package anomaly 基于 Z-score 对 TSDB 指标进行异常检测。
// 每 30 秒扫描所有活跃指标序列，对超过 mean±3σ 的点生成异常事件，
// 并将最新 500 条事件存储在环形缓冲区中。
package anomaly

import (
	"math"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const (
	// maxEvents 是环形缓冲区保留的最大异常事件数量
	maxEvents = 500
	// checkInterval 是后台检测的执行周期
	checkInterval = 30 * time.Second
	// baselineWindow 是计算基线均值和标准差的回溯时间窗口
	baselineWindow = 30 * time.Minute
	// zThreshold 触发异常所需的最低 Z-score（标准差倍数）
	zThreshold = 3.0
)

// Severity 描述异常的严重程度
type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// AnomalyEvent 描述一次检测到的异常
type AnomalyEvent struct {
	MetricName     string            `json:"metric_name"`
	Labels         map[string]string `json:"labels"`
	Timestamp      int64             `json:"timestamp"`       // Unix 毫秒
	Value          float64           `json:"value"`           // 实际观测值
	ExpectedValue  float64           `json:"expected_value"`  // 基线均值
	DeviationScore float64           `json:"deviation_score"` // Z-score
	Severity       Severity          `json:"severity"`
}

// Detector 管理异常检测逻辑和事件历史
type Detector struct {
	mu     sync.RWMutex
	db     *tsdb.TSDB
	events []AnomalyEvent // 环形缓冲区（最新在前）
	stopCh chan struct{}
}

// New 创建异常检测器实例
func New(db *tsdb.TSDB) *Detector {
	return &Detector{
		db:     db,
		stopCh: make(chan struct{}),
	}
}

// Start 启动后台检测 goroutine
func (d *Detector) Start() {
	go d.detectLoop()
}

// Stop 停止后台检测 goroutine
func (d *Detector) Stop() {
	close(d.stopCh)
}

// GetEvents 返回最近的异常事件（最新在前）。
// metric 为空字符串时返回全部，否则按指标名称过滤。
func (d *Detector) GetEvents(metric string) []AnomalyEvent {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if metric == "" {
		out := make([]AnomalyEvent, len(d.events))
		copy(out, d.events)
		return out
	}
	var out []AnomalyEvent
	for _, e := range d.events {
		if e.MetricName == metric {
			out = append(out, e)
		}
	}
	return out
}

// Count 返回当前缓冲区中的事件数量
func (d *Detector) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.events)
}

// detectLoop 每 checkInterval 扫描一次
func (d *Detector) detectLoop() {
	// 启动时立即执行一次
	d.detect()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.detect()
		case <-d.stopCh:
			return
		}
	}
}

// detect 遍历所有活跃指标序列，对每条序列执行 Z-score 检测
func (d *Detector) detect() {
	now := time.Now()
	end := now.UnixMilli()
	start := now.Add(-baselineWindow).UnixMilli()

	metrics := d.db.ListMetrics()
	for _, name := range metrics {
		for _, s := range d.db.GetSeries(name) {
			points := s.QueryRange(start, end)
			if len(points) < 5 {
				// 样本不足：跳过，避免误报
				continue
			}
			ev := checkSeries(name, s.Labels, points)
			if ev != nil {
				d.addEvent(*ev)
			}
		}
	}
}

// checkSeries 对给定数据点切片执行 Z-score 检测。
// 以最新点为当前值，其余点为基线。
// 若检测到异常则返回 AnomalyEvent，否则返回 nil。
func checkSeries(name string, labels map[string]string, points []model.DataPoint) *AnomalyEvent {
	if len(points) < 2 {
		return nil
	}
	// 最新点作为检测目标，其余点组成基线
	latest := points[len(points)-1]
	baseline := points[:len(points)-1]

	mean, stddev := meanStddev(baseline)
	if stddev < 1e-10 {
		// 标准差极小：数据几乎无变化，跳过（避免除零和误报）
		return nil
	}

	z := math.Abs(latest.Value-mean) / stddev
	if z < zThreshold {
		return nil
	}

	// 复制标签，避免持有对 Series 内部 map 的引用
	labelsCopy := make(map[string]string, len(labels))
	for k, v := range labels {
		labelsCopy[k] = v
	}

	return &AnomalyEvent{
		MetricName:     name,
		Labels:         labelsCopy,
		Timestamp:      latest.Timestamp,
		Value:          latest.Value,
		ExpectedValue:  mean,
		DeviationScore: z,
		Severity:       classifySeverity(z),
	}
}

// addEvent 将事件插入环形缓冲区头部，并在超过 maxEvents 时截断尾部
func (d *Detector) addEvent(ev AnomalyEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append([]AnomalyEvent{ev}, d.events...)
	if len(d.events) > maxEvents {
		d.events = d.events[:maxEvents]
	}
}

// classifySeverity 根据 Z-score 确定异常严重程度
func classifySeverity(z float64) Severity {
	switch {
	case z >= 5:
		return SeverityHigh
	case z >= 3.5:
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// meanStddev 计算样本均值和总体标准差
func meanStddev(points []model.DataPoint) (mean, stddev float64) {
	if len(points) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, p := range points {
		sum += p.Value
	}
	mean = sum / float64(len(points))

	variance := 0.0
	for _, p := range points {
		diff := p.Value - mean
		variance += diff * diff
	}
	variance /= float64(len(points))
	stddev = math.Sqrt(variance)
	return mean, stddev
}
