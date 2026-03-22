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

	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFiles embed.FS

// upgrader 将 HTTP 连接升级为 WebSocket
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 开发模式允许所有来源
}

// wsMessage 是通过 WebSocket 推送给浏览器的消息结构
type wsMessage struct {
	// Metrics 存储指标名称 → 最新值的映射
	Metrics map[string]float64 `json:"metrics"`
	// Ts 是服务器推送时间（Unix 毫秒）
	Ts int64 `json:"ts"`
}

// client 代表一个 WebSocket 连接
type client struct {
	conn *websocket.Conn
	send chan []byte
}

// Server 是仪表板 HTTP 服务，包含 WebSocket hub
type Server struct {
	db       *tsdb.TSDB
	httpSrv  *http.Server
	listener net.Listener

	// hub 管理所有活跃 WebSocket 客户端
	mu      sync.RWMutex
	clients map[*client]struct{}

	stopCh chan struct{}
}

// New 创建仪表板服务实例并绑定到 addr
func New(addr string, db *tsdb.TSDB) (*Server, error) {
	s := &Server{
		db:      db,
		clients: make(map[*client]struct{}),
		stopCh:  make(chan struct{}),
	}

	mux := http.NewServeMux()

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

	msg := wsMessage{Metrics: metrics, Ts: now}
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
