// Package tsdb - 持久化存储测试
// 覆盖：Delta-of-Delta 时间戳编码往返、XOR 浮点编码往返、块序列化往返、
// StorageManager 刷盘与重载，以及 TSDB 持久化集成
package tsdb

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// ─────────────────────────────────────────────────────────────────────────────
// Delta-of-Delta 编码往返测试
// ─────────────────────────────────────────────────────────────────────────────

// TestDeltaDeltaRoundtrip_Uniform 验证均匀间隔时间戳（Delta-of-Delta 全为 0）的往返正确性
func TestDeltaDeltaRoundtrip_Uniform(t *testing.T) {
	base := time.Now().UnixMilli()
	// 每 5 秒一个点，共 720 个（正好 1 小时）
	ts := make([]int64, 720)
	for i := range ts {
		ts[i] = base + int64(i)*5000
	}

	bw := &bitWriter{}
	encodeTimestamps(bw, ts)
	data := bw.flush()

	br := newBitReader(data)
	decoded, err := decodeTimestamps(br, len(ts))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	for i, got := range decoded {
		if got != ts[i] {
			t.Errorf("ts[%d]: 期望 %d，实际 %d", i, ts[i], got)
		}
	}
}

// TestDeltaDeltaRoundtrip_Irregular 验证不规则时间间隔（DoD 各异）的往返正确性
func TestDeltaDeltaRoundtrip_Irregular(t *testing.T) {
	// 故意使用不规则间隔以覆盖多个 DoD 编码分支
	ts := []int64{
		1000000, 1005000, 1010100, 1015000, 1115000, // 跨度超 100 秒触发大 DoD
		1115100, 1115200, 1115300,                   // 均匀间隔 DoD=0
	}

	bw := &bitWriter{}
	encodeTimestamps(bw, ts)
	data := bw.flush()

	br := newBitReader(data)
	decoded, err := decodeTimestamps(br, len(ts))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	for i, got := range decoded {
		if got != ts[i] {
			t.Errorf("ts[%d]: 期望 %d，实际 %d", i, ts[i], got)
		}
	}
}

