// Package ingest 提供指标数据摄入的 HTTP 服务
package ingest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/apaqa/watchtower/internal/wql"
)

// Server 负责接收 HTTP POST 请求并将数据写入 TSDB
type Server struct {
	db       *tsdb.TSDB
	httpSrv  *http.Server
	listener net.Listener
}

// New 创建摄入服务实例，绑定到指定地址
func New(addr string, db *tsdb.TSDB) (*Server, error) {
	mux := http.NewServeMux()
	s := &Server{db: db}

	// 注册 POST /api/v1/metrics 路由
	mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	// 注册 GET /api/v1/query 路由（WQL 查询接口）
	mux.HandleFunc("/api/v1/query", s.handleQuery)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("摄入服务监听 %s 失败: %w", addr, err)
	}

	s.listener = ln
	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	return s, nil
}

// Addr 返回实际监听地址（用于测试时获取随机端口）
func (s *Server) Addr() string {
	return s.listener.Addr().String()
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
