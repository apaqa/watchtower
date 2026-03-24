// Package tsdb 实现基于内存的时间序列数据库，支持写入、查询和自动清理
package tsdb

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// TSDB 是线程安全的内存时间序列数据库
// 按指标指纹（名称 + 标签哈希）组织序列
type TSDB struct {
	mu      sync.RWMutex
	series  map[string]*Series // 指纹 → 序列
	stopCh  chan struct{}       // 控制清理协程退出
	storage *StorageManager    // 可选的持久化存储管理器（nil 表示纯内存模式）
}

// New 创建并启动一个新的 TSDB 实例，开始后台清理循环（纯内存模式）
func New() *TSDB {
	db := &TSDB{
		series: make(map[string]*Series),
		stopCh: make(chan struct{}),
	}
	// 启动后台清理协程，每 10 分钟删除过期数据点
	go db.cleanupLoop()
	return db
}

// NewWithStorage 创建带磁盘持久化的 TSDB 实例
// 启动时从 dataDir 加载已有块数据；后台每 30 秒将新数据刷盘
func NewWithStorage(dataDir string) (*TSDB, error) {
	sm, err := NewStorageManager(dataDir)
	if err != nil {
		return nil, fmt.Errorf("初始化存储管理器失败: %w", err)
	}
	db := &TSDB{
		series:  make(map[string]*Series),
		stopCh:  make(chan struct{}),
		storage: sm,
	}
	go db.cleanupLoop()

	// 从磁盘恢复历史数据
	points, err := sm.LoadAll()
	if err != nil {
		return nil, fmt.Errorf("加载历史数据块失败: %w", err)
	}
	if len(points) > 0 {
		db.Write(points)
	}

	// 启动后台刷盘协程
	sm.StartFlushLoop()
	return db, nil
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
		dp := model.DataPoint{Timestamp: ts, Value: mp.Value}
		s.Append(dp)
		// 若持久化存储已初始化，同步写入活跃块
		if db.storage != nil {
			db.storage.write(fp, mp.Name, mp.Labels, dp)
		}
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

// Stop 停止后台清理协程，并（若启用持久化）执行最终刷盘
func (db *TSDB) Stop() {
	close(db.stopCh)
	if db.storage != nil {
		db.storage.Stop()
	}
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

// cleanup 删除原始序列中超过保留期限（1小时）的数据点
// 注意：带 :1m / :5m 后缀的降采样序列由 RetentionEngine 单独管理，此处跳过
func (db *TSDB) cleanup() {
	cutoff := cutoffTime()

	db.mu.RLock()
	series := make([]*Series, 0, len(db.series))
	for _, s := range db.series {
		series = append(series, s)
	}
	db.mu.RUnlock()

	for _, s := range series {
		// 跳过降采样序列，由 RetentionEngine 管理更长的保留期
		if strings.HasSuffix(s.Name, ":1m") || strings.HasSuffix(s.Name, ":5m") {
			continue
		}
		s.Cleanup(cutoff)
	}
}

// GC 立即执行一次数据清理（供管理员 API 强制触发）
func (db *TSDB) GC() {
	db.cleanup()
}

// TotalPoints 返回所有序列的数据点总数（用于监控和 Admin API）
func (db *TSDB) TotalPoints() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	total := 0
	for _, s := range db.series {
		total += s.Len()
	}
	return total
}

// DeleteSeries 删除所有与指定名称匹配的序列；返回删除数量（供管理员清理使用）
func (db *TSDB) DeleteSeries(name string) int {
	db.mu.Lock()
	defer db.mu.Unlock()
	count := 0
	for fp, s := range db.series {
		if s.Name == name {
			delete(db.series, fp)
			count++
		}
	}
	return count
}

// Snapshot 将当前数据立即刷盘（若已启用持久化存储）；纯内存模式返回 false
func (db *TSDB) Snapshot() bool {
	if db.storage == nil {
		return false
	}
	db.storage.Flush()
	return true
}

// SeriesCount 返回当前序列总数（用于测试和监控）
func (db *TSDB) SeriesCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.series)
}
