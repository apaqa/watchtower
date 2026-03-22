// Package alert - 告警引擎测试：状态机转换、评估触发和集成测试
package alert

import (
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// newTestDB 创建一个预填充了测试数据的内存 TSDB
func newTestDB(t *testing.T) *tsdb.TSDB {
	t.Helper()
	db := tsdb.New()
	now := time.Now()

	// cpu_usage_percent = 90（远高于典型告警阈值 80）
	db.Write([]model.MetricPoint{
		{
			Name:      "cpu_usage_percent",
			Labels:    map[string]string{"host": "test"},
			Value:     90,
			Timestamp: now.Add(-1 * time.Minute).UnixMilli(),
		},
		{
			Name:      "cpu_usage_percent",
			Labels:    map[string]string{"host": "test"},
			Value:     92,
			Timestamp: now.UnixMilli(),
		},
	})
	return db
}

// newLowDB 创建一个值低于告警阈值的 TSDB
func newLowDB(t *testing.T) *tsdb.TSDB {
	t.Helper()
	db := tsdb.New()
	db.Write([]model.MetricPoint{
		{
			Name:      "cpu_usage_percent",
			Labels:    map[string]string{"host": "test"},
			Value:     30, // 低于 80
			Timestamp: time.Now().UnixMilli(),
		},
	})
	return db
}

// newHighCPURule 返回一个立即触发（duration=0）的高 CPU 告警规则
func newHighCPURule() AlertRule {
	return AlertRule{
		Name:       "high_cpu",
		Expression: `avg(cpu_usage_percent{host="test"}[5m])`,
		Operator:   ">",
		Threshold:  80,
		Duration:   "0", // 立即触发
		Severity:   "critical",
	}
}

// TestEngineAddAndRemoveRule 验证规则的添加与删除
func TestEngineAddAndRemoveRule(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	eng := NewEngine(db)

	rule := newHighCPURule()
	if err := eng.AddRule(rule); err != nil {
		t.Fatalf("AddRule 失败: %v", err)
	}

	alerts := eng.GetAlerts()
	if len(alerts) != 1 {
		t.Fatalf("期望 1 条告警状态，实际 %d 条", len(alerts))
	}
	if alerts[0].Rule.Name != "high_cpu" {
		t.Errorf("规则名称错误: %q", alerts[0].Rule.Name)
	}
	if alerts[0].State != StateInactive {
		t.Errorf("初始状态应为 Inactive，实际 %q", alerts[0].State)
	}

	if err := eng.RemoveRule("high_cpu"); err != nil {
		t.Fatalf("RemoveRule 失败: %v", err)
	}
	if len(eng.GetAlerts()) != 0 {
		t.Error("删除规则后 GetAlerts 应返回空列表")
	}
}

// TestEngineAddRule_Validation 验证非法规则被拒绝
func TestEngineAddRule_Validation(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	eng := NewEngine(db)

	// 缺少名称
	err := eng.AddRule(AlertRule{Expression: "avg(cpu[5m])", Operator: ">", Threshold: 80, Severity: "info"})
	if err == nil {
		t.Error("期望空名称返回错误")
	}
}

// TestEngineStateMachine_Firing 验证：条件满足 → Inactive → Pending → Firing
func TestEngineStateMachine_Firing(t *testing.T) {
	db := newTestDB(t) // CPU=90, 高于阈值 80
	defer db.Stop()
	eng := NewEngine(db)

	rule := newHighCPURule() // duration=0 → 立即触发
	if err := eng.AddRule(rule); err != nil {
		t.Fatalf("AddRule 失败: %v", err)
	}

	// 第一次评估：Inactive → Pending（duration=0 应直接到 Firing）
	eng.Evaluate()

	alerts := eng.GetAlerts()
	if len(alerts) != 1 {
		t.Fatalf("期望 1 条告警，实际 %d 条", len(alerts))
	}
	// duration=0 时，Pending 立即转换到 Firing
	if alerts[0].State != StateFiring {
		t.Errorf("duration=0 时首次评估应直接到 Firing，实际 %q", alerts[0].State)
	}
	if alerts[0].FiredAt == nil {
		t.Error("FiredAt 应非空")
	}

	// GetActiveAlerts 应返回此告警
	active := eng.GetActiveAlerts()
	if len(active) != 1 {
		t.Errorf("GetActiveAlerts 期望 1 条，实际 %d 条", len(active))
	}
}

// TestEngineStateMachine_PendingDuration 验证：有 duration 时先进入 Pending
func TestEngineStateMachine_PendingDuration(t *testing.T) {
	db := newTestDB(t)
	defer db.Stop()
	eng := NewEngine(db)

	rule := AlertRule{
		Name:       "high_cpu_with_duration",
		Expression: `avg(cpu_usage_percent{host="test"}[5m])`,
		Operator:   ">",
		Threshold:  80,
		Duration:   "5m", // 需要持续 5 分钟
		Severity:   "warning",
	}
	if err := eng.AddRule(rule); err != nil {
		t.Fatalf("AddRule 失败: %v", err)
	}

	// 第一次评估：条件满足，进入 Pending
	eng.Evaluate()

	alerts := eng.GetAlerts()
	if len(alerts) == 0 {
		t.Fatal("期望有告警状态")
	}
	// 应为 Pending（5 分钟未到）
	if alerts[0].State != StatePending {
		t.Errorf("5m duration 下首次评估应为 Pending，实际 %q", alerts[0].State)
	}
	if alerts[0].PendingAt == nil {
		t.Error("PendingAt 应非空")
	}
	// 尚未到 Firing
	if len(eng.GetActiveAlerts()) != 0 {
		t.Error("duration 未到，不应有 Firing 告警")
	}
}

// TestEngineStateMachine_Resolved 验证：条件消除时从 Firing → Resolved → Inactive
func TestEngineStateMachine_Resolved(t *testing.T) {
	db := newTestDB(t) // 高 CPU
	defer db.Stop()
	eng := NewEngine(db)

	rule := newHighCPURule()
	if err := eng.AddRule(rule); err != nil {
		t.Fatalf("AddRule 失败: %v", err)
	}

	// 触发 Firing
	eng.Evaluate()

	// 写入低 CPU 数据（使条件变假）
	now := time.Now()
	db.Write([]model.MetricPoint{
		{Name: "cpu_usage_percent", Labels: map[string]string{"host": "test"},
			Value: 20, Timestamp: now.UnixMilli()},
	})

	// 再次评估：Firing → Resolved
	eng.Evaluate()

	alerts := eng.GetAlerts()
	if len(alerts) == 0 {
		t.Fatal("期望告警状态存在")
	}
	if alerts[0].State != StateResolved {
		t.Errorf("条件消除后应为 Resolved，实际 %q", alerts[0].State)
	}
	if alerts[0].ResolvedAt == nil {
		t.Error("ResolvedAt 应非空")
	}

	// 再次评估：Resolved → Inactive
	eng.Evaluate()
	alerts = eng.GetAlerts()
	if alerts[0].State != StateInactive {
		t.Errorf("Resolved 后应变为 Inactive，实际 %q", alerts[0].State)
	}
}

// TestEngineHistory 验证历史记录的正确性
func TestEngineHistory(t *testing.T) {
	db := newTestDB(t)
	defer db.Stop()
	eng := NewEngine(db)

	rule := newHighCPURule()
	eng.AddRule(rule)

	// 初始无历史
	if len(eng.GetHistory()) != 0 {
		t.Error("初始历史应为空")
	}

	// 触发状态变化
	eng.Evaluate()

	history := eng.GetHistory()
	if len(history) == 0 {
		t.Error("评估后应有历史记录")
	}
	// 最新记录应是 Firing（duration=0 直接触发）
	if history[0].RuleName != "high_cpu" {
		t.Errorf("历史规则名: 期望 'high_cpu'，实际 %q", history[0].RuleName)
	}
}

// TestEngineIntegration_TriggerAndResolve 集成测试：写入指标触发告警，更新指标恢复告警
func TestEngineIntegration_TriggerAndResolve(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	eng := NewEngine(db)

	// 注册规则（立即触发）
	rule := AlertRule{
		Name:       "integration_test",
		Expression: "avg(test_metric[5m])",
		Operator:   ">",
		Threshold:  50,
		Duration:   "0",
		Severity:   "warning",
	}
	if err := eng.AddRule(rule); err != nil {
		t.Fatalf("AddRule 失败: %v", err)
	}

	// 写入高于阈值的数据
	db.Write([]model.MetricPoint{
		{Name: "test_metric", Labels: nil, Value: 80,
			Timestamp: time.Now().UnixMilli()},
	})

	// 评估 → 应触发 Firing
	eng.Evaluate()

	active := eng.GetActiveAlerts()
	if len(active) == 0 {
		t.Fatal("期望有 Firing 告警")
	}
	if active[0].Rule.Name != "integration_test" {
		t.Errorf("触发告警名称错误: %q", active[0].Rule.Name)
	}

	// 写入低于阈值的数据
	db.Write([]model.MetricPoint{
		{Name: "test_metric", Labels: nil, Value: 20,
			Timestamp: time.Now().UnixMilli()},
	})

	// 评估 → 应解除 Firing
	eng.Evaluate()

	alerts := eng.GetAlerts()
	if len(alerts) == 0 {
		t.Fatal("期望告警状态仍存在")
	}
	if alerts[0].State != StateResolved {
		t.Errorf("期望 Resolved，实际 %q", alerts[0].State)
	}
}

// TestEngineRemoveNonExistent 验证删除不存在的规则返回错误
func TestEngineRemoveNonExistent(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()
	eng := NewEngine(db)

	err := eng.RemoveRule("nonexistent")
	if err == nil {
		t.Error("删除不存在的规则应返回错误")
	}
}
