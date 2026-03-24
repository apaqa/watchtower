// Package tsdb — 降采样模块：将高分辨率原始数据聚合为低分辨率数据，
// 以支持更长时间跨度的查询而不占用大量内存。
//
// 层级说明：
//   - 原始数据（5 秒采样）：保留最近 1 小时
//   - cpu_usage_percent:1m（1 分钟平均）：保留最近 24 小时
//   - cpu_usage_percent:5m（5 分钟平均）：保留最近 7 天
package tsdb

import (
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

const (
	// downsampleInterval 降采样运行间隔（每 5 分钟执行一次）
	downsampleInterval = 5 * time.Minute

	// bucket1mMs 1 分钟桶宽度（毫秒）
	bucket1mMs = int64(60 * 1000)
	// bucket5mMs 5 分钟桶宽度（毫秒）
	bucket5mMs = int64(5 * 60 * 1000)

	// Suffix1m 降采样序列名称后缀：1 分钟聚合
	Suffix1m = ":1m"
	// Suffix5m 降采样序列名称后缀：5 分钟聚合
	Suffix5m = ":5m"
)

// Downsampler 定期读取原始指标序列，计算时间桶平均值并写回 TSDB
type Downsampler struct {
	db     *TSDB
	stopCh chan struct{}
}

// NewDownsampler 创建降采样器实例
func NewDownsampler(db *TSDB) *Downsampler {
	return &Downsampler{db: db, stopCh: make(chan struct{})}
}

// Start 在后台启动降采样循环
func (ds *Downsampler) Start() {
	go ds.loop()
}

// Stop 停止降采样循环
func (ds *Downsampler) Stop() {
	close(ds.stopCh)
}

// loop 每 downsampleInterval 执行一次降采样
func (ds *Downsampler) loop() {
	ticker := time.NewTicker(downsampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ds.Run()
		case <-ds.stopCh:
			return
		}
	}
}

// Run 立即对所有原始指标执行一轮降采样（也可由外部代码直接调用，用于测试）
func (ds *Downsampler) Run() {
	metrics := ds.db.ListMetrics()
	for _, name := range metrics {
		// 已是降采样序列，跳过
		if strings.HasSuffix(name, Suffix1m) || strings.HasSuffix(name, Suffix5m) {
			continue
		}
		series := ds.db.GetSeries(name)
		if len(series) == 0 {
			continue
		}
		// 对每个原始序列分别计算两种分辨率的聚合
		ds.downsampleSeries(name, series, bucket1mMs, Suffix1m)
		ds.downsampleSeries(name, series, bucket5mMs, Suffix5m)
	}
}

// downsampleSeries 计算指定桶宽度的时间平均值，并将尚未写入的新桶追加到降采样序列
func (ds *Downsampler) downsampleSeries(name string, series []*Series, bucketMs int64, suffix string) {
	downName := name + suffix

	// 找到该降采样序列中已有的最新桶时间戳，避免重复写入
	var lastBucketMs int64
	for _, es := range ds.db.GetSeries(downName) {
		pts := es.Latest(1)
		if len(pts) > 0 && pts[0].Timestamp > lastBucketMs {
			lastBucketMs = pts[0].Timestamp
		}
	}

	now := time.Now()
	// 当前未完成桶（不应写入，因为该桶的数据尚未收集完毕）
	currentBucketMs := (now.UnixMilli() / bucketMs) * bucketMs
	// 原始数据最远查询到 retentionDuration 之前
	start := now.Add(-retentionDuration).UnixMilli()
	end := now.UnixMilli()

	for _, s := range series {
		rawPoints := s.QueryRange(start, end)
		if len(rawPoints) == 0 {
			continue
		}

		// 将原始数据点分配到对应的时间桶
		buckets := make(map[int64][]float64)
		for _, p := range rawPoints {
			bucket := (p.Timestamp / bucketMs) * bucketMs
			// 仅处理新桶（> lastBucketMs）且已完成的桶（< currentBucketMs）
			if bucket > lastBucketMs && bucket < currentBucketMs {
				buckets[bucket] = append(buckets[bucket], p.Value)
			}
		}

		// 计算每桶平均值并写入降采样序列
		for bucket, vals := range buckets {
			if len(vals) == 0 {
				continue
			}
			avg := bucketAverage(vals)
			ds.db.Write([]model.MetricPoint{{
				Name:      downName,
				Labels:    s.Labels,
				Value:     avg,
				Timestamp: bucket,
			}})
		}
	}
}

// bucketAverage 计算浮点数切片的算术平均值
func bucketAverage(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}
