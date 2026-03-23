// Package ingest 提供指标数据摄入的 HTTP 服务
package ingest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/auth"
	"github.com/apaqa/watchtower/internal/export"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/notify"
	"github.com/apaqa/watchtower/internal/pipeline"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/registry"
	"github.com/apaqa/watchtower/internal/servicemap"
	"github.com/apaqa/watchtower/internal/slo"
	"github.com/apaqa/watchtower/internal/tracestore"
	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/apaqa/watchtower/internal/wql"
)

// Server 负责接收 HTTP POST 请求并将数据写入 TSDB
type Server struct {
	db       *tsdb.TSDB
	logStore *logstore.Store // 可选的日志存储；由 RegisterLogStore 注入
	keyStore *auth.KeyStore  // 可选的 API 密钥存储；nil 表示开放模式
	mux      *http.ServeMux  // 保留对路由器的引用，以便后续注册路由
	httpSrv  *http.Server
	listener net.Listener
}

// New 创建摄入服务实例，绑定到指定地址
func New(addr string, db *tsdb.TSDB) (*Server, error) {
	mux := http.NewServeMux()
	s := &Server{db: db, mux: mux}

	// 注册 POST /api/v1/metrics 路由
	mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	// 注册 GET /api/v1/query 路由（WQL 查询接口）
	mux.HandleFunc("/api/v1/query", s.handleQuery)
	// 注册 POST /api/v1/metrics/prometheus 路由（Prometheus 文本格式摄入）
	mux.HandleFunc("/api/v1/metrics/prometheus", s.handlePrometheusIngest)
	// 注册 GET /metrics 路由（Prometheus 抓取端点；始终公开）
	mux.HandleFunc("/metrics", s.handlePrometheusScrape)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("摄入服务监听 %s 失败: %w", addr, err)
	}

	s.listener = ln
	s.httpSrv = &http.Server{
		// Server 实现 http.Handler 接口，在 ServeHTTP 中执行认证检查
		Handler:      s,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s, nil
}

// ServeHTTP 实现 http.Handler，在委托给 mux 之前执行 API 密钥认证检查
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth.Middleware(s.keyStore, s.mux).ServeHTTP(w, r)
}

// RegisterKeyStore 注入 API 密钥存储，启用认证（必须在 Start 之前调用）
func (s *Server) RegisterKeyStore(ks *auth.KeyStore) {
	s.keyStore = ks
	auth.RegisterRoutes(s.mux, ks)
}

// Addr 返回实际监听地址（用于测试时获取随机端口）
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// RegisterAlertEngine 将告警 API 路由注册到现有 ServeMux（必须在 Start 之前调用）
// 设计为独立方法，避免修改 New() 签名影响现有测试
func (s *Server) RegisterAlertEngine(engine *alert.Engine) {
	alert.RegisterRoutes(s.mux, engine)
}

// RegisterProbeManager 注入探针管理器并注册探针 API 路由（必须在 Start 之前调用）
func (s *Server) RegisterProbeManager(pm *probe.Manager) {
	probe.RegisterRoutes(s.mux, pm)
}

// RegisterTraceStore 注入 trace 存储并注册 trace API 路由（必须在 Start 之前调用）
func (s *Server) RegisterTraceStore(ts *tracestore.Store) {
	tracestore.RegisterRoutes(s.mux, ts)
}

// RegisterAgentRegistry registers agent registry API routes (must be called before Start).
func (s *Server) RegisterAgentRegistry(reg *registry.Registry) {
	registry.RegisterRoutes(s.mux, reg)
}

// RegisterNotifyRouter 注册通知历史 API 路由（必须在 Start 之前调用）
func (s *Server) RegisterNotifyRouter(r *notify.Router) {
	notify.RegisterRoutes(s.mux, r.History())
}

// RegisterServiceMapBuilder 注册服务地图 API 路由（必须在 Start 之前调用）
func (s *Server) RegisterServiceMapBuilder(b *servicemap.Builder) {
	servicemap.RegisterRoutes(s.mux, b)
}

// RegisterSLOStore 注册 SLO CRUD API 路由（必须在 Start 之前调用）
func (s *Server) RegisterSLOStore(store *slo.Store) {
	slo.RegisterRoutes(s.mux, store)
}

