// Package tracestore 提供分布式追踪数据的内存存储，支持按 trace_id 和服务名检索。
package tracestore

import (
	"sort"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

const (
	// maxTraces 是内存中保留的最大 trace 数量（环形缓冲区上限）。
	maxTraces = 1000
	// retentionDur 是 trace 数据的最大保留时长。
	retentionDur = time.Hour
)

// QueryOptions 控制 ListTraces 的过滤和分页行为。
type QueryOptions struct {
	Service       string // 按服务名过滤（空字符串表示不过滤）
	Limit         int    // 最多返回的 trace 数量（0 表示默认 50）
	MinDurationMs int64  // 只返回总时长不小于此值的 trace（0 表示不过滤）
}

// Store 是线程安全的内存 trace 存储。
// 当 trace 数量超过 maxTraces 时，最旧的 trace 会被自动淘汰。
// 后台协程每 5 分钟清理一次超过 retentionDur 的 trace。
type Store struct {
	mu     sync.RWMutex
	traces map[string]*model.Trace // 以 trace_id 为键
	order  []string                // 按插入顺序排列的 trace_id（最旧在前）
	stopCh chan struct{}
}

// New 创建并启动一个新的 trace 存储（包含后台清理协程）。
func New() *Store {
	s := &Store{
		traces: make(map[string]*model.Trace),
		stopCh: make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// Stop 停止后台清理协程。
func (s *Store) Stop() {
	close(s.stopCh)
}

// WriteSpans 将一批 span 写入存储，按 trace_id 分组合并到对应的 Trace 中。
// 如果某个 trace_id 已存在，新 span 会追加合并；否则新建 Trace。
// 忽略 trace_id 或 span_id 为空的 span。
func (s *Store) WriteSpans(spans []model.Span) {
	if len(spans) == 0 {
		return
	}

	// 按 trace_id 分组，过滤无效 span
	byTrace := make(map[string][]*model.Span)
	for i := range spans {
		sp := &spans[i]
		if sp.TraceID == "" || sp.SpanID == "" {
			continue
		}
		byTrace[sp.TraceID] = append(byTrace[sp.TraceID], sp)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for traceID, newSpans := range byTrace {
		if existing, ok := s.traces[traceID]; ok {
			// 合并到已有 trace
			existing.Spans = append(existing.Spans, newSpans...)
			rebuildTrace(existing)
		} else {
			// 新建 trace
			t := &model.Trace{TraceID: traceID, Spans: newSpans}
			rebuildTrace(t)
			s.traces[traceID] = t
			s.order = append(s.order, traceID)

			// 超出上限时淘汰最旧的 trace
			for len(s.traces) > maxTraces && len(s.order) > 0 {
				oldest := s.order[0]
				s.order = s.order[1:]
				delete(s.traces, oldest)
			}
		}
	}
}

// ListTraces 返回按插入时间倒序排列的 trace 摘要列表（最新的在前）。
func (s *Store) ListTraces(opts QueryOptions) []model.TraceSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	results := make([]model.TraceSummary, 0, opts.Limit)

	// 从最新到最旧遍历
	for i := len(s.order) - 1; i >= 0; i-- {
		t, ok := s.traces[s.order[i]]
		if !ok {
			continue
		}
		// 按最小持续时间过滤
		if opts.MinDurationMs > 0 && t.DurationMs < opts.MinDurationMs {
			continue
		}
		// 按服务名过滤
		if opts.Service != "" {
			found := false
			for _, svc := range t.ServiceNames {
				if svc == opts.Service {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		sum := model.TraceSummary{
			TraceID:      t.TraceID,
			StartTime:    t.StartTime,
			DurationMs:   t.DurationMs,
			SpanCount:    len(t.Spans),
			ServiceNames: t.ServiceNames,
			HasError:     t.HasError,
		}
		// 根 span（无 parent）决定摘要中的操作名和服务名
		for _, sp := range t.Spans {
			if sp.ParentSpanID == "" {
				sum.RootOperation = sp.OperationName
				sum.RootService = sp.ServiceName
				break
			}
		}

		results = append(results, sum)
		if len(results) >= opts.Limit {
			break
		}
	}
	return results
}

// GetTrace 按 trace_id 返回完整的 trace（包含所有 span）。
// 返回一份深拷贝，调用方可以安全地读取而无需加锁。
func (s *Store) GetTrace(traceID string) (*model.Trace, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.traces[traceID]
	if !ok {
		return nil, false
	}

	// 深拷贝，避免外部修改影响内部状态
	cp := *t
	cp.Spans = make([]*model.Span, len(t.Spans))
	for i, sp := range t.Spans {
		spCopy := *sp
		cp.Spans[i] = &spCopy
	}
	cp.ServiceNames = append([]string(nil), t.ServiceNames...)
	return &cp, true
}

// TotalCount 返回当前存储的 trace 数量。
func (s *Store) TotalCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.traces)
}

// cleanupLoop 每 5 分钟执行一次过期 trace 清理。
func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

// cleanup 删除超过 retentionDur 的 trace。
func (s *Store) cleanup() {
	cutoff := time.Now().Add(-retentionDur).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()

	newOrder := make([]string, 0, len(s.order))
	for _, id := range s.order {
		t, ok := s.traces[id]
		if !ok {
			continue
		}
		if t.StartTime < cutoff {
			delete(s.traces, id)
		} else {
			newOrder = append(newOrder, id)
		}
	}
	s.order = newOrder
}

// rebuildTrace 根据当前 span 列表重新计算 Trace 的聚合字段：
// StartTime、DurationMs、ServiceNames、HasError；同时对 span 排序（根 span 在前，其余按 StartTime 升序）。
// 调用方必须持有写锁。
func rebuildTrace(t *model.Trace) {
	if len(t.Spans) == 0 {
		return
	}

	minStart := t.Spans[0].StartTime
	maxEnd := t.Spans[0].StartTime + t.Spans[0].DurationMs
	serviceSet := make(map[string]struct{})
	hasError := false

	for _, sp := range t.Spans {
		if sp.StartTime < minStart {
			minStart = sp.StartTime
		}
		end := sp.StartTime + sp.DurationMs
		if end > maxEnd {
			maxEnd = end
		}
		if sp.ServiceName != "" {
			serviceSet[sp.ServiceName] = struct{}{}
		}
		if sp.Status == "error" {
			hasError = true
		}
	}

	t.StartTime = minStart
	if maxEnd > minStart {
		t.DurationMs = maxEnd - minStart
	}

	t.ServiceNames = make([]string, 0, len(serviceSet))
	for svc := range serviceSet {
		t.ServiceNames = append(t.ServiceNames, svc)
	}
	sort.Strings(t.ServiceNames)
	t.HasError = hasError

	// 根 span 排在最前，其余按 StartTime 升序
	sort.SliceStable(t.Spans, func(i, j int) bool {
		iRoot := t.Spans[i].ParentSpanID == ""
		jRoot := t.Spans[j].ParentSpanID == ""
		if iRoot != jRoot {
			return iRoot
		}
		return t.Spans[i].StartTime < t.Spans[j].StartTime
	})
}
