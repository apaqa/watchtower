package wql

import (
	"math"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

// writeSingleMetric 向 TSDB 写入一个单点指标
func writeSingleMetric(db *tsdb.TSDB, name string, value float64, labels map[string]string) {
	db.Write([]model.MetricPoint{{
		Name:      name,
		Labels:    labels,
		Value:     value,
		Timestamp: time.Now().UnixMilli(),
	}})
}

// writeTimeSeries 写入一组时间序列数据点（每点间隔 intervalSec 秒）
func writeTimeSeries(db *tsdb.TSDB, name string, values []float64, labels map[string]string, intervalSec int) {
	now := time.Now()
	pts := make([]model.MetricPoint, len(values))
	for i, v := range values {
		pts[i] = model.MetricPoint{
			Name:      name,
			Labels:    labels,
			Value:     v,
			Timestamp: now.Add(time.Duration(i-len(values)+1) * time.Duration(intervalSec) * time.Second).UnixMilli(),
		}
	}
	db.Write(pts)
}

// almostEqual 比较两个浮点数是否近似相等（允许 1e-9 误差）
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// ── 标量运算测试 ───────────────────────────────────────────────────────────────

func TestMath_ScalarDivision(t *testing.T) {
	// cpu_usage_percent / 100  →  scalar
	db := tsdb.New()
	writeSingleMetric(db, "cpu_usage_percent", 75.0, map[string]string{})

	result, err := EvalQuery(db, "cpu_usage_percent / 100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("expected scalar, got %s", result.Type)
	}
	if !almostEqual(*result.Scalar, 0.75) {
		t.Errorf("expected 0.75, got %f", *result.Scalar)
	}
}

func TestMath_ScalarMultiplication(t *testing.T) {
	// cpu_usage_percent * 2  →  scalar
	db := tsdb.New()
	writeSingleMetric(db, "cpu_usage_percent", 50.0, map[string]string{})

	result, err := EvalQuery(db, "cpu_usage_percent * 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("expected scalar, got %s", result.Type)
	}
	if !almostEqual(*result.Scalar, 100.0) {
		t.Errorf("expected 100.0, got %f", *result.Scalar)
	}
}

func TestMath_MetricDivision_BothScalar(t *testing.T) {
	// memory_used_bytes / memory_total_bytes * 100  →  scalar
	// 两个单序列指标均视为标量，直接标量÷标量
	db := tsdb.New()
	writeSingleMetric(db, "memory_used_bytes", 768.0, map[string]string{})
	writeSingleMetric(db, "memory_total_bytes", 1024.0, map[string]string{})

	result, err := EvalQuery(db, "memory_used_bytes / memory_total_bytes * 100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("expected scalar, got %s", result.Type)
	}
	// 768 / 1024 * 100 = 75.0
	if !almostEqual(*result.Scalar, 75.0) {
		t.Errorf("expected 75.0, got %f", *result.Scalar)
	}
}

// ── 运算符优先级测试 ───────────────────────────────────────────────────────────

func TestMath_OperatorPrecedence(t *testing.T) {
	// 纯数值表达式：2 + 3 * 4 应按 * 优先得到 14，而非 20
	result, err := EvalQuery(tsdb.New(), "2 + 3 * 4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("expected scalar, got %s", result.Type)
	}
	if !almostEqual(*result.Scalar, 14.0) {
		t.Errorf("operator precedence: expected 2+(3*4)=14, got %f", *result.Scalar)
	}
}

func TestMath_OperatorPrecedence_SubtractMultiply(t *testing.T) {
	// 10 - 2 * 3 = 10 - 6 = 4（不是 (10-2)*3 = 24）
	result, err := EvalQuery(tsdb.New(), "10 - 2 * 3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !almostEqual(*result.Scalar, 4.0) {
		t.Errorf("expected 4.0, got %f", *result.Scalar)
	}
}

// ── 括号分组测试 ───────────────────────────────────────────────────────────────

func TestMath_Parentheses(t *testing.T) {
	// (cpu + memory) / 2：括号改变优先级
	db := tsdb.New()
	writeSingleMetric(db, "cpu", 60.0, map[string]string{})
	writeSingleMetric(db, "memory", 40.0, map[string]string{})

	result, err := EvalQuery(db, "(cpu + memory) / 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("expected scalar, got %s", result.Type)
	}
	// (60 + 40) / 2 = 50
	if !almostEqual(*result.Scalar, 50.0) {
		t.Errorf("expected 50.0, got %f", *result.Scalar)
	}
}

func TestMath_Parentheses_NumericOnly(t *testing.T) {
	// (1 + 2) * (3 + 4) = 3 * 7 = 21
	result, err := EvalQuery(tsdb.New(), "(1 + 2) * (3 + 4)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !almostEqual(*result.Scalar, 21.0) {
		t.Errorf("expected 21.0, got %f", *result.Scalar)
	}
}

// ── rate() 与乘法组合测试 ──────────────────────────────────────────────────────

func TestMath_RateWithMultiplication(t *testing.T) {
	// rate(counter[5m]) * 60：每秒速率转换为每分钟速率
	db := tsdb.New()
	// 计数器在 5 分钟内从 1000 增加到 1500，增量 = 500
	// rate = 500 / 300s ≈ 1.667 req/s → * 60 ≈ 100 req/min
	writeTimeSeries(db, "http_requests_total", []float64{1000, 1100, 1200, 1300, 1400, 1500},
		map[string]string{}, 60) // 每 60 秒一个点（共 5 分钟）

	result, err := EvalQuery(db, "rate(http_requests_total[5m]) * 60")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("expected scalar, got %s", result.Type)
	}
	// rate = 500 / 300 ≈ 1.6667，* 60 ≈ 100
	if math.Abs(*result.Scalar-100.0) > 1.0 {
		t.Errorf("expected ~100.0 req/min, got %f", *result.Scalar)
	}
}

