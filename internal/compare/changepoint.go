package compare

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

const (
	changeCheckInterval = 2 * time.Minute
	changeLookback      = 30 * time.Minute
	minChangePoints     = 12
	maxChangePoints     = 500
	changeThreshold     = 1.5
)

// ChangePoint 表示检测到的行为突变点。
type ChangePoint struct {
	Metric            string  `json:"metric"`
	Timestamp         int64   `json:"timestamp"`
	BeforeMean        float64 `json:"before_mean"`
	AfterMean         float64 `json:"after_mean"`
	SignificanceScore float64 `json:"significance_score"`
}

// ChangeDetector 周期性执行 CUSUM 变化检测。
type ChangeDetector struct {
	mu      sync.RWMutex
	db      *tsdb.TSDB
	changes []ChangePoint
	seen    map[string]struct{}
	stopCh  chan struct{}
}

// NewChangeDetector 创建变化检测器。
func NewChangeDetector(db *tsdb.TSDB) *ChangeDetector {
	return &ChangeDetector{
		db:     db,
		seen:   make(map[string]struct{}),
		stopCh: make(chan struct{}),
	}
}

// Start 启动后台检测协程。
func (d *ChangeDetector) Start() {
	go d.loop()
}

// Stop 停止后台检测协程。
func (d *ChangeDetector) Stop() {
	close(d.stopCh)
}

// GetChangePoints 返回已检测到的变化点。
func (d *ChangeDetector) GetChangePoints(metric string) []ChangePoint {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if metric == "" {
		out := make([]ChangePoint, len(d.changes))
		copy(out, d.changes)
		return out
	}

	var out []ChangePoint
	for _, cp := range d.changes {
		if cp.Metric == metric {
			out = append(out, cp)
		}
	}
	return out
}

func (d *ChangeDetector) loop() {
	d.detect()

	ticker := time.NewTicker(changeCheckInterval)
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

func (d *ChangeDetector) detect() {
	end := time.Now().UnixMilli()
	start := time.Now().Add(-changeLookback).UnixMilli()

	for _, metric := range d.db.ListMetrics() {
		points := mergePoints(d.db.GetSeries(metric), start, end)
		cp := detectCUSUMChange(metric, points)
		if cp != nil {
			d.add(*cp)
		}
	}
}

func (d *ChangeDetector) add(cp ChangePoint) {
	key := cp.Metric + ":" + time.UnixMilli(cp.Timestamp).UTC().Format(time.RFC3339Nano)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[key]; ok {
		return
	}
	d.seen[key] = struct{}{}

	d.changes = append([]ChangePoint{cp}, d.changes...)
	if len(d.changes) > maxChangePoints {
		trimmed := d.changes[maxChangePoints:]
		for _, old := range trimmed {
			delete(d.seen, old.Metric+":"+time.UnixMilli(old.Timestamp).UTC().Format(time.RFC3339Nano))
		}
		d.changes = d.changes[:maxChangePoints]
	}
}

func detectCUSUMChange(metric string, points []model.DataPoint) *ChangePoint {
	if len(points) < minChangePoints {
		return nil
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Timestamp < points[j].Timestamp
	})

	mean, stddev := meanStddev(points)
	if stddev < 1e-9 {
		return nil
	}

	cusum := 0.0
	minSum, maxSum := 0.0, 0.0
	minIdx, maxIdx := -1, -1

	for i, p := range points {
		cusum += p.Value - mean
		if cusum < minSum {
			minSum = cusum
			minIdx = i
		}
		if cusum > maxSum {
			maxSum = cusum
			maxIdx = i
		}
	}

	candidate := maxIdx
	if math.Abs(minSum) > math.Abs(maxSum) {
		candidate = minIdx
	}
	if candidate < 2 || candidate >= len(points)-3 {
		return nil
	}

	beforeMean, _ := meanStddev(points[:candidate+1])
	afterMean, _ := meanStddev(points[candidate+1:])
	delta := math.Abs(afterMean - beforeMean)
	significance := delta / stddev

	if significance < changeThreshold {
		return nil
	}

	return &ChangePoint{
		Metric:            metric,
		Timestamp:         points[candidate+1].Timestamp,
		BeforeMean:        roundTo(beforeMean, 4),
		AfterMean:         roundTo(afterMean, 4),
		SignificanceScore: roundTo(significance, 4),
	}
}

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
