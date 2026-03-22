// Package tsdb 实现基于内存的时间序列数据库
package tsdb

import (
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// Series 存储某一指标名称 + 标签组合的时间序列数据点
// 采用读写互斥锁保证并发安全
type Series struct {
	mu     sync.RWMutex
	Name   string            // 指标名称
	Labels map[string]string // 标签集合
	points []model.DataPoint // 按时间戳升序排列的数据点切片
}

// newSeries 创建一个新的时间序列
func newSeries(name string, labels map[string]string) *Series {
	// 复制标签，避免外部修改影响序列
	labelsCopy := make(map[string]string, len(labels))
	for k, v := range labels {
		labelsCopy[k] = v
	}
	return &Series{
		Name:   name,
		Labels: labelsCopy,
		points: make([]model.DataPoint, 0, 64),
	}
}

// Append 向序列末尾追加一个数据点
func (s *Series) Append(dp model.DataPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points = append(s.points, dp)
}

// QueryRange 返回 [start, end] 时间范围（Unix 毫秒）内的数据点副本
func (s *Series) QueryRange(start, end int64) []model.DataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.DataPoint, 0)
	for _, dp := range s.points {
		if dp.Timestamp >= start && dp.Timestamp <= end {
			result = append(result, dp)
		}
	}
	return result
}

// Latest 返回最新的 n 个数据点；若序列点数不足 n 则返回全部
func (s *Series) Latest(n int) []model.DataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.points) == 0 {
		return nil
	}
	start := len(s.points) - n
	if start < 0 {
		start = 0
	}
	result := make([]model.DataPoint, len(s.points)-start)
	copy(result, s.points[start:])
	return result
}

// Cleanup 删除早于 cutoff（Unix 毫秒）的数据点，释放内存
func (s *Series) Cleanup(cutoff int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 找到第一个不早于 cutoff 的数据点索引
	idx := 0
	for idx < len(s.points) && s.points[idx].Timestamp < cutoff {
		idx++
	}
	if idx > 0 {
		// 使用 copy 避免内存泄漏
		s.points = append([]model.DataPoint(nil), s.points[idx:]...)
	}
}

// Len 返回当前序列中数据点数量（线程安全）
func (s *Series) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.points)
}

// retentionDuration 是数据点在内存中保留的最大时长
const retentionDuration = time.Hour

// cutoffTime 计算当前保留截止时间（Unix 毫秒）
func cutoffTime() int64 {
	return time.Now().Add(-retentionDuration).UnixMilli()
}
