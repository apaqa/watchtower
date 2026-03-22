// Package ingest - 日志摄入 API 单元测试
package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// newLogServer 创建附带日志存储的测试服务实例
func newLogServer(t *testing.T) (*Server, *logstore.Store) {
	t.Helper()
	db := tsdb.New()
	t.Cleanup(func() { db.Stop() })

	ls := logstore.New()
	t.Cleanup(func() { ls.Stop() })

	srv := &Server{db: db, logStore: ls}
	return srv, ls
}

// TestLogPostMany 验证 POST /api/v1/logs 批量写入多条日志
func TestLogPostMany(t *testing.T) {
	srv, ls := newLogServer(t)

	entries := []model.LogEntry{
		{Level: model.LogLevelInfo, Source: "app", Message: "server started"},
		{Level: model.LogLevelError, Source: "app", Message: "connection failed"},
	}
	body, _ := json.Marshal(entries)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleLogs(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，实际 %d：%s", w.Code, w.Body.String())
	}
	if ls.TotalCount() != 2 {
		t.Errorf("期望存储 2 条日志，实际 %d 条", ls.TotalCount())
	}
}

// TestLogPostSingle 验证 POST /api/v1/logs/single 写入单条日志
func TestLogPostSingle(t *testing.T) {
	srv, ls := newLogServer(t)

	entry := model.LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     model.LogLevelWarn,
		Source:    "agent",
		Message:   "CPU spike detected",
	}
	body, _ := json.Marshal(entry)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/single", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleLogSingle(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，实际 %d：%s", w.Code, w.Body.String())
	}
	if ls.TotalCount() != 1 {
		t.Errorf("期望存储 1 条日志，实际 %d 条", ls.TotalCount())
	}
}

// TestLogSearch 验证 GET /api/v1/logs 返回匹配的日志条目
func TestLogSearch(t *testing.T) {
	srv, ls := newLogServer(t)

	ls.Write(model.LogEntry{Level: model.LogLevelError, Source: "db", Message: "query timeout"})
	ls.Write(model.LogEntry{Level: model.LogLevelInfo, Source: "api", Message: "request received"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?q=timeout", nil)
	w := httptest.NewRecorder()

	srv.handleLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}

	var results []model.LogEntry
	if err := json.NewDecoder(w.Body).Decode(&results); err != nil {
		t.Fatalf("响应 JSON 解析失败: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("搜索 timeout 期望 1 条，实际 %d 条", len(results))
	}
}

// TestLogSearchLevelFilter 验证 GET /api/v1/logs?level=error 只返回错误日志
func TestLogSearchLevelFilter(t *testing.T) {
	srv, ls := newLogServer(t)

	ls.Write(model.LogEntry{Level: model.LogLevelError, Source: "svc", Message: "fatal error"})
	ls.Write(model.LogEntry{Level: model.LogLevelInfo, Source: "svc", Message: "all good"})
	ls.Write(model.LogEntry{Level: model.LogLevelWarn, Source: "svc", Message: "high load"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?level=error", nil)
	w := httptest.NewRecorder()

	srv.handleLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}

	var results []model.LogEntry
	json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 1 {
		t.Errorf("级别过滤 error 期望 1 条，实际 %d 条", len(results))
	}
	if results[0].Level != model.LogLevelError {
		t.Errorf("期望 error 级别，实际 %q", results[0].Level)
	}
}

// TestLogSearchEmpty 验证无匹配结果时返回空数组（而非 null）
func TestLogSearchEmpty(t *testing.T) {
	srv, _ := newLogServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?q=nonexistent", nil)
	w := httptest.NewRecorder()

	srv.handleLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	body := w.Body.String()
	if body == "null\n" || body == "null" {
		t.Error("无结果时应返回空数组 []，而非 null")
	}
}

// TestLogPostInvalidJSON 验证非法 JSON 被拒绝
func TestLogPostInvalidJSON(t *testing.T) {
	srv, _ := newLogServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs",
		bytes.NewReader([]byte("not-json")))
	w := httptest.NewRecorder()

	srv.handleLogs(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("非法 JSON 期望 400，实际 %d", w.Code)
	}
}
