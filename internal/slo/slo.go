// Package slo 实现服务等级目标（SLO）的定义、SLI 计算和错误预算跟踪。
package slo

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/apaqa/watchtower/internal/wql"
)

// windowMinutes 将窗口字符串映射为分钟数
var windowMinutes = map[string]float64{
	"1d":  24 * 60,
	"7d":  7 * 24 * 60,
	"30d": 30 * 24 * 60,
}

// SLO 描述一条服务等级目标
type SLO struct {
	Name          string  `json:"name"`           // 唯一标识
	TargetPercent float64 `json:"target_percent"` // 目标 SLI（如 99.9）
	MetricType    string  `json:"metric_type"`    // "availability" | "latency"
	WQLQuery      string  `json:"wql_query"`      // 返回 0–100 的 WQL 表达式
	Window        string  `json:"window"`         // "1d" | "7d" | "30d"
	CreatedAt     int64   `json:"created_at"`     // Unix 毫秒
}

// SLOStatus 是 SLO 的当前计算结果
type SLOStatus struct {
	SLO                 SLO     `json:"slo"`
	CurrentSLI          float64 `json:"current_sli"`           // 0–100
	ErrorBudgetTotal    float64 `json:"error_budget_total"`    // 允许中断的总分钟数
	ErrorBudgetConsumed float64 `json:"error_budget_consumed"` // 已消耗的分钟数
	ErrorBudgetPct      float64 `json:"error_budget_pct"`      // 剩余预算百分比（0–100）
	Healthy             bool    `json:"healthy"`               // SLI >= target
}

// Store 是线程安全的内存 SLO 存储
type Store struct {
	mu   sync.RWMutex
	slos map[string]*SLO
	db   *tsdb.TSDB
}

// NewStore 创建由给定 TSDB 支撑的 SLO 存储
func NewStore(db *tsdb.TSDB) *Store {
	return &Store{
		slos: make(map[string]*SLO),
		db:   db,
	}
}

// Add 添加或替换一条 SLO；字段不合法时返回错误
func (s *Store) Add(slo SLO) error {
	if slo.Name == "" {
		return fmt.Errorf("SLO name is required")
	}
	if slo.TargetPercent <= 0 || slo.TargetPercent >= 100 {
		return fmt.Errorf("target_percent must be between 0 and 100 (exclusive)")
	}
	if slo.WQLQuery == "" {
		return fmt.Errorf("wql_query is required")
	}
	if _, ok := windowMinutes[slo.Window]; !ok {
		slo.Window = "30d" // 默认使用 30 天窗口
	}
	if slo.CreatedAt == 0 {
		slo.CreatedAt = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slos[slo.Name] = &slo
	return nil
}

// Remove 按名称删除一条 SLO
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.slos[name]; !ok {
		return fmt.Errorf("SLO %q not found", name)
	}
	delete(s.slos, name)
	return nil
}

// ListStatuses 返回所有 SLO 的当前状态（按名称排序）
func (s *Store) ListStatuses() []SLOStatus {
	s.mu.RLock()
	slos := make([]*SLO, 0, len(s.slos))
	for _, slo := range s.slos {
		cp := *slo
		slos = append(slos, &cp)
	}
	s.mu.RUnlock()

	sort.Slice(slos, func(i, j int) bool { return slos[i].Name < slos[j].Name })

	statuses := make([]SLOStatus, 0, len(slos))
	for _, slo := range slos {
		statuses = append(statuses, s.computeStatus(slo))
	}
	return statuses
}

// Count 返回 SLO 数量（用于测试和启动日志）
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.slos)
}

// computeStatus 求值 WQL 查询并计算 SLI 与错误预算
func (s *Store) computeStatus(slo *SLO) SLOStatus {
	sli := s.evalSLI(slo.WQLQuery)

	winMin := windowMinutes[slo.Window]
	if winMin == 0 {
		winMin = windowMinutes["30d"]
	}

	// 错误预算总量 = (1 - target%) × window_minutes
	budgetTotal := (1 - slo.TargetPercent/100) * winMin

	// 已消耗 = (1 - current_sli%) × window_minutes
	consumed := (1 - sli/100) * winMin
	if consumed < 0 {
		consumed = 0
	}

	// 剩余预算百分比 = (budgetTotal - consumed) / budgetTotal × 100
	remaining := budgetTotal - consumed
	var pct float64
	if budgetTotal > 0 {
		pct = (remaining / budgetTotal) * 100
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
	}

	return SLOStatus{
		SLO:                 *slo,
		CurrentSLI:          sli,
		ErrorBudgetTotal:    budgetTotal,
		ErrorBudgetConsumed: consumed,
		ErrorBudgetPct:      pct,
		Healthy:             sli >= slo.TargetPercent,
	}
}

// evalSLI 执行 WQL 查询，返回 0–100 范围内的 SLI 值。
// 查询失败（如指标尚无数据）返回 0。
func (s *Store) evalSLI(query string) float64 {
	if s.db == nil {
		return 0
	}
	result, err := wql.EvalQuery(s.db, query)
	if err != nil {
		return 0
	}
	switch result.Type {
	case wql.ResultScalar:
		if result.Scalar != nil {
			v := *result.Scalar
			if v < 0 {
				return 0
			}
			if v > 100 {
				return 100
			}
			return v
		}
	case wql.ResultBool:
		if result.Bool != nil && *result.Bool {
			return 100
		}
		return 0
	case wql.ResultVector:
		if len(result.Vector) > 0 {
			sum := 0.0
			for _, sample := range result.Vector {
				sum += sample.Value
			}
			v := sum / float64(len(result.Vector))
			if v < 0 {
				return 0
			}
			if v > 100 {
				return 100
			}
			return v
		}
	}
	return 0
}