// ── 向量×向量运算测试 ──────────────────────────────────────────────────────────

func TestMath_VectorVectorDivision(t *testing.T) {
	// used_bytes / total_bytes（两个多序列指标，按标签匹配后相除）
	db := tsdb.New()
	now := time.Now().UnixMilli()

	db.Write([]model.MetricPoint{
		{Name: "used_bytes", Labels: map[string]string{"host": "A"}, Value: 80.0, Timestamp: now},
		{Name: "used_bytes", Labels: map[string]string{"host": "B"}, Value: 60.0, Timestamp: now},
	})
	db.Write([]model.MetricPoint{
		{Name: "total_bytes", Labels: map[string]string{"host": "A"}, Value: 100.0, Timestamp: now},
		{Name: "total_bytes", Labels: map[string]string{"host": "B"}, Value: 100.0, Timestamp: now},
	})

	result, err := EvalQuery(db, "used_bytes / total_bytes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultVector {
		t.Fatalf("expected vector, got %s", result.Type)
	}
	if len(result.Vector) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(result.Vector))
	}
	// 验证每个标签组的结果
	for _, s := range result.Vector {
		switch s.Labels["host"] {
		case "A":
			if !almostEqual(s.Value, 0.8) {
				t.Errorf("host=A: expected 0.8, got %f", s.Value)
			}
		case "B":
			if !almostEqual(s.Value, 0.6) {
				t.Errorf("host=B: expected 0.6, got %f", s.Value)
			}
		default:
			t.Errorf("unexpected label host=%s", s.Labels["host"])
		}
	}
}

func TestMath_VectorBroadcast_RightSingleElement(t *testing.T) {
	// 右侧只有单一序列时广播到左侧所有元素
	// （left=vector, right=single-series scalar-like）
	db := tsdb.New()
	now := time.Now().UnixMilli()

	db.Write([]model.MetricPoint{
		{Name: "request_bytes", Labels: map[string]string{"svc": "api"}, Value: 1024.0, Timestamp: now},
		{Name: "request_bytes", Labels: map[string]string{"svc": "grpc"}, Value: 512.0, Timestamp: now},
	})
	db.Write([]model.MetricPoint{
		{Name: "bytes_per_kb", Labels: map[string]string{}, Value: 1024.0, Timestamp: now},
	})

	result, err := EvalQuery(db, "request_bytes / bytes_per_kb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != ResultVector {
		t.Fatalf("expected vector, got %s", result.Type)
	}
	if len(result.Vector) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(result.Vector))
	}
	for _, s := range result.Vector {
		switch s.Labels["svc"] {
		case "api":
			if !almostEqual(s.Value, 1.0) {
				t.Errorf("svc=api: expected 1.0 KB, got %f", s.Value)
			}
		case "grpc":
			if !almostEqual(s.Value, 0.5) {
				t.Errorf("svc=grpc: expected 0.5 KB, got %f", s.Value)
			}
		}
	}
}

// ── canonicalLabelKey 测试 ────────────────────────────────────────────────────

func TestCanonicalLabelKey_Deterministic(t *testing.T) {
	// 相同标签的不同插入顺序应产生相同的 key
	labels1 := map[string]string{"host": "A", "region": "us-east"}
	labels2 := map[string]string{"region": "us-east", "host": "A"}

	if canonicalLabelKey(labels1) != canonicalLabelKey(labels2) {
		t.Error("canonicalLabelKey should be deterministic regardless of map iteration order")
	}
}

func TestCanonicalLabelKey_EmptyLabels(t *testing.T) {
	if canonicalLabelKey(map[string]string{}) != "" {
		t.Error("empty labels should produce empty key")
	}
}
