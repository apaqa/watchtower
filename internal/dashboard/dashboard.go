// Package dashboard 提供 Web 仪表板的 HTTP 服务和 WebSocket 推送
package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFiles embed.FS

// upgrader 将 HTTP 连接升级为 WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 开发模式允许所有来源
}

// AlertInfo 是 WebSocket 消息中内嵌的轻量告警摘要
type AlertInfo struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Severity string `json:"severity"`
	Value    float64 `json:"value"`
	Message  string `json:"message"`
}

// wsMessage 是通过 WebSocket 推送给浏览器的消息结构
type wsMessage struct {
	// Metrics 存储指标名称 → 最新值的映射
	Metrics map[string]float64 `json:"metrics"`
	// Ts 是服务器推送时间（Unix 毫秒）
	Ts int64 `json:"ts"`
	// Alerts 是当前非 Inactive 状态的告警摘要列表
	Alerts []AlertInfo `json:"alerts"`
	// Logs 是最近 20 条日志记录（按时间升序，最新在最后）
	Logs []model.LogEntry `json:"logs"`
}

// client 代表一个 WebSocket 连接
type client struct {
	conn *websocket.Conn
	send chan []byte
}

// Server 是仪表板 HTTP 服务，包含 WebSocket hub
type Server struct {
	db       *tsdb.TSDB
	mux      *http.ServeMux
	httpSrv  *http.Server
	listener net.Listener

	// hub 管理所有活跃 WebSocket 客户端
	mu      sync.RWMutex
	clients map[*client]struct{}

	stopCh chan struct{}

	// alertEngine 是可选的告警引擎引用，用于在 WebSocket 推送中包含告警状态
	alertEngine *alert.Engine
	// logStore 是可选的日志存储引用，用于在 WebSocket 推送中包含最近日志
	logStore *logstore.Store
}

// New 创建仪表板服务实例并绑定到 addr
func New(addr string, db *tsdb.TSDB) (*Server, error) {
	mux := http.NewServeMux()
	s := &Server{
		db:      db,
		mux:     mux,
		clients: make(map[*client]struct{}),
		stopCh:  make(chan struct{}),
	}

	// 提取嵌入的 static 子目录为文件系统
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("嵌入文件系统初始化失败: %w", err)
	}

	// GET / 提供单页应用 HTML
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	// GET /ws 升级为 WebSocket
	mux.HandleFunc("/ws", s.handleWS)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("仪表板服务监听 %s 失败: %w", addr, err)
	}

	s.listener = ln
	s.httpSrv = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	return s, nil
}

// Addr 返回实际监听地址
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// SetAlertEngine 注入告警引擎引用，使 WebSocket 推送中包含告警状态（必须在 Start 之前调用）
func (s *Server) SetAlertEngine(engine *alert.Engine) {
	s.mu.Lock()
	s.alertEngine = engine
	s.mu.Unlock()
}

// SetLogStore 注入日志存储引用，使 WebSocket 推送中包含最近日志（必须在 Start 之前调用）
func (s *Server) SetLogStore(ls *logstore.Store) {
	s.mu.Lock()
	s.logStore = ls
	s.mu.Unlock()
}

// SetPanelStore registers custom-panel API routes on the dashboard server.
// Must be called before Start.
func (s *Server) SetPanelStore(ps *PanelStore) {
	RegisterPanelRoutes(s.mux, ps)
}

// Start 启动 HTTP 服务和推送循环（应在 goroutine 中调用）
func (s *Server) Start() error {
	go s.broadcastLoop() // 后台每 5 秒向所有客户端推送最新指标
	return s.httpSrv.Serve(s.listener)
}

// Close 优雅关闭服务
func (s *Server) Close() error {
	close(s.stopCh)
	return s.httpSrv.Close()
}

// handleWS 处理 WebSocket 握手并注册客户端
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	c := &client{
		conn: conn,
		send: make(chan []byte, 32),
	}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	// 启动写协程
	go s.writeLoop(c)

	// 读循环：仅用于检测连接断开，忽略浏览器发来的帧
	for {
		if _, _, err := conn.NextReader(); err != nil {
			break
		}
	}

	// 客户端断开，清理资源
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	close(c.send)
}

// writeLoop 将消息从 send 通道写入 WebSocket 连接
func (s *Server) writeLoop(c *client) {
	defer c.conn.Close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			break
		}
	}
}

// broadcastLoop 每 5 秒采样一次 TSDB 最新值并广播给所有客户端
func (s *Server) broadcastLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.broadcast()
		case <-s.stopCh:
			return
		}
	}
}

// broadcast 读取各指标的最新数据点并发送给所有已连接的 WebSocket 客户端
func (s *Server) broadcast() {
	// 需要推送的指标名称列表
	metricNames := []string{
		"cpu_usage_percent",
		"memory_usage_percent",
		"memory_used_bytes",
		"disk_usage_percent",
	}

	metrics := make(map[string]float64)
	fiveMinAgo := time.Now().Add(-5 * time.Minute).UnixMilli()
	now := time.Now().UnixMilli()

	for _, name := range metricNames {
		seriesList := s.db.GetSeries(name)
		if len(seriesList) == 0 {
			continue
		}
		// 取第一个匹配序列的最新数据点
		points := seriesList[0].QueryRange(fiveMinAgo, now)
		if len(points) > 0 {
			metrics[name] = points[len(points)-1].Value
		}
	}

	if len(metrics) == 0 {
		return // 尚无数据，不推送空消息
	}

	// 收集非 Inactive 状态的告警摘要
	var alertInfos []AlertInfo
	s.mu.RLock()
	engine := s.alertEngine
	s.mu.RUnlock()
	if engine != nil {
		for _, a := range engine.GetAlerts() {
			if a.State != alert.StateInactive {
				alertInfos = append(alertInfos, AlertInfo{
					Name:     a.Rule.Name,
					State:    string(a.State),
					Severity: a.Rule.Severity,
					Value:    a.LastValue,
					Message:  a.Message,
				})
			}
		}
	}
	if alertInfos == nil {
		alertInfos = []AlertInfo{}
	}

	// 收集最近 20 条日志（升序排列：最旧在前，最新在后，便于浏览器追加显示）
	var recentLogs []model.LogEntry
	s.mu.RLock()
	ls := s.logStore
	s.mu.RUnlock()
	if ls != nil {
		desc := ls.Recent(20) // Search 返回降序，翻转为升序方便追加
		recentLogs = make([]model.LogEntry, len(desc))
		for i, e := range desc {
			recentLogs[len(desc)-1-i] = e
		}
	}
	if recentLogs == nil {
		recentLogs = []model.LogEntry{}
	}

	msg := wsMessage{Metrics: metrics, Ts: now, Alerts: alertInfos, Logs: recentLogs}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	// 广播给所有客户端（使用非阻塞发送，避免慢客户端阻塞整体）
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.clients {
		select {
		case c.send <- data:
		default:
			// 客户端消费过慢，跳过本次推送
		}
	}
}
