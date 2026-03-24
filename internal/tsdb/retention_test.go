package tsdb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// writeTimestampedMetric 以指定时间戳写入一个数据点
func writeTimestampedMetric(db *TSDB, name string, ts int64, val float64) {
	db.Write([]model.MetricPoint{{
		Name:      name,
		Labels:    map[string]string{},
		Value:     val,
		Timestamp: ts,
	}})
}

// TestRetention_OldDataCleaned 验证超过 max_age 的数据被清理
func TestRetention_OldDataCleaned(t *testing.T) {
	db := New()
	re := NewRetentionEngine(db)

	now := time.Now().UnixMilli()
	// 写入一个 2 小时前的点（超过默认 raw 1h 保留）
	writeTimestampedMetric(db, "cpu_usage_percent", now-2*3600*1000, 75.0)
	// 写入一个当前点
	writeTimestampedMetric(db, "cpu_usage_percent", now, 50.0)

	series := db.GetSeries("cpu_usage_percent")
	if len(series) == 0 {
		t.Fatal("series not found after write")
	}
	if series[0].Len() != 2 {
		t.Fatalf("expected 2 points before enforcement, got %d", series[0].Len())
	}

	re.EnforceAll()

	if series[0].Len() != 1 {
		t.Errorf("expected 1 point after enforcement (old data pruned), got %d", series[0].Len())
	}
}

// TestRetention_MaxPointsEnforced 验证序列长度限制正常工作
func TestRetention_MaxPointsEnforced(t *testing.T) {
	db := New()

	// 写入 50 个点
	now := time.Now().UnixMilli()
	for i := 0; i < 50; i++ {
		writeTimestampedMetric(db, "memory_usage_percent", now-int64(i)*1000, float64(i))
	}

	series := db.GetSeries("memory_usage_percent")
	if len(series) == 0 || series[0].Len() != 50 {
		t.Fatalf("expected 50 points, got %d", series[0].Len())
	}

	// 添加带 MaxPoints=10 的自定义策略
	re := NewRetentionEngine(db)
	if err := re.AddPolicy(RetentionPolicy{
		Name:         "mem-limit",
		MatchPattern: `^memory_usage_percent$`,
		MaxAgeSecs:   0, // 不按时间限制
		MaxPoints:    10,
	}); err != nil {
		t.Fatalf("AddPolicy failed: %v", err)
	}

	re.EnforceAll()

	if series[0].Len() != 10 {
		t.Errorf("expected 10 points after max_points enforcement, got %d", series[0].Len())
	}
}

// TestRetention_CustomPolicyOverridesBuiltin 验证自定义策略优先于内置策略
func TestRetention_CustomPolicyOverridesBuiltin(t *testing.T) {
	db := New()
	re := NewRetentionEngine(db)

	now := time.Now().UnixMilli()
	// 写入一个 45 分钟前的点（在内置 raw 1h 保留内，但自定义策略设置 30 分钟）
	writeTimestampedMetric(db, "disk_usage_percent", now-45*60*1000, 60.0)
	writeTimestampedMetric(db, "disk_usage_percent", now, 62.0)

	// 自定义策略：30 分钟保留
	if err := re.AddPolicy(RetentionPolicy{
		Name:         "short-disk",
		MatchPattern: `^disk_usage_percent$`,
		MaxAgeSecs:   1800, // 30 分钟
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	series := db.GetSeries("disk_usage_percent")
	if len(series) == 0 {
		t.Fatal("series not found")
	}
	if series[0].Len() != 2 {
		t.Fatalf("expected 2 points before enforcement, got %d", series[0].Len())
	}

	re.EnforceAll()

	// 45 分钟前的点应被自定义策略删除
	if series[0].Len() != 1 {
		t.Errorf("expected 1 point after custom policy enforcement, got %d", series[0].Len())
	}
}

// TestRetention_DownsampledSeriesLongerRetention 验证 :1m 序列保留时间比 raw 更长
func TestRetention_DownsampledSeriesLongerRetention(t *testing.T) {
	db := New()
	re := NewRetentionEngine(db)

	now := time.Now().UnixMilli()
	// 写入一个 2 小时前的 :1m 点（超过 raw 1h 保留，但在 1m-ds 24h 保留内）
	writeTimestampedMetric(db, "cpu_usage_percent:1m", now-2*3600*1000, 55.0)
	writeTimestampedMetric(db, "cpu_usage_percent:1m", now, 58.0)

	re.EnforceAll()

	series := db.GetSeries("cpu_usage_percent:1m")
	if len(series) == 0 {
		t.Fatal(":1m series not found after enforcement")
	}
	// 2 小时内的点应该被 1m-ds 24h 策略保留
	if series[0].Len() < 2 {
		t.Errorf("expected :1m point from 2h ago to be preserved (24h retention), got %d points", series[0].Len())
	}
}

// TestRetention_AddDeletePolicy 验证策略 CRUD 操作
func TestRetention_AddDeletePolicy(t *testing.T) {
	re := NewRetentionEngine(New())

	// 添加
	if err := re.AddPolicy(RetentionPolicy{
		Name:         "test-policy",
		MatchPattern: `^test_.*`,
		MaxAgeSecs:   300,
	}); err != nil {
		t.Fatalf("AddPolicy: %v", err)
	}

	policies := re.ListPolicies()
	found := false
	for _, p := range policies {
		if p.Name == "test-policy" {
			found = true
		}
	}
	if !found {
		t.Error("expected test-policy in list after add")
	}

	// 重复添加应报错
	if err := re.AddPolicy(RetentionPolicy{
		Name: "test-policy", MatchPattern: `^test_.*`, MaxAgeSecs: 300,
	}); err == nil {
		t.Error("expected error for duplicate policy name")
	}

	// 删除
	if err := re.DeletePolicy("test-policy"); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	for _, p := range re.ListPolicies() {
		if p.Name == "test-policy" {
			t.Error("policy should be removed after delete")
		}
	}

	// 不能删除内置策略
	if err := re.DeletePolicy("raw"); err == nil {
		t.Error("expected error when deleting built-in policy")
	}
}

// TestRetention_InvalidRegexRejected 验证非法正则被拒绝
func TestRetention_InvalidRegexRejected(t *testing.T) {
	re := NewRetentionEngine(New())
	err := re.AddPolicy(RetentionPolicy{
		Name:         "bad",
		MatchPattern: `[invalid`,
		MaxAgeSecs:   3600,
	})
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

// ── HTTP API 测试 ──────────────────────────────────────────────────────────────

func TestRetentionAPI_List(t *testing.T) {
	mux := http.NewServeMux()
	re := NewRetentionEngine(New())
	RegisterRetentionRoutes(mux, re)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/retention", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var policies []RetentionPolicy
	if err := json.NewDecoder(rec.Body).Decode(&policies); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(policies) < 3 {
		t.Errorf("expected at least 3 built-in policies, got %d", len(policies))
	}
}

func TestRetentionAPI_CreateDelete(t *testing.T) {
	mux := http.NewServeMux()
	re := NewRetentionEngine(New())
	RegisterRetentionRoutes(mux, re)

	// 创建策略
	body := `{"name":"api-test","match_pattern":"^api_.*","max_age_seconds":600}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/retention",
		strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// 删除策略
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/retention/api-test", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRetentionAPI_DeleteBuiltin_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	re := NewRetentionEngine(New())
	RegisterRetentionRoutes(mux, re)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/retention/raw", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
