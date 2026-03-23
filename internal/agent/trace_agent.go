// Package agent — trace agent: generates synthetic distributed traces for testing.
package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// TraceAgent 定期生成合成分布式追踪数据并发送到 trace 摄入 API。
// 每次生成一个模拟 API 请求链路，包含根 span 和多个子 span。
type TraceAgent struct {
	ingestURL string
	interval  time.Duration
	stopCh    chan struct{}
	client    *http.Client
	rng       *rand.Rand
}

// NewTraceAgent 创建一个新的追踪代理，以指定间隔向 ingestURL 发送 span 数据。
func NewTraceAgent(ingestURL string, interval time.Duration) *TraceAgent {
	return &TraceAgent{
		ingestURL: ingestURL,
		interval:  interval,
		stopCh:    make(chan struct{}),
		client:    &http.Client{Timeout: 5 * time.Second},
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Start 在当前 goroutine 中启动追踪生成循环（启动时立即发送一条）。
func (a *TraceAgent) Start() {
	a.generateAndSend()

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.generateAndSend()
		case <-a.stopCh:
			return
		}
	}
}

// Stop 通知追踪代理停止运行。
func (a *TraceAgent) Stop() {
	close(a.stopCh)
}

// generateAndSend 生成一条合成 trace 并通过 HTTP POST 发送到摄入 API。
func (a *TraceAgent) generateAndSend() {
	spans := a.generateTrace()
	data, err := json.Marshal(spans)
	if err != nil {
		return
	}
	resp, err := a.client.Post(a.ingestURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return
	}
	resp.Body.Close()
}

// generateTrace 生成一个包含 4 个 span 的合成追踪：
//
//	根 span:      HTTP GET /api/users  (api-gateway,   50–200 ms)
//	子 span 1:    auth.validate_token  (auth-service,   5–20 ms)
//	子 span 2:    db.query_users       (user-service,  20–100 ms)
//	孙 span:      cache.lookup         (user-service,   1–5 ms)
//
// 有 20% 的概率在根 span 或 db span 上产生 error 状态。
func (a *TraceAgent) generateTrace() []model.Span {
	traceID := fmt.Sprintf("%016x%016x", a.rng.Int63(), a.rng.Int63())
	hasError := a.rng.Float64() < 0.20

	now := time.Now().UnixMilli()

	// 根 span
	rootID := a.newSpanID()
	rootDur := int64(50 + a.rng.Intn(151))
	rootStatus := spanStatus(hasError)

	rootSpan := model.Span{
		SpanID:        rootID,
		TraceID:       traceID,
		OperationName: "HTTP GET /api/users",
		ServiceName:   "api-gateway",
		StartTime:     now,
		DurationMs:    rootDur,
		Status:        rootStatus,
		Tags:          map[string]string{"http.method": "GET", "http.url": "/api/users"},
	}

	// 子 span 1: 认证令牌验证
	authID := a.newSpanID()
	authDur := int64(5 + a.rng.Intn(16))
	authSpan := model.Span{
		SpanID:        authID,
		ParentSpanID:  rootID,
		TraceID:       traceID,
		OperationName: "auth.validate_token",
		ServiceName:   "auth-service",
		StartTime:     now + 2,
		DurationMs:    authDur,
		Status:        "ok",
		Tags:          map[string]string{"component": "auth"},
	}

	// 子 span 2: 数据库查询
	dbID := a.newSpanID()
	dbDur := int64(20 + a.rng.Intn(81))
	dbStatus := spanStatus(hasError && a.rng.Float64() < 0.5)
	dbStart := now + 2 + authDur + 3

	dbSpan := model.Span{
		SpanID:        dbID,
		ParentSpanID:  rootID,
		TraceID:       traceID,
		OperationName: "db.query_users",
		ServiceName:   "user-service",
		StartTime:     dbStart,
		DurationMs:    dbDur,
		Status:        dbStatus,
		Tags:          map[string]string{"db.type": "postgresql", "db.statement": "SELECT * FROM users"},
	}

	// 孙 span: 缓存查找（db 查询的子操作）
	cacheID := a.newSpanID()
	cacheDur := int64(1 + a.rng.Intn(5))
	cacheSpan := model.Span{
		SpanID:        cacheID,
		ParentSpanID:  dbID,
		TraceID:       traceID,
		OperationName: "cache.lookup",
		ServiceName:   "user-service",
		StartTime:     dbStart + 2,
		DurationMs:    cacheDur,
		Status:        "ok",
		Tags:          map[string]string{"cache.type": "redis"},
	}

	return []model.Span{rootSpan, authSpan, dbSpan, cacheSpan}
}

// newSpanID 生成一个随机的 16 字符十六进制 span ID。
func (a *TraceAgent) newSpanID() string {
	return fmt.Sprintf("%016x", a.rng.Int63())
}

// spanStatus 根据 isError 标志返回 "ok" 或 "error"。
func spanStatus(isError bool) string {
	if isError {
		return "error"
	}
	return "ok"
}