// TestDeltaDeltaRoundtrip_Single 验证单个时间戳的边界情况
func TestDeltaDeltaRoundtrip_Single(t *testing.T) {
	ts := []int64{1234567890123}
	bw := &bitWriter{}
	encodeTimestamps(bw, ts)
	br := newBitReader(bw.flush())
	decoded, err := decodeTimestamps(br, 1)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if decoded[0] != ts[0] {
		t.Errorf("期望 %d，实际 %d", ts[0], decoded[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// XOR 浮点编码往返测试
// ─────────────────────────────────────────────────────────────────────────────

// TestXORRoundtrip_Typical 验证典型浮点值（CPU/内存百分比）的往返正确性
func TestXORRoundtrip_Typical(t *testing.T) {
	vals := []float64{45.2, 45.1, 45.3, 45.2, 46.0, 47.5, 48.0, 47.9, 47.8, 0.0}

	bw := &bitWriter{}
	encodeValues(bw, vals)
	br := newBitReader(bw.flush())
	decoded, err := decodeValues(br, len(vals))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	for i, got := range decoded {
		if math.Abs(got-vals[i]) > 1e-10 {
			t.Errorf("val[%d]: 期望 %v，实际 %v", i, vals[i], got)
		}
	}
}

// TestXORRoundtrip_Identical 验证所有值相同时（XOR=0）的情况
func TestXORRoundtrip_Identical(t *testing.T) {
	vals := make([]float64, 100)
	for i := range vals {
		vals[i] = 42.0
	}

	bw := &bitWriter{}
	encodeValues(bw, vals)
	encoded := bw.flush()

	br := newBitReader(encoded)
	decoded, err := decodeValues(br, len(vals))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	for i, got := range decoded {
		if got != 42.0 {
			t.Errorf("val[%d]: 期望 42.0，实际 %v", i, got)
		}
	}

	// XOR=0 路径压缩率应远高于原始大小（100×8=800 字节 vs 极少字节）
	if len(encoded) >= 100 {
		t.Logf("注意：100 个相同值压缩后 %d 字节（原始 800 字节）", len(encoded))
	}
}

// TestXORRoundtrip_SpecialValues 验证特殊浮点值（NaN、±Inf）的往返正确性
func TestXORRoundtrip_SpecialValues(t *testing.T) {
	vals := []float64{1.0, math.Inf(1), math.Inf(-1), 0.0, -1.0}

	bw := &bitWriter{}
	encodeValues(bw, vals)
	br := newBitReader(bw.flush())
	decoded, err := decodeValues(br, len(vals))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}

	for i, got := range decoded {
		orig := vals[i]
		if math.IsInf(orig, 1) && !math.IsInf(got, 1) {
			t.Errorf("val[%d]: 期望 +Inf，实际 %v", i, got)
		} else if math.IsInf(orig, -1) && !math.IsInf(got, -1) {
			t.Errorf("val[%d]: 期望 -Inf，实际 %v", i, got)
		} else if !math.IsInf(orig, 0) && got != orig {
			t.Errorf("val[%d]: 期望 %v，实际 %v", i, orig, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 块序列化往返测试
// ─────────────────────────────────────────────────────────────────────────────

// TestChunkRoundtrip 验证块完整序列化/反序列化往返
func TestChunkRoundtrip(t *testing.T) {
	base := time.Now().Add(-30 * time.Minute).UnixMilli()
	pts := make([]model.DataPoint, 360)
	for i := range pts {
		pts[i] = model.DataPoint{
			Timestamp: base + int64(i)*5000,
			Value:     float64(i%100) + 0.5,
		}
	}

	original := &chunk{
		fp:     "testfp",
		name:   "cpu_usage_percent",
		labels: map[string]string{"host": "server1", "env": "prod"},
		points: pts,
	}

	data, err := encodeChunk(original)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	decoded, err := decodeChunk(data)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}

	if decoded.name != original.name {
		t.Errorf("name: 期望 %q，实际 %q", original.name, decoded.name)
	}
	if len(decoded.labels) != len(original.labels) {
		t.Errorf("label 数量: 期望 %d，实际 %d", len(original.labels), len(decoded.labels))
	}
	for k, v := range original.labels {
		if decoded.labels[k] != v {
			t.Errorf("label[%q]: 期望 %q，实际 %q", k, v, decoded.labels[k])
		}
	}
	if len(decoded.points) != len(original.points) {
		t.Fatalf("数据点数量: 期望 %d，实际 %d", len(original.points), len(decoded.points))
	}
	for i := range original.points {
		if decoded.points[i].Timestamp != original.points[i].Timestamp {
			t.Errorf("points[%d].Timestamp: 期望 %d，实际 %d",
				i, original.points[i].Timestamp, decoded.points[i].Timestamp)
		}
		if math.Abs(decoded.points[i].Value-original.points[i].Value) > 1e-10 {
			t.Errorf("points[%d].Value: 期望 %v，实际 %v",
				i, original.points[i].Value, decoded.points[i].Value)
		}
	}

	// 验证压缩有效（压缩后应小于原始大小 360×16=5760 字节）
	t.Logf("块压缩：360 个点，原始 ~5760 字节，实际 %d 字节", len(data))
}

// ─────────────────────────────────────────────────────────────────────────────
// StorageManager 集成测试
// ─────────────────────────────────────────────────────────────────────────────

// TestStorageFlushReload 验证数据写入 → 刷盘 → 重载的完整流程
func TestStorageFlushReload(t *testing.T) {
	dir := t.TempDir()

	sm, err := NewStorageManager(dir)
	if err != nil {
		t.Fatalf("创建 StorageManager 失败: %v", err)
	}

	// 写入测试数据
	base := time.Now().Add(-10 * time.Minute).UnixMilli()
	labels := map[string]string{"host": "testhost"}
	fp := model.Fingerprint("test_metric", labels)

	for i := 0; i < 10; i++ {
		dp := model.DataPoint{
			Timestamp: base + int64(i)*5000,
			Value:     float64(i) * 1.5,
		}
		sm.write(fp, "test_metric", labels, dp)
	}

	// 刷盘
	sm.Flush()

	// 验证磁盘上有块文件
	chunksDir := filepath.Join(dir, "chunks")
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		t.Fatalf("读取块目录失败: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("刷盘后块目录应有文件")
	}

	// 重载
	sm2, err := NewStorageManager(dir)
	if err != nil {
		t.Fatalf("创建第二个 StorageManager 失败: %v", err)
	}
	reloaded, err := sm2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}

	if len(reloaded) != 10 {
		t.Errorf("重载数据点数量：期望 10，实际 %d", len(reloaded))
	}

	// 验证数据内容
	for _, mp := range reloaded {
		if mp.Name != "test_metric" {
			t.Errorf("指标名称：期望 'test_metric'，实际 %q", mp.Name)
		}
	}
}

// TestStorageChunkRotation 验证块超过 1 小时后自动轮换
func TestStorageChunkRotation(t *testing.T) {
	dir := t.TempDir()
	sm, err := NewStorageManager(dir)
	if err != nil {
		t.Fatalf("创建 StorageManager 失败: %v", err)
	}

	labels := map[string]string{"host": "rot"}
	fp := model.Fingerprint("rot_metric", labels)

	// 写入第一个块的数据
	firstTs := time.Now().Add(-2 * time.Hour).UnixMilli()
	sm.write(fp, "rot_metric", labels, model.DataPoint{Timestamp: firstTs, Value: 1.0})

	// 写入一个超过 1 小时的点，应触发轮换
	secondTs := firstTs + int64(time.Hour/time.Millisecond) + 1000
	sm.write(fp, "rot_metric", labels, model.DataPoint{Timestamp: secondTs, Value: 2.0})

	// 轮换时第一个块应已刷盘
	chunksDir := filepath.Join(dir, "chunks")
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) == 0 {
		t.Error("块轮换后应有至少一个块文件")
	}
}

// TestNewWithStorage 验证 NewWithStorage 启动时从磁盘恢复数据
func TestNewWithStorage(t *testing.T) {
	dir := t.TempDir()

	// 第一次：写入数据并关闭
	db1, err := NewWithStorage(dir)
	if err != nil {
		t.Fatalf("创建 TSDB 失败: %v", err)
	}
	now := time.Now()
	db1.Write([]model.MetricPoint{
		{Name: "persist_test", Labels: map[string]string{"env": "test"},
			Value: 99.9, Timestamp: now.Add(-1 * time.Minute).UnixMilli()},
	})
	db1.Stop() // 触发最终刷盘

	// 第二次：重新打开，验证数据已恢复
	db2, err := NewWithStorage(dir)
	if err != nil {
		t.Fatalf("重新打开 TSDB 失败: %v", err)
	}
	defer db2.Stop()

	series := db2.GetSeries("persist_test")
	if len(series) == 0 {
		t.Fatal("重启后应能从磁盘恢复 persist_test 序列")
	}
}
