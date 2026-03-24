package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// newTestHandler 构造用于测试的 Admin Handler（使用 tsdb.New() 纯内存模式）
func newTestHandler() *Handler {
	db := tsdb.New()
	cfg := config.Default()
	cfg.APIKeys = []config.APIKeyConfig{
		{Name: "test-key", Key: "super-secret-123", Permissions: []string{"read", "write"}},
	}
	re := tsdb.NewRetentionEngine(db)
	return New(db, cfg, re, "watchtower.yaml")
}

func TestAdminStatus_ValidData(t *testing.T) {
	h := newTestHandler()
	// 写入一些数据点以验证 TSDB 统计
	h.db.Write([]model.MetricPoint{
		{Name: "cpu_usage_percent", Labels: map[string]string{}, Value: 45.0, Timestamp: time.Now().UnixMilli()},
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var status SystemStatus
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if status.UptimeSecs < 0 {
		t.Errorf("uptime_seconds should be >= 0, got %.2f", status.UptimeSecs)
	}
	if status.Version == "" {
		t.Error("expected non-empty version")
	}
	if status.GoVersion == "" {
		t.Error("expected non-empty go_version")
	}
	if status.Goroutines <= 0 {
		t.Errorf("expected positive goroutine count, got %d", status.Goroutines)
	}
	if status.HeapAllocMB < 0 {
		t.Errorf("heap_alloc_mb should be >= 0, got %.2f", status.HeapAllocMB)
	}
	if status.TSDBSeriesCount < 1 {
		t.Errorf("expected at least 1 TSDB series after writing, got %d", status.TSDBSeriesCount)
	}
	if status.TSDBTotalPoints < 1 {
		t.Errorf("expected at least 1 TSDB data point after writing, got %d", status.TSDBTotalPoints)
	}
	if status.TimestampMs == 0 {
		t.Error("expected non-zero timestamp_ms")
	}
}

func TestAdminGC_OK(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/gc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", resp["status"])
	}
}

func TestAdminSnapshot_OK(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/snapshot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", resp["status"])
	}
}

func TestAdminConfig_RedactsKeys(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var view configView
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(view.APIKeys) != 1 {
		t.Fatalf("expected 1 API key entry, got %d", len(view.APIKeys))
	}
	if view.APIKeys[0].Key == "super-secret-123" {
		t.Error("API key value must be redacted in config response")
	}
	if view.APIKeys[0].Key != "***REDACTED***" {
		t.Errorf("expected ***REDACTED***, got %q", view.APIKeys[0].Key)
	}
	if view.APIKeys[0].Name != "test-key" {
		t.Errorf("expected name=test-key, got %q", view.APIKeys[0].Name)
	}
}

func TestAdminReload_NoFile(t *testing.T) {
	h := newTestHandler()
	h.configPath = "nonexistent_watchtower_test.yaml"
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/reload", nil))
	// 文件不存在时应返回 200（不报错，使用内存中默认值）
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminMethodNotAllowed(t *testing.T) {
	h := newTestHandler()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	tests := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/status"},
		{http.MethodGet, "/api/v1/admin/gc"},
		{http.MethodGet, "/api/v1/admin/snapshot"},
		{http.MethodPost, "/api/v1/admin/config"},
		{http.MethodGet, "/api/v1/admin/reload"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: expected 405, got %d", tt.method, tt.path, rec.Code)
		}
	}
}
