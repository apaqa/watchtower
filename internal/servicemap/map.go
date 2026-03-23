// Package servicemap 分析 trace 数据，构建服务间的依赖关系图。
package servicemap

import (
	"sort"
	"strings"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tracestore"
)

// 服务类型常量
const (
	TypeAPI      = "api"
	TypeDatabase = "database"
	TypeCache    = "cache"
	TypeQueue    = "queue"
	TypeExternal = "external"
)

// ServiceNode 表示服务依赖图中的一个节点（服务）
type ServiceNode struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`          // api/database/cache/queue/external
	RequestCount int64   `json:"request_count"` // 该服务处理的总请求数
	ErrorCount   int64   `json:"error_count"`   // 错误请求数
	AvgLatencyMs float64 `json:"avg_latency_ms"` // 平均延迟（毫秒）
}

// ServiceEdge 表示两个服务之间的调用关系
type ServiceEdge struct {
	FromService  string  `json:"from_service"`  // 调用方
	ToService    string  `json:"to_service"`    // 被调用方
	RequestCount int64   `json:"request_count"` // 调用次数
	ErrorRate    float64 `json:"error_rate"`    // 错误率（0.0–1.0）
	AvgLatencyMs float64 `json:"avg_latency_ms"` // 平均延迟（毫秒）
}

// ServiceMap 是完整的服务依赖图，包含节点列表和边列表
type ServiceMap struct {
	Nodes []ServiceNode `json:"nodes"`
	Edges []ServiceEdge `json:"edges"`
}

// edgeKey 用于在 map 中唯一标识一条有向边
type edgeKey struct{ from, to string }

// nodeStats 累计单个服务节点的指标数据
type nodeStats struct {
	count      int64
	errCount   int64
	totalLatMs int64
}

// edgeStats 累计单条调用边的指标数据
type edgeStats struct {
	count      int64
	errCount   int64
	totalLatMs int64
}

// Builder 依赖 tracestore.Store，按需构建服务依赖图
type Builder struct {
	store *tracestore.Store
}

// NewBuilder 创建 Builder 实例
func NewBuilder(store *tracestore.Store) *Builder {
	return &Builder{store: store}
}

// Build 读取当前所有 trace 并构建服务依赖图
func (b *Builder) Build() ServiceMap {
	return BuildFromTraces(b.store.AllTraces())
}

// BuildFromTraces 从给定 trace 列表构建服务依赖图（方便单元测试直接调用）
func BuildFromTraces(traces []*model.Trace) ServiceMap {
	nodes := make(map[string]*nodeStats)
	edges := make(map[edgeKey]*edgeStats)

	for _, t := range traces {
		// 构建 spanID → span 的快速查找表
		spanMap := make(map[string]*model.Span, len(t.Spans))
		for _, sp := range t.Spans {
			spanMap[sp.SpanID] = sp
		}

		for _, sp := range t.Spans {
			// 累计节点统计
			ns, ok := nodes[sp.ServiceName]
			if !ok {
				ns = &nodeStats{}
				nodes[sp.ServiceName] = ns
			}
			ns.count++
			if sp.Status == "error" {
				ns.errCount++
			}
			ns.totalLatMs += sp.DurationMs

			// 若父 span 属于不同服务，则记录一条有向调用边
			if sp.ParentSpanID != "" {
				parent, ok := spanMap[sp.ParentSpanID]
				if ok && parent.ServiceName != sp.ServiceName {
					k := edgeKey{from: parent.ServiceName, to: sp.ServiceName}
					es, ok := edges[k]
					if !ok {
						es = &edgeStats{}
						edges[k] = es
					}
					es.count++
					if sp.Status == "error" {
						es.errCount++
					}
					es.totalLatMs += sp.DurationMs
				}
			}
		}
	}

	// 将节点统计转换为 ServiceNode 列表（按名称排序，结果确定性更强）
	nodeList := make([]ServiceNode, 0, len(nodes))
	for name, ns := range nodes {
		avgLat := 0.0
		if ns.count > 0 {
			avgLat = float64(ns.totalLatMs) / float64(ns.count)
		}
		nodeList = append(nodeList, ServiceNode{
			Name:         name,
			Type:         inferServiceType(name),
			RequestCount: ns.count,
			ErrorCount:   ns.errCount,
			AvgLatencyMs: avgLat,
		})
	}
	sort.Slice(nodeList, func(i, j int) bool { return nodeList[i].Name < nodeList[j].Name })

	// 将边统计转换为 ServiceEdge 列表（按 from→to 排序）
	edgeList := make([]ServiceEdge, 0, len(edges))
	for k, es := range edges {
		errRate := 0.0
		if es.count > 0 {
			errRate = float64(es.errCount) / float64(es.count)
		}
		avgLat := 0.0
		if es.count > 0 {
			avgLat = float64(es.totalLatMs) / float64(es.count)
		}
		edgeList = append(edgeList, ServiceEdge{
			FromService:  k.from,
			ToService:    k.to,
			RequestCount: es.count,
			ErrorRate:    errRate,
			AvgLatencyMs: avgLat,
		})
	}
	sort.Slice(edgeList, func(i, j int) bool {
		if edgeList[i].FromService != edgeList[j].FromService {
			return edgeList[i].FromService < edgeList[j].FromService
		}
		return edgeList[i].ToService < edgeList[j].ToService
	})

	return ServiceMap{Nodes: nodeList, Edges: edgeList}
}

// inferServiceType 根据服务名中的关键词推断服务类型。
// 无法识别时默认为 "api"。
func inferServiceType(name string) string {
	lower := strings.ToLower(name)
	for _, kw := range []string{"postgres", "mysql", "mongo", "sqlite", "database", "-db", "_db"} {
		if strings.Contains(lower, kw) {
			return TypeDatabase
		}
	}
	for _, kw := range []string{"redis", "memcache", "cache"} {
		if strings.Contains(lower, kw) {
			return TypeCache
		}
	}
	for _, kw := range []string{"kafka", "rabbitmq", "sqs", "nats", "queue", "broker"} {
		if strings.Contains(lower, kw) {
			return TypeQueue
		}
	}
	for _, kw := range []string{"external", "third-party", "partner", "vendor"} {
		if strings.Contains(lower, kw) {
			return TypeExternal
		}
	}
	return TypeAPI
}