// RegisterPipeline 注册聚合管道 CRUD API 路由（必须在 Start 之前调用）
func (s *Server) RegisterPipeline(p *pipeline.Pipeline) {
	pipeline.RegisterRoutes(s.mux, p)
}

// RegisterExportHandler 注册数据导出 API 路由（必须在 Start 之前调用）
func (s *Server) RegisterExportHandler(h *export.Handler) {
	export.RegisterRoutes(s.mux, h)
}

// RegisterLogStore 注入日志存储并注册日志 API 路由（必须在 Start 之前调用）
func (s *Server) RegisterLogStore(ls *logstore.Store) {
	s.logStore = ls
	// POST/GET /api/v1/logs — 批量写入 / 搜索日志
	s.mux.HandleFunc("/api/v1/logs", s.handleLogs)
	// POST /api/v1/logs/single — 写入单条日志（方便客户端调用）
	s.mux.HandleFunc("/api/v1/logs/single", s.handleLogSingle)
}

// Start 启动 HTTP 服务（阻塞，应在 goroutine 中调用）
func (s *Server) Start() error {
	return s.httpSrv.Serve(s.listener)
}

// Close 优雅关闭 HTTP 服务
func (s *Server) Close() error {
	return s.httpSrv.Close()
}

// handleQuery 处理 GET /api/v1/query?q=<WQL> 请求
// 返回 JSON 格式的查询结果（标量、向量或布尔值）
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	// 允许跨域（浏览器仪表板与此端口不同）
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"仅支持 GET 方法"}`, http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, `{"error":"缺少查询参数 q"}`, http.StatusBadRequest)
		return
	}

	result, err := wql.EvalQuery(s.db, q)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(result)
}

// handleMetrics 处理 POST /api/v1/metrics 请求
// 接受 JSON 数组格式的指标数据点，验证后写入 TSDB
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// 仅允许 POST 方法
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST 方法", http.StatusMethodNotAllowed)
		return
	}

	// 限制请求体大小为 1MB，防止滥用
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var points []model.MetricPoint
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&points); err != nil {
		http.Error(w, fmt.Sprintf("JSON 解析失败: %v", err), http.StatusBadRequest)
		return
	}

	// 验证并补全时间戳
	now := time.Now().UnixMilli()
	for i := range points {
		if points[i].Name == "" {
			http.Error(w, "指标名称不能为空", http.StatusBadRequest)
			return
		}
		// 若客户端未提供时间戳，使用服务器当前时间
		if points[i].Timestamp == 0 {
			points[i].Timestamp = now
		}
	}

	// 写入 TSDB
	s.db.Write(points)

	w.WriteHeader(http.StatusNoContent)
}

// setCORSHeaders 设置通用 CORS 响应头（允许仪表板跨域调用）
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

// handleLogs 处理 POST /api/v1/logs（批量写入）和 GET /api/v1/logs（搜索）
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.logStore == nil {
		http.Error(w, `{"error":"日志存储未初始化"}`, http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodPost:
		// 批量写入：接受 JSON 数组格式的日志条目
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		var entries []model.LogEntry
		if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusBadRequest)
			return
		}
		s.logStore.WriteMany(entries)
		w.WriteHeader(http.StatusNoContent)

	case http.MethodGet:
		// 搜索：支持 q、level、source、limit 查询参数
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		opts := logstore.SearchOptions{
			Query:  q.Get("q"),
			Level:  model.LogLevel(q.Get("level")),
			Source: q.Get("source"),
			Limit:  limit,
		}
		results := s.logStore.Search(opts)
		if results == nil {
			results = []model.LogEntry{}
		}
		json.NewEncoder(w).Encode(results)

	default:
		http.Error(w, `{"error":"仅支持 GET 和 POST 方法"}`, http.StatusMethodNotAllowed)
	}
}

// handleLogSingle 处理 POST /api/v1/logs/single — 写入单条日志记录
func (s *Server) handleLogSingle(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"仅支持 POST 方法"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.logStore == nil {
		http.Error(w, `{"error":"日志存储未初始化"}`, http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var entry model.LogEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), http.StatusBadRequest)
		return
	}
	s.logStore.Write(entry)
	w.WriteHeader(http.StatusNoContent)
}
