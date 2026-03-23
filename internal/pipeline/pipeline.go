// Package pipeline 实现指标聚合管道：按规则将原始指标聚合为新序列并写回 TSDB。
package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// 支持的窗口时长映射
var windowDuration = map[string]time.Duration{
	"1m": time.Minute,
	"5m": 5 * time.Minute,
	"1h": time.Hour,
}

// Rule 描述一条聚合规则
type Rule struct {
	Name         string   `json:"name"`          // 规则唯一名称
	InputMetric  string   `json:"input_metric"`  // 原始指标名称
	OutputMetric string   `json:"output_metric"` // 输出聚合指标名称
	Aggregation  string   `json:"aggregation"`   // avg/sum/max/min/count/p50/p95/p99
	Window       string   `json:"window"`        // 1m/5m/1h
	GroupBy      []string `json:"group_by"`      // 按标签键分组（空表示全量聚合）
	CreatedAt    int64    `json:"created_at"`    // Unix 毫秒创建时间
}

// validate 校验规则字段的合法性
func (r *Rule) validate() error {
	if r.Name == "" {
		return fmt.Errorf("规则 name 不能为空")
	}
	if r.InputMetric == "" {
		return fmt.Errorf("input_metric 不能为空")
	}
	if r.OutputMetric == "" {
		return fmt.Errorf("output_metric 不能为空")
	}
	validAgg := map[string]bool{"avg": true, "sum": true, "max": true, "min": true, "count": true, "p50": true, "p95": true, "p99": true}
	if !validAgg[r.Aggregation] {
		return fmt.Errorf("aggregation 必须是 avg/sum/max/min/count/p50/p95/p99 之一")
	}
	if _, ok := windowDuration[r.Window]; !ok {
		return fmt.Errorf("window 必须是 1m/5m/1h 之一")
	}
	return nil
}

// Pipeline 管理聚合规则，并在后台定期计算聚合结果
type Pipeline struct {
	mu     sync.RWMutex
	rules  map[string]*Rule
	db     *tsdb.TSDB
	stopCh chan struct{}
}

// New 创建聚合管道实例
func New(db *tsdb.TSDB) *Pipeline {
	return &Pipeline{
		rules:  make(map[string]*Rule),
		db:     db,
		stopCh: make(chan struct{}),
	}
}

// AddRule 添加或替换一条聚合规则
func (p *Pipeline) AddRule(rule Rule) error {
	if err := rule.validate(); err != nil {
		return err
	}
	if rule.CreatedAt == 0 {
		rule.CreatedAt = time.Now().UnixMilli()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules[rule.Name] = &rule
	return nil
}

// RemoveRule 按名称删除一条规则
func (p *Pipeline) RemoveRule(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.rules[name]; !ok {
		return fmt.Errorf("规则 %q 不存在", name)
	}
	delete(p.rules, name)
	return nil
}

// ListRules 返回所有规则的副本（按名称排序）
func (p *Pipeline) ListRules() []Rule {
	p.mu.RLock()
	rules := make([]*Rule, 0, len(p.rules))
	for _, r := range p.rules {
		rules = append(rules, r)
	}
	p.mu.RUnlock()

	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	out := make([]Rule, len(rules))
	for i, r := range rules {
		out[i] = *r
	}
	return out
}

// Count 返回规则数量
func (p *Pipeline) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.rules)
}

// Start 启动后台聚合循环（每分钟执行一次）
func (p *Pipeline) Start() {
	go p.evalLoop()
}

// Stop 停止后台聚合循环
func (p *Pipeline) Stop() {
	close(p.stopCh)
}

// evalLoop 每分钟对所有规则执行一次聚合计算
func (p *Pipeline) evalLoop() {
	// 对齐到下一分钟整点再触发，减少噪声
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	// 启动时立即执行一次
	p.evaluate()
	for {
		select {
		case <-ticker.C:
			p.evaluate()
		case <-p.stopCh:
			return
		}
	}
}

// evaluate 对所有规则顺序求值
func (p *Pipeline) evaluate() {
	p.mu.RLock()
	rules := make([]*Rule, 0, len(p.rules))
	for _, r := range p.rules {
		rules = append(rules, r)
	}
	p.mu.RUnlock()

	for _, r := range rules {
		p.evalRule(r)
	}
}

// evalRule 对单条规则求值并将结果写入 TSDB
func (p *Pipeline) evalRule(rule *Rule) {
	dur := windowDuration[rule.Window]
	now := time.Now()
	start := now.Add(-dur).UnixMilli()
	end := now.UnixMilli()

	// 获取输入指标的所有序列
	series := p.db.GetSeries(rule.InputMetric)
	if len(series) == 0 {
		return
	}

	// 按 group_by 标签分组序列
	type group struct {
		labels map[string]string
		values []float64
	}
	groups := make(map[string]*group)

	for _, s := range series {
		points := s.QueryRange(start, end)
		if len(points) == 0 {
			continue
		}
		key := buildGroupKey(s.Labels, rule.GroupBy)
		g, ok := groups[key]
		if !ok {
			// 提取 group_by 对应的标签子集
			outLabels := make(map[string]string)
			for _, k := range rule.GroupBy {
				if v, exists := s.Labels[k]; exists {
					outLabels[k] = v
				}
			}
			g = &group{labels: outLabels}
			groups[key] = g
		}
		for _, dp := range points {
			g.values = append(g.values, dp.Value)
		}
	}

	// 对每个分组计算聚合值并写入 TSDB
	ts := now.UnixMilli()
	for _, g := range groups {
		if len(g.values) == 0 {
			continue
		}
		v := computeAgg(rule.Aggregation, g.values)
		p.db.Write([]model.MetricPoint{{
			Name:      rule.OutputMetric,
			Labels:    g.labels,
			Value:     v,
			Timestamp: ts,
		}})
	}
}

// buildGroupKey 根据标签集合和 group_by 键列表生成分组 key
func buildGroupKey(labels map[string]string, groupBy []string) string {
	if len(groupBy) == 0 {
		return "__all__"
	}
	parts := make([]string, 0, len(groupBy))
	for _, k := range groupBy {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// computeAgg 对给定的值切片执行指定聚合函数
func computeAgg(agg string, values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	switch agg {
	case "sum":
		s := 0.0
		for _, v := range values {
			s += v
		}
		return s
	case "avg":
		s := 0.0
		for _, v := range values {
			s += v
		}
		return s / float64(len(values))
	case "max":
		m := values[0]
		for _, v := range values[1:] {
			if v > m {
				m = v
			}
		}
		return m
	case "min":
		m := values[0]
		for _, v := range values[1:] {
			if v < m {
				m = v
			}
		}
		return m
	case "count":
		return float64(len(values))
	case "p50":
		return percentile(values, 50)
	case "p95":
		return percentile(values, 95)
	case "p99":
		return percentile(values, 99)
	default:
		return 0
	}
}

// percentile 计算给定百分位数的值（线性插值）
// pct 范围：0–100
func percentile(values []float64, pct float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	if pct <= 0 {
		return sorted[0]
	}
	if pct >= 100 {
		return sorted[len(sorted)-1]
	}

	// 线性插值
	idx := (pct / 100) * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
