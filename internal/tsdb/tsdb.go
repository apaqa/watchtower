// Package tsdb 实现基于内存的时间序列数据库，支持写入、查询和自动清理
package tsdb

import (
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// TSDB 是线程安全的内存时间序列数据库
// 按指标指纹（名称 + 标签哈希）组织序列
type TSDB struct {
	mu     sync.RWMutex
	series map[string]*Series // 指纹 → 序列
	stopCh chan struct{}       // 控制清理协程退出
}

// New 创建并启动一个新的 TSDB 实例，开始后台清理循环
func New() *TSDB {
	db := &TSDB{
		series: make(map[string]*Series),
		stopCh: make(chan struct{}),
	}
	// 启动后台清理协程，每 10 分钟删除过期数据点
	go db.cleanupLoop()
	return db
}

// Write 将一批指标数据点写入数据库；若序列不存在则自动创建
func (db *TSDB) Write(points []model.MetricPoint) {
	for _, mp := range points {
		fp := model.Fingerprint(mp.Name, mp.Labels)

		// 快速路径：序列已存在，只需读锁
		db.mu.RLock()
		s, ok := db.series[fp]
		db.mu.RUnlock()

		if !ok {
			// 慢路径：需要写锁创建新序列
			db.mu.Lock()
			// 二次检查，防止并发时重复创建
			s, ok = db.series[fp]
			if !ok {
				s = newSeries(mp.Name, mp.Labels)
				db.series[fp] = s
			}
			db.mu.Unlock()
		}

		ts := mp.Timestamp
		if ts == 0 {
			ts = time.Now().UnixMilli()
		}
		s.Append(model.DataPoint{Timestamp: ts, Value: mp.Value})
	}
}

// QueryRange 返回指定名称和标签在时间范围 [start, end]（Unix 毫秒）内的数据点
func (db *TSDB) QueryRange(name string, labels map[string]string, start, end int64) []model.DataPoint {
	fp := model.Fingerprint(name, labels)

	db.mu.RLock()
	s, ok := db.series[fp]
	db.mu.RUnlock()

	if !ok {
		return nil
	}
	return s.QueryRange(start, end)
}

// QueryLatest 返回指定指标最近 n 个数据点
func (db *TSDB) QueryLatest(name string, labels map[string]string, n int) []model.DataPoint {
	fp := model.Fingerprint(name, labels)

	db.mu.RLock()
	s, ok := db.series[fp]
	db.mu.RUnlock()

	if !ok {
		return nil
	}
	return s.Latest(n)
}

// ListMetrics 返回所有已知的指标名称（去重）
func (db *TSDB) ListMetrics() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, s := range db.series {
		if _, exists := seen[s.Name]; !exists {
			seen[s.Name] = struct{}{}
			result = append(result, s.Name)
		}
	}
	return result
}

// GetSeries 返回所有与指定指标名称匹配的序列（包含不同标签组合）
func (db *TSDB) GetSeries(name string) []*Series {
	db.mu.RLock()
	defer db.mu.RUnlock()

	result := make([]*Series, 0)
	for _, s := range db.series {
		if s.Name == name {
			result = append(result, s)
		}
	}
	return result
}

// Stop 停止后台清理协程
func (db *TSDB) Stop() {
	close(db.stopCh)
}

// cleanupLoop 每 10 分钟执行一次过期数据清理
func (db *TSDB) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			db.cleanup()
		case <-db.stopCh:
			return
		}
	}
}

// cleanup 删除所有序列中超过保留期限（1小时）的数据点
func (db *TSDB) cleanup() {
	cutoff := cutoffTime()

	db.mu.RLock()
	series := make([]*Series, 0, len(db.series))
	for _, s := range db.series {
		series = append(series, s)
	}
	db.mu.RUnlock()

	for _, s := range series {
		s.Cleanup(cutoff)
	}
}

// SeriesCount 返回当前序列总数（用于测试和监控）
func (db *TSDB) SeriesCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.series)
}
