// Package wql - 语法分析器测试
package wql

import (
	"testing"
	"time"
)

// TestParserFunctionCall 验证基本函数调用的 AST 结构
func TestParserFunctionCall(t *testing.T) {
	node, err := Parse("avg(cpu_usage_percent[5m])")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	fn, ok := node.(*FunctionCall)
	if !ok {
		t.Fatalf("期望 *FunctionCall，实际 %T", node)
	}
	if fn.Func != "avg" {
		t.Errorf("函数名：期望 'avg'，实际 %q", fn.Func)
	}
	if fn.Selector == nil || fn.Selector.Name != "cpu_usage_percent" {
		t.Errorf("选择器名称错误：%v", fn.Selector)
	}
	if fn.Range != 5*time.Minute {
		t.Errorf("时间范围：期望 5m，实际 %v", fn.Range)
	}
}

// TestParserWithLabels 验证带标签过滤器的函数调用
func TestParserWithLabels(t *testing.T) {
	node, err := Parse(`avg(cpu_usage_percent{host="DESKTOP"}[5m])`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	fn, ok := node.(*FunctionCall)
	if !ok {
		t.Fatalf("期望 *FunctionCall，实际 %T", node)
	}
	if fn.Selector.Labels["host"] != "DESKTOP" {
		t.Errorf("标签 host：期望 'DESKTOP'，实际 %q", fn.Selector.Labels["host"])
	}
}

// TestParserGroupBy 验证 "sum(...) by (label)" 语法
func TestParserGroupBy(t *testing.T) {
	node, err := Parse("sum(cpu_usage_percent) by (host)")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	gb, ok := node.(*GroupByExpr)
	if !ok {
		t.Fatalf("期望 *GroupByExpr，实际 %T", node)
	}
	if len(gb.Labels) != 1 || gb.Labels[0] != "host" {
		t.Errorf("分组标签：期望 [host]，实际 %v", gb.Labels)
	}

	fn, ok := gb.Expr.(*FunctionCall)
	if !ok {
		t.Fatalf("GroupByExpr.Expr 期望 *FunctionCall，实际 %T", gb.Expr)
	}
	if fn.Func != "sum" {
		t.Errorf("函数名：期望 'sum'，实际 %q", fn.Func)
	}
}

// TestParserComparison 验证比较表达式解析
func TestParserComparison(t *testing.T) {
	node, err := Parse("avg(cpu_usage_percent[5m]) > 80")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	cmp, ok := node.(*ComparisonExpr)
	if !ok {
		t.Fatalf("期望 *ComparisonExpr，实际 %T", node)
	}
	if cmp.Op != ">" {
		t.Errorf("运算符：期望 '>'，实际 %q", cmp.Op)
	}
	if cmp.Right != 80 {
		t.Errorf("右值：期望 80，实际 %v", cmp.Right)
	}
}

// TestParserArithmetic 验证算术表达式（乘除优先级）
func TestParserArithmetic(t *testing.T) {
	// metric_a / metric_b * 100 → (metric_a / metric_b) * 100
	node, err := Parse("metric_a / metric_b * 100")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	bin, ok := node.(*BinaryExpr)
	if !ok {
		t.Fatalf("期望 *BinaryExpr，实际 %T", node)
	}
	if bin.Op != "*" {
		t.Errorf("顶层运算符：期望 '*'，实际 %q", bin.Op)
	}

	// 左侧应为 metric_a / metric_b
	left, ok := bin.Left.(*BinaryExpr)
	if !ok {
		t.Fatalf("左侧期望 *BinaryExpr，实际 %T", bin.Left)
	}
	if left.Op != "/" {
		t.Errorf("左侧运算符：期望 '/'，实际 %q", left.Op)
	}
}

// TestParserAllFunctions 验证所有支持的函数名均可解析
func TestParserAllFunctions(t *testing.T) {
	fns := []string{"avg", "max", "min", "sum", "count", "rate", "last"}
	for _, fn := range fns {
		q := fn + "(some_metric[5m])"
		node, err := Parse(q)
		if err != nil {
			t.Errorf("函数 %q 解析失败: %v", fn, err)
			continue
		}
		call, ok := node.(*FunctionCall)
		if !ok {
			t.Errorf("函数 %q：期望 *FunctionCall，实际 %T", fn, node)
			continue
		}
		if call.Func != fn {
			t.Errorf("函数名：期望 %q，实际 %q", fn, call.Func)
		}
	}
}

// TestParserAllDurations 验证各种时长均可正确解析为 time.Duration
func TestParserAllDurations(t *testing.T) {
	cases := []struct {
		dur  string
		want time.Duration
	}{
		{"1m", time.Minute},
		{"5m", 5 * time.Minute},
		{"15m", 15 * time.Minute},
		{"1h", time.Hour},
		{"1d", 24 * time.Hour},
	}
	for _, c := range cases {
		q := "avg(m[" + c.dur + "])"
		node, err := Parse(q)
		if err != nil {
			t.Errorf("时长 %q 解析失败: %v", c.dur, err)
			continue
		}
		fn := node.(*FunctionCall)
		if fn.Range != c.want {
			t.Errorf("时长 %q：期望 %v，实际 %v", c.dur, c.want, fn.Range)
		}
	}
}
