// Package tsdb 单元测试：涵盖写入查询、自动清理、并发安全和指标列举
package tsdb

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// helper：向 db 写入一个指定时间戳的数据点
func writePoint(db *TSDB, name string, labels map[string]string, value float64, ts int64) {
	db.Write([]model.MetricPoint{
		{Name: name, Labels: labels, Value: value, Timestamp: ts},
	})
}

// TestWriteAndQueryRange 测试写入后能按时间范围查询到正确数据
func TestWriteAndQueryRange(t *testing.T) {
	db := New()
	defer db.Stop()

	labels := map[string]string{"host": "test-node"}
	now := time.Now().UnixMilli()

	// 写入 3 个间隔 1 秒的数据点
	for i := 0; i < 3; i++ {
		writePoint(db, "cpu_usage_percent", labels, float64(10+i*10), now+int64(i*1000))
	}

	// 查询全部范围
	pts := db.QueryRange("cpu_usage_percent", labels, now-1, now+10000)
	if len(pts) != 3 {
		t.Fatalf("期望 3 个数据点，实际 %d", len(pts))
	}
	if pts[0].Value != 10 || pts[1].Value != 20 || pts[2].Value != 30 {
		t.Errorf("数据点值不符: %v", pts)
	}
}

// TestQueryRangeSubset 测试只返回时间范围内的数据点
func TestQueryRangeSubset(t *testing.T) {
	db := New()
	defer db.Stop()

	labels := map[string]string{"host": "node1"}
	base := time.Now().UnixMilli()

	for i := 0; i < 5; i++ {
		writePoint(db, "mem", labels, float64(i), base+int64(i*1000))
	}

	// 仅查询中间 3 个
	pts := db.QueryRange("mem", labels, base+1000, base+3000)
	if len(pts) != 3 {
		t.Fatalf("期望 3 个数据点，实际 %d", len(pts))
	}
}

// TestAutoCleanup 验证 Cleanup 能删除超出保留期限的旧数据点
func TestAutoCleanup(t *testing.T) {
	db := New()
	defer db.Stop()

	labels := map[string]string{"host": "old-node"}

	// 写入两个超过 1 小时的旧数据点
	twoHoursAgo := time.Now().Add(-2 * time.Hour).UnixMilli()
	writePoint(db, "old_metric", labels, 1.0, twoHoursAgo)
	writePoint(db, "old_metric", labels, 2.0, twoHoursAgo+1000)

	// 写入一个新的数据点
	writePoint(db, "old_metric", labels, 3.0, time.Now().UnixMilli())

	// 手动触发清理
	db.cleanup()

	// 旧数据应被删除，只剩 1 个
	pts := db.QueryRange("old_metric", labels,
		time.Now().Add(-3*time.Hour).UnixMilli(),
		time.Now().Add(time.Minute).UnixMilli(),
	)
	if len(pts) != 1 {
		t.Fatalf("清理后期望 1 个数据点，实际 %d", len(pts))
	}
	if pts[0].Value != 3.0 {
		t.Errorf("保留的应为新数据点 3.0，实际 %.1f", pts[0].Value)
	}
}

// TestConcurrentWrite 验证并发写入时数据库不发生竞争条件
func TestConcurrentWrite(t *testing.T) {
	db := New()
	defer db.Stop()

	const goroutines = 20
	const pointsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			labels := map[string]string{"host": fmt.Sprintf("node%d", g)}
			for i := 0; i < pointsPerGoroutine; i++ {
				db.Write([]model.MetricPoint{
					{
						Name:      "concurrent_metric",
						Labels:    labels,
						Value:     float64(i),
						Timestamp: time.Now().UnixMilli() + int64(i),
					},
				})
			}
		}()
	}

	wg.Wait()

	// 每个 goroutine 有独立标签，应创建 goroutines 个序列
	if db.SeriesCount() != goroutines {
		t.Errorf("期望 %d 个序列，实际 %d", goroutines, db.SeriesCount())
	}
}

// TestListMetrics 验证 ListMetrics 返回所有唯一指标名称
func TestListMetrics(t *testing.T) {
	db := New()
	defer db.Stop()

	names := []string{"cpu", "memory", "disk", "network"}
	for _, name := range names {
		writePoint(db, name, map[string]string{"host": "n1"}, 1.0, time.Now().UnixMilli())
	}
	// 写入重复指标名（不同标签），不应产生重复项
	writePoint(db, "cpu", map[string]string{"host": "n2"}, 2.0, time.Now().UnixMilli())

	metrics := db.ListMetrics()

	// 构建 set 检查
	seen := make(map[string]bool)
	for _, m := range metrics {
		if seen[m] {
			t.Errorf("指标 %s 重复出现", m)
		}
		seen[m] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Errorf("指标 %s 未出现在 ListMetrics 结果中", name)
		}
	}
}

// TestQueryNonExistentMetric 查询不存在的指标应返回 nil
func TestQueryNonExistentMetric(t *testing.T) {
	db := New()
	defer db.Stop()

	pts := db.QueryRange("does_not_exist", map[string]string{}, 0, time.Now().UnixMilli())
	if pts != nil {
		t.Errorf("不存在的指标应返回 nil，实际: %v", pts)
	}
}
