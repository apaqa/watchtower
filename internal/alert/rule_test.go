// Package alert - 告警规则类型和状态机测试
package alert

import (
	"testing"
	"time"
)

// TestAlertRuleValidate_Valid 验证合法规则通过校验
func TestAlertRuleValidate_Valid(t *testing.T) {
	rule := AlertRule{
		Name:       "high_cpu",
		Expression: "avg(cpu_usage_percent[5m])",
		Operator:   ">",
		Threshold:  80,
		Duration:   "5m",
		Severity:   "critical",
	}
	if err := rule.Validate(); err != nil {
		t.Errorf("合法规则校验失败: %v", err)
	}
}

// TestAlertRuleValidate_EmptyName 验证名称为空时返回错误
func TestAlertRuleValidate_EmptyName(t *testing.T) {
	rule := AlertRule{
		Expression: "avg(cpu[5m])",
		Operator:   ">",
		Threshold:  80,
		Severity:   "warning",
	}
	if err := rule.Validate(); err == nil {
		t.Error("期望名称为空时返回错误")
	}
}

// TestAlertRuleValidate_InvalidOperator 验证非法运算符被拒绝
func TestAlertRuleValidate_InvalidOperator(t *testing.T) {
	rule := AlertRule{
		Name:       "test",
		Expression: "avg(cpu[5m])",
		Operator:   "??",
		Threshold:  80,
		Severity:   "info",
	}
	if err := rule.Validate(); err == nil {
		t.Error("期望非法运算符返回错误")
	}
}

// TestAlertRuleValidate_InvalidSeverity 验证非法严重级别被拒绝
func TestAlertRuleValidate_InvalidSeverity(t *testing.T) {
	rule := AlertRule{
		Name:       "test",
		Expression: "avg(cpu[5m])",
		Operator:   ">",
		Threshold:  80,
		Severity:   "extreme", // 非法值
	}
	if err := rule.Validate(); err == nil {
		t.Error("期望非法 severity 返回错误")
	}
}

// TestAlertRuleValidate_AllOperators 验证所有合法运算符均通过校验
func TestAlertRuleValidate_AllOperators(t *testing.T) {
	ops := []string{">", "<", ">=", "<=", "==", "!="}
	for _, op := range ops {
		rule := AlertRule{
			Name:       "test_" + op,
			Expression: "avg(cpu[5m])",
			Operator:   op,
			Threshold:  80,
			Severity:   "warning",
		}
		if err := rule.Validate(); err != nil {
			t.Errorf("运算符 %q 应通过校验，实际错误: %v", op, err)
		}
	}
}

// TestAlertRuleParseDuration 验证时长字符串解析
func TestAlertRuleParseDuration(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"30s", 30 * time.Second},
		{"invalid", 0},
	}
	for _, c := range cases {
		rule := AlertRule{Duration: c.input}
		got := rule.ParseDuration()
		if got != c.want {
			t.Errorf("ParseDuration(%q): 期望 %v，实际 %v", c.input, c.want, got)
		}
	}
}

// TestApplyOperator 验证比较运算符的正确性
func TestApplyOperator(t *testing.T) {
	cases := []struct {
		op   string
		a, b float64
		want bool
	}{
		{">", 90, 80, true},
		{">", 70, 80, false},
		{"<", 70, 80, true},
		{"<", 90, 80, false},
		{">=", 80, 80, true},
		{"<=", 80, 80, true},
		{"==", 80, 80, true},
		{"==", 80, 81, false},
		{"!=", 80, 81, true},
		{"!=", 80, 80, false},
	}
	for _, c := range cases {
		got := applyOperator(c.op, c.a, c.b)
		if got != c.want {
			t.Errorf("applyOperator(%q, %v, %v): 期望 %v，实际 %v",
				c.op, c.a, c.b, c.want, got)
		}
	}
}
