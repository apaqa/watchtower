// Package wql - 求值器测试（含集成测试：写入指标 → WQL 查询 → 验证结果）
package wql

import (
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// newTestDB 创建一个预填充了测试数据的 TSDB 实例
func newTestDB(t *testing.T) *tsdb.TSDB {
	t.Helper()
	db := tsdb.New()
	now := time.Now()

	// 写入 cpu_usage_percent（host=A）：值为 10, 20, 30, 40, 50 → avg=30
	for i := 0; i < 5; i++ {
		db.Write([]model.MetricPoint{
			{
				Name:      "cpu_usage_percent",
				Labels:    map[string]string{"host": "A"},
				Value:     float64((i + 1) * 10),
				Timestamp: now.Add(time.Duration(i-5) * time.Minute).UnixMilli(),
			},
		})
	}

	// 写入 cpu_usage_percent（host=B）：固定值 90
	db.Write([]model.MetricPoint{
		{
			Name:      "cpu_usage_percent",
			Labels:    map[string]string{"host": "B"},
			Value:     90,
			Timestamp: now.Add(-1 * time.Minute).UnixMilli(),
		},
	})

	// 写入 memory_usage_percent：60
	db.Write([]model.MetricPoint{
		{
			Name:      "memory_usage_percent",
			Labels:    map[string]string{"host": "A"},
			Value:     60,
			Timestamp: now.Add(-1 * time.Minute).UnixMilli(),
		},
	})
	return db
}

// TestEvalAvg 验证 avg() 函数对单序列的求值
func TestEvalAvg(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	node, err := Parse(`avg(cpu_usage_percent{host="A"}[10m])`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	result, err := ev.Eval(node)
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("期望标量结果，实际 %s", result.Type)
	}
	// 5 个值 10+20+30+40+50 / 5 = 30
	if got := *result.Scalar; got < 29.9 || got > 30.1 {
		t.Errorf("avg 期望 30，实际 %.2f", got)
	}
}

// TestEvalMax 验证 max() 函数
func TestEvalMax(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	result, err := ev.Eval(mustParse(t, `max(cpu_usage_percent{host="A"}[10m])`))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if *result.Scalar != 50 {
		t.Errorf("max 期望 50，实际 %.2f", *result.Scalar)
	}
}

// TestEvalMin 验证 min() 函数
func TestEvalMin(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	result, err := ev.Eval(mustParse(t, `min(cpu_usage_percent{host="A"}[10m])`))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if *result.Scalar != 10 {
		t.Errorf("min 期望 10，实际 %.2f", *result.Scalar)
	}
}

// TestEvalSum 验证 sum() 函数（多序列合计）
func TestEvalSum(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	// cpu_usage_percent（所有序列）：host=A 返回向量各序列单值，host=B=90
	// 单序列求 sum
	result, err := ev.Eval(mustParse(t, `sum(cpu_usage_percent{host="A"}[10m])`))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	// 10+20+30+40+50 = 150
	if got := *result.Scalar; got < 149.9 || got > 150.1 {
		t.Errorf("sum 期望 150，实际 %.2f", got)
	}
}

// TestEvalComparison 验证比较运算符
func TestEvalComparison(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	// avg(cpu_usage_percent{host="A"}[10m]) = 30 > 20 → true
	result, err := ev.Eval(mustParse(t, `avg(cpu_usage_percent{host="A"}[10m]) > 20`))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if result.Type != ResultBool {
		t.Fatalf("期望布尔结果，实际 %s", result.Type)
	}
	if !*result.Bool {
		t.Error("期望 true，实际 false")
	}

	// 30 > 50 → false
	result, err = ev.Eval(mustParse(t, `avg(cpu_usage_percent{host="A"}[10m]) > 50`))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if *result.Bool {
		t.Error("期望 false，实际 true")
	}
}

// TestEvalGroupBy 验证 by (label) 分组聚合
func TestEvalGroupBy(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	result, err := ev.Eval(mustParse(t, "avg(cpu_usage_percent[10m]) by (host)"))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if result.Type != ResultVector {
		t.Fatalf("期望向量结果，实际 %s", result.Type)
	}
	if len(result.Vector) != 2 {
		t.Fatalf("期望 2 个分组（host=A, host=B），实际 %d 个", len(result.Vector))
	}

	groups := make(map[string]float64)
	for _, s := range result.Vector {
		groups[s.Labels["host"]] = s.Value
	}
	// host=A: avg(10,20,30,40,50)=30
	if v := groups["A"]; v < 29.9 || v > 30.1 {
		t.Errorf("host=A avg 期望 30，实际 %.2f", v)
	}
	// host=B: avg(90)=90
	if v := groups["B"]; v < 89.9 || v > 90.1 {
		t.Errorf("host=B avg 期望 90，实际 %.2f", v)
	}
}

// TestEvalArithmetic 验证标量间的算术运算
func TestEvalArithmetic(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluatorAt(db, time.Now())

	// memory_usage_percent{host="A"} = 60，* 2 = 120
	result, err := ev.Eval(mustParse(t, `last(memory_usage_percent{host="A"}[10m]) * 2`))
	if err != nil {
		t.Fatalf("求值失败: %v", err)
	}
	if result.Type != ResultScalar {
		t.Fatalf("期望标量，实际 %s", result.Type)
	}
	if got := *result.Scalar; got < 119.9 || got > 120.1 {
		t.Errorf("期望 120，实际 %.2f", got)
	}
}

// TestEvalIntegration 集成测试：写入指标 → WQL 查询 → 验证结果
// 模拟真实场景：通过 TSDB.Write 写入数据，再通过 WQL 求值器查询
func TestEvalIntegration(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()

	// 模拟写入 5 分钟内的 CPU 数据（每分钟一个点）
	baseTime := time.Now().Add(-5 * time.Minute)
	for i := 0; i < 5; i++ {
		db.Write([]model.MetricPoint{
			{
				Name:      "cpu_usage_percent",
				Labels:    map[string]string{"host": "server1", "env": "prod"},
				Value:     float64(20 + i*5), // 20, 25, 30, 35, 40
				Timestamp: baseTime.Add(time.Duration(i) * time.Minute).UnixMilli(),
			},
		})
	}

	// 使用 EvalQuery 一步完成解析和求值
	result, err := EvalQuery(db, `avg(cpu_usage_percent{host="server1"}[10m])`)
	if err != nil {
		t.Fatalf("EvalQuery 失败: %v", err)
	}

	// avg(20,25,30,35,40) = 30
	if result.Type != ResultScalar {
		t.Fatalf("期望标量，实际 %s", result.Type)
	}
	if got := *result.Scalar; got < 29.9 || got > 30.1 {
		t.Errorf("集成测试 avg 期望 30，实际 %.2f", got)
	}

	// 验证比较查询：avg > 25 → true
	boolResult, err := EvalQuery(db, `avg(cpu_usage_percent{host="server1"}[10m]) > 25`)
	if err != nil {
		t.Fatalf("比较查询失败: %v", err)
	}
	if !*boolResult.Bool {
		t.Error("比较结果期望 true")
	}
}

// mustParse 是测试辅助函数，解析失败则立即 Fatal
func mustParse(t *testing.T, q string) Node {
	t.Helper()
	node, err := Parse(q)
	if err != nil {
		t.Fatalf("mustParse(%q) 失败: %v", q, err)
	}
	return node
}
