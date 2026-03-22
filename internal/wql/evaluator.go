// Package wql - 求值器：将 AST 对 TSDB 数据求值，返回标量或向量结果
package wql

import (
	"fmt"
	"math"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// 结果类型
// ─────────────────────────────────────────────────────────────────────────────

// ResultType 标识求值结果的种类
type ResultType string

const (
	ResultScalar ResultType = "scalar" // 单个数值
	ResultVector ResultType = "vector" // 多个带标签的样本
	ResultBool   ResultType = "bool"   // 比较运算结果
)

// Sample 是向量结果中的一个元素：标签 → 值
type Sample struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// Result 是求值器的输出
type Result struct {
	Type   ResultType `json:"type"`
	Scalar *float64   `json:"scalar,omitempty"` // ResultScalar 时非空
	Vector []Sample   `json:"vector,omitempty"` // ResultVector 时非空
	Bool   *bool      `json:"bool,omitempty"`   // ResultBool 时非空
}

// 默认时间范围：5 分钟
const defaultRange = 5 * time.Minute

// ─────────────────────────────────────────────────────────────────────────────
// 求值器
// ─────────────────────────────────────────────────────────────────────────────

// Evaluator 使用 TSDB 中的数据对 WQL AST 求值
type Evaluator struct {
	db  *tsdb.TSDB
	now time.Time // 求值基准时间（便于测试时注入固定时间）
}

// NewEvaluator 创建求值器，使用当前时间作为基准
func NewEvaluator(db *tsdb.TSDB) *Evaluator {
	return &Evaluator{db: db, now: time.Now()}
}

// NewEvaluatorAt 创建求值器，使用指定时间作为基准（用于测试）
func NewEvaluatorAt(db *tsdb.TSDB, at time.Time) *Evaluator {
	return &Evaluator{db: db, now: at}
}

// Eval 对 AST 节点求值并返回结果
func (e *Evaluator) Eval(node Node) (*Result, error) {
	switch n := node.(type) {
	case *FunctionCall:
		return e.evalFunction(n)
	case *MetricSelector:
		return e.evalSelector(n)
	case *BinaryExpr:
		return e.evalBinary(n)
	case *NumberLiteral:
		v := n.Value
		return &Result{Type: ResultScalar, Scalar: &v}, nil
	case *ComparisonExpr:
		return e.evalComparison(n)
	case *GroupByExpr:
		return e.evalGroupBy(n)
	default:
		return nil, fmt.Errorf("未知 AST 节点类型: %T", node)
	}
}

// evalFunction 对聚合函数求值：avg/max/min/sum/count/rate/last
func (e *Evaluator) evalFunction(n *FunctionCall) (*Result, error) {
	rang := n.Range
	if rang == 0 {
		rang = defaultRange // 未指定时间窗口则使用默认值
	}

	end := e.now.UnixMilli()
	start := e.now.Add(-rang).UnixMilli()

	// 找到所有匹配指标名称和标签过滤器的序列
	seriesList := e.matchSeries(n.Selector.Name, n.Selector.Labels)
	if len(seriesList) == 0 {
		return nil, fmt.Errorf("指标 %q 无数据", n.Selector.Name)
	}

	// 单序列→标量，多序列→向量
	if len(seriesList) == 1 {
		points := seriesList[0].QueryRange(start, end)
		v, err := applyFunction(n.Func, points, rang)
		if err != nil {
			return nil, err
		}
		return &Result{Type: ResultScalar, Scalar: &v}, nil
	}

	// 多序列返回向量结果
	samples := make([]Sample, 0, len(seriesList))
	for _, s := range seriesList {
		points := s.QueryRange(start, end)
		v, err := applyFunction(n.Func, points, rang)
		if err != nil {
			continue // 无数据的序列跳过
		}
		samples = append(samples, Sample{Labels: copyLabels(s.Labels), Value: v})
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("指标 %q 在时间范围内无数据", n.Selector.Name)
	}
	return &Result{Type: ResultVector, Vector: samples}, nil
}

// evalSelector 对裸指标选择器求值（取最新一个点）
func (e *Evaluator) evalSelector(n *MetricSelector) (*Result, error) {
	seriesList := e.matchSeries(n.Name, n.Labels)
	if len(seriesList) == 0 {
		return nil, fmt.Errorf("指标 %q 无数据", n.Name)
	}

	if len(seriesList) == 1 {
		pts := seriesList[0].Latest(1)
		if len(pts) == 0 {
			return nil, fmt.Errorf("指标 %q 无数据", n.Name)
		}
		v := pts[0].Value
		return &Result{Type: ResultScalar, Scalar: &v}, nil
	}

	samples := make([]Sample, 0, len(seriesList))
	for _, s := range seriesList {
		pts := s.Latest(1)
		if len(pts) == 0 {
			continue
		}
		samples = append(samples, Sample{Labels: copyLabels(s.Labels), Value: pts[0].Value})
	}
	return &Result{Type: ResultVector, Vector: samples}, nil
}

// evalBinary 对算术表达式求值
func (e *Evaluator) evalBinary(n *BinaryExpr) (*Result, error) {
	left, err := e.Eval(n.Left)
	if err != nil {
		return nil, err
	}
	right, err := e.Eval(n.Right)
	if err != nil {
		return nil, err
	}

	// 两个标量之间的运算
	if left.Type == ResultScalar && right.Type == ResultScalar {
		v, err := applyArith(n.Op, *left.Scalar, *right.Scalar)
		if err != nil {
			return nil, err
		}
		return &Result{Type: ResultScalar, Scalar: &v}, nil
	}

	// 向量与标量：对向量中每个元素运算
	if left.Type == ResultVector && right.Type == ResultScalar {
		samples := make([]Sample, len(left.Vector))
		for i, s := range left.Vector {
			v, err := applyArith(n.Op, s.Value, *right.Scalar)
			if err != nil {
				return nil, err
			}
			samples[i] = Sample{Labels: s.Labels, Value: v}
		}
		return &Result{Type: ResultVector, Vector: samples}, nil
	}

	if left.Type == ResultScalar && right.Type == ResultVector {
		samples := make([]Sample, len(right.Vector))
		for i, s := range right.Vector {
			v, err := applyArith(n.Op, *left.Scalar, s.Value)
			if err != nil {
				return nil, err
			}
			samples[i] = Sample{Labels: s.Labels, Value: v}
		}
		return &Result{Type: ResultVector, Vector: samples}, nil
	}

	return nil, fmt.Errorf("不支持向量之间的算术运算")
}

// evalComparison 对比较表达式求值，返回 bool 结果
func (e *Evaluator) evalComparison(n *ComparisonExpr) (*Result, error) {
	leftRes, err := e.Eval(n.Left)
	if err != nil {
		return nil, err
	}

	var leftVal float64
	if leftRes.Type == ResultScalar {
		leftVal = *leftRes.Scalar
	} else {
		return nil, fmt.Errorf("比较运算左侧必须为标量，当前为 %s", leftRes.Type)
	}

	b := applyComparison(n.Op, leftVal, n.Right)
	return &Result{Type: ResultBool, Bool: &b}, nil
}

// evalGroupBy 对 "expr by (labels)" 形式求值
func (e *Evaluator) evalGroupBy(n *GroupByExpr) (*Result, error) {
	// 只支持最内层为 FunctionCall 的情况
	fn, ok := n.Expr.(*FunctionCall)
	if !ok {
		return nil, fmt.Errorf("by 子句只支持直接函数调用")
	}

	rang := fn.Range
	if rang == 0 {
		rang = defaultRange
	}

	end := e.now.UnixMilli()
	start := e.now.Add(-rang).UnixMilli()

	seriesList := e.matchSeries(fn.Selector.Name, fn.Selector.Labels)
	if len(seriesList) == 0 {
		return nil, fmt.Errorf("指标 %q 无数据", fn.Selector.Name)
	}

	// 按分组标签汇总数据
	type group struct {
		labels map[string]string
		vals   []float64
	}
	groupKey := func(s *tsdb.Series) string {
		// 构建分组键（按分组标签排序）
		key := ""
		for _, lbl := range n.Labels {
			key += lbl + "=" + s.Labels[lbl] + ","
		}
		return key
	}

	groups := make(map[string]*group)
	for _, s := range seriesList {
		key := groupKey(s)
		if _, exists := groups[key]; !exists {
			// 提取分组标签子集
			lbls := make(map[string]string, len(n.Labels))
			for _, lbl := range n.Labels {
				lbls[lbl] = s.Labels[lbl]
			}
			groups[key] = &group{labels: lbls}
		}
		points := s.QueryRange(start, end)
		v, err := applyFunction(fn.Func, points, rang)
		if err != nil {
			continue
		}
		groups[key].vals = append(groups[key].vals, v)
	}

	// 对每个分组应用聚合函数
	samples := make([]Sample, 0, len(groups))
	for _, g := range groups {
		if len(g.vals) == 0 {
			continue
		}
		var agg float64
		switch fn.Func {
		case "sum":
			for _, v := range g.vals {
				agg += v
			}
		case "avg":
			for _, v := range g.vals {
				agg += v
			}
			agg /= float64(len(g.vals))
		case "max":
			agg = g.vals[0]
			for _, v := range g.vals[1:] {
				if v > agg {
					agg = v
				}
			}
		case "min":
			agg = g.vals[0]
			for _, v := range g.vals[1:] {
				if v < agg {
					agg = v
				}
			}
		case "count":
			agg = float64(len(g.vals))
		default:
			agg = g.vals[len(g.vals)-1]
		}
		samples = append(samples, Sample{Labels: g.labels, Value: agg})
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("分组聚合结果为空")
	}
	return &Result{Type: ResultVector, Vector: samples}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 内部辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// matchSeries 返回所有满足名称和标签过滤器的序列
func (e *Evaluator) matchSeries(name string, filters map[string]string) []*tsdb.Series {
	all := e.db.GetSeries(name)
	if len(filters) == 0 {
		return all
	}
	result := all[:0:0]
	for _, s := range all {
		if labelsMatch(s.Labels, filters) {
			result = append(result, s)
		}
	}
	return result
}

// labelsMatch 检查序列标签是否满足所有过滤器条件
func labelsMatch(seriesLabels, filters map[string]string) bool {
	for k, v := range filters {
		if seriesLabels[k] != v {
			return false
		}
	}
	return true
}

// applyFunction 对数据点切片应用聚合函数
func applyFunction(fn string, points []model.DataPoint, rang time.Duration) (float64, error) {
	if len(points) == 0 {
		return 0, fmt.Errorf("无数据点")
	}

	switch fn {
	case "avg":
		sum := 0.0
		for _, p := range points {
			sum += p.Value
		}
		return sum / float64(len(points)), nil

	case "max":
		max := math.Inf(-1)
		for _, p := range points {
			if p.Value > max {
				max = p.Value
			}
		}
		return max, nil

	case "min":
		min := math.Inf(1)
		for _, p := range points {
			if p.Value < min {
				min = p.Value
			}
		}
		return min, nil

	case "sum":
		sum := 0.0
		for _, p := range points {
			sum += p.Value
		}
		return sum, nil

	case "count":
		return float64(len(points)), nil

	case "rate":
		if len(points) < 2 {
			return 0, fmt.Errorf("rate 需要至少 2 个数据点")
		}
		first := points[0]
		last := points[len(points)-1]
		durationSec := float64(rang) / float64(time.Second)
		if durationSec == 0 {
			return 0, fmt.Errorf("时间范围为 0")
		}
		return (last.Value - first.Value) / durationSec, nil

	case "last":
		return points[len(points)-1].Value, nil

	default:
		return 0, fmt.Errorf("未知函数 %q", fn)
	}
}

// applyArith 执行二元算术运算
func applyArith(op string, a, b float64) (float64, error) {
	switch op {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "/":
		if b == 0 {
			return 0, fmt.Errorf("除以零")
		}
		return a / b, nil
	default:
		return 0, fmt.Errorf("未知运算符 %q", op)
	}
}

// applyComparison 执行比较运算并返回 bool
func applyComparison(op string, a, b float64) bool {
	switch op {
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "<":
		return a < b
	case "<=":
		return a <= b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

// copyLabels 复制标签 map，避免外部修改影响结果
func copyLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// EvalQuery 是便捷函数：解析并求值一条 WQL 查询
func EvalQuery(db *tsdb.TSDB, query string) (*Result, error) {
	node, err := Parse(query)
	if err != nil {
		return nil, err
	}
	ev := NewEvaluator(db)
	return ev.Eval(node)
}
