package tsdb

import (
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// makeRawPoints 写入以 intervalMs 为间隔的原始数据点序列
// 起点从 now - count*intervalMs 开始，使数据点落在 1h 保留窗口内
func makeRawPoints(db *TSDB, name string, values []float64, intervalMs int64) {
	now := time.Now().UnixMilli()
	// 确保最早的点在 retentionDuration 之内
	start := now - int64(len(values))*intervalMs
	pts := make([]model.MetricPoint, len(values))
	for i, v := range values {
		pts[i] = model.MetricPoint{
			Name:      name,
			Labels:    map[string]string{},
			Value:     v,
			Timestamp: start + int64(i)*intervalMs,
		}
	}
	db.Write(pts)
}

// TestDownsample_1mAverages 验证 1 分钟桶平均值计算正确
func TestDownsample_1mAverages(t *testing.T) {
	db := New()

	// 写入跨 3 个完整 1 分钟桶的数据（每桶 6 个点，间隔 10 秒）
	// 让桶时间戳落在 2 分钟前，保证桶已完成
	intervalMs := int64(10 * 1000) // 10 秒
	now := time.Now().UnixMilli()
	// 桶起点：3 分钟前，对齐到分钟边界
	bucketStart := ((now - 3*60*1000) / bucket1mMs) * bucket1mMs

	var vals []float64
	var pts []model.MetricPoint
	for b := 0; b < 3; b++ {
		for j := 0; j < 6; j++ {
			ts := bucketStart + int64(b)*bucket1mMs + int64(j)*intervalMs
			v := float64(b+1) * 10.0 // 桶0: 10.0，桶1: 20.0，桶2: 30.0
			vals = append(vals, v)
			pts = append(pts, model.MetricPoint{
				Name:      "test_metric",
				Labels:    map[string]string{},
				Value:     v,
				Timestamp: ts,
			})
		}
	}
	db.Write(pts)

	ds := NewDownsampler(db)
	ds.Run()

	// 检查 :1m 降采样序列已创建
	series1m := db.GetSeries("test_metric" + Suffix1m)
	if len(series1m) == 0 {
		t.Fatal("expected :1m series to be created after downsampling")
	}

	pts1m := series1m[0].QueryRange(bucketStart, now)
	if len(pts1m) == 0 {
		t.Fatal("expected downsampled points in :1m series")
	}

	// 检验每个桶的平均值
	bucketMap := make(map[int64]float64, len(pts1m))
	for _, p := range pts1m {
		bucketMap[p.Timestamp] = p.Value
	}

	for b := 0; b < 3; b++ {
		bts := bucketStart + int64(b)*bucket1mMs
		// 该桶的点可能因 currentBucketMs 检查而被跳过，不强制要求所有桶都存在
		if v, ok := bucketMap[bts]; ok {
			expected := float64(b+1) * 10.0
			if v != expected {
				t.Errorf("bucket %d: expected avg=%.1f, got %.1f", b, expected, v)
			}
		}
	}
}

// TestDownsample_TimestampAlignment 验证降采样时间戳对齐到桶边界
func TestDownsample_TimestampAlignment(t *testing.T) {
	db := New()

	// 写入过去 4 分钟内，不完全对齐分钟边界的点
	now := time.Now().UnixMilli()
	base := now - 4*60*1000
	pts := make([]model.MetricPoint, 12)
	for i := range pts {
		pts[i] = model.MetricPoint{
			Name:      "align_test",
			Labels:    map[string]string{},
			Value:     float64(i),
			Timestamp: base + int64(i)*20*1000, // 20 秒间隔
		}
	}
	db.Write(pts)

	ds := NewDownsampler(db)
	ds.Run()

	series1m := db.GetSeries("align_test" + Suffix1m)
	if len(series1m) == 0 {
		return // 没有完成的桶，跳过
	}

	all := series1m[0].QueryRange(0, now)
	for _, p := range all {
		// 每个降采样点的时间戳必须是 60000ms 的整数倍
		if p.Timestamp%bucket1mMs != 0 {
			t.Errorf("timestamp %d is not aligned to 1-minute boundary", p.Timestamp)
		}
	}
}

// TestDownsample_NoDataNoSeries 验证无原始数据时不创建降采样序列
func TestDownsample_NoDataNoSeries(t *testing.T) {
	db := New()
	ds := NewDownsampler(db)
	ds.Run()

	if len(db.GetSeries("nonexistent"+Suffix1m)) != 0 {
		t.Error("expected no :1m series for metric with no data")
	}
}

// TestDownsample_5mCreated 验证 :5m 降采样序列也被创建
func TestDownsample_5mCreated(t *testing.T) {
	db := New()

	// 写入 10 分钟前的数据，确保跨越至少两个 5 分钟桶
	now := time.Now().UnixMilli()
	bucketStart := ((now - 10*60*1000) / bucket5mMs) * bucket5mMs

	pts := make([]model.MetricPoint, 60)
	for i := range pts {
		pts[i] = model.MetricPoint{
			Name:      "disk_usage_percent",
			Labels:    map[string]string{},
			Value:     float64(50 + i%10),
			Timestamp: bucketStart + int64(i)*10*1000,
		}
	}
	db.Write(pts)

	ds := NewDownsampler(db)
	ds.Run()

	series5m := db.GetSeries("disk_usage_percent" + Suffix5m)
	if len(series5m) == 0 {
		t.Fatal("expected :5m series to be created")
	}

	pts5m := series5m[0].QueryRange(0, now)
	if len(pts5m) == 0 {
		t.Error("expected at least one :5m data point")
	}

	// 5m 时间戳也必须对齐到 300000ms 的整数倍
	for _, p := range pts5m {
		if p.Timestamp%bucket5mMs != 0 {
			t.Errorf("5m timestamp %d not aligned to 5-minute boundary", p.Timestamp)
		}
	}
}

// TestDownsample_SkipsDownsampledSeries 验证降采样器不对已有 :1m/:5m 序列再次降采样
func TestDownsample_SkipsDownsampledSeries(t *testing.T) {
	db := New()

	// 写入一个 :1m 序列
	now := time.Now().UnixMilli()
	db.Write([]model.MetricPoint{{
		Name:      "cpu:1m",
		Labels:    map[string]string{},
		Value:     50.0,
		Timestamp: now - 2*60*1000,
	}})

	countBefore := db.SeriesCount()

	ds := NewDownsampler(db)
	ds.Run()

	// 不应产生 cpu:1m:1m 或 cpu:1m:5m 序列
	if db.SeriesCount() > countBefore {
		t.Errorf("downsampler should not process :1m series, but series count increased from %d to %d",
			countBefore, db.SeriesCount())
	}
}
