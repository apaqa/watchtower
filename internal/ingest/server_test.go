// Package ingest 单元测试：验证 HTTP 摄入端点的正常和异常行为
package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// makeServer 创建用于测试的摄入服务（绑定随机端口）
func makeServer(t *testing.T) *Server {
	t.Helper()
	db := tsdb.New()
	t.Cleanup(func() { db.Stop() })

	srv, err := New("127.0.0.1:0", db)
	if err != nil {
		t.Fatalf("创建摄入服务失败: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

// TestValidPost 验证合法的指标 JSON 能被正确接受并存入 TSDB
func TestValidPost(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()

	srv := &Server{db: db}

	points := []model.MetricPoint{
		{
			Name:      "cpu_usage_percent",
			Labels:    map[string]string{"host": "test"},
			Value:     55.5,
			Timestamp: time.Now().UnixMilli(),
		},
	}
	body, _ := json.Marshal(points)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleMetrics(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("期望状态码 204，实际 %d：%s", w.Code, w.Body.String())
	}

	// 验证数据确实写入了 TSDB
	if db.SeriesCount() != 1 {
		t.Errorf("期望 TSDB 中有 1 个序列，实际 %d", db.SeriesCount())
	}
}

// TestInvalidJSONRejected 验证无效 JSON 请求被拒绝并返回 400
func TestInvalidJSONRejected(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()

	srv := &Server{db: db}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics",
		bytes.NewBufferString("{ not valid json !!!"))
	w := httptest.NewRecorder()

	srv.handleMetrics(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("期望状态码 400，实际 %d", w.Code)
	}
}

// TestEmptyNameRejected 验证指标名为空时请求被拒绝
func TestEmptyNameRejected(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()

	srv := &Server{db: db}

	points := []model.MetricPoint{
		{Name: "", Labels: map[string]string{"host": "x"}, Value: 1.0},
	}
	body, _ := json.Marshal(points)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMetrics(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("空名称应返回 400，实际 %d", w.Code)
	}
}

// TestMethodNotAllowed 验证非 POST 请求被拒绝
func TestMethodNotAllowed(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()

	srv := &Server{db: db}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	w := httptest.NewRecorder()

	srv.handleMetrics(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 请求应返回 405，实际 %d", w.Code)
	}
}

// TestTimestampAutoFilled 验证缺少 timestamp 时服务器自动填充
func TestTimestampAutoFilled(t *testing.T) {
	db := tsdb.New()
	defer db.Stop()

	srv := &Server{db: db}

	// Timestamp 故意设为 0
	points := []model.MetricPoint{
		{Name: "test_metric", Labels: map[string]string{"host": "h"}, Value: 42, Timestamp: 0},
	}
	body, _ := json.Marshal(points)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
	w := httptest.NewRecorder()

	before := time.Now().UnixMilli()
	srv.handleMetrics(w, req)
	after := time.Now().UnixMilli()

	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，实际 %d", w.Code)
	}

	// 查询 TSDB 验证时间戳已被填充
	pts := db.QueryRange("test_metric", map[string]string{"host": "h"}, before-100, after+100)
	if len(pts) == 0 {
		t.Fatal("未找到写入的数据点")
	}
	if pts[0].Timestamp < before || pts[0].Timestamp > after+1000 {
		t.Errorf("自动填充的时间戳不在合理范围内: %d", pts[0].Timestamp)
	}
}
