package quota

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestQuotaManager_Defaults 默认配额应预设三种资源类型
func TestQuotaManager_Defaults(t *testing.T) {
	m := NewManager()
	list := m.List()

	// 至少包含三种默认资源
	found := map[ResourceType]bool{}
	for _, q := range list {
		found[q.Resource] = true
	}
	for _, rt := range []ResourceType{ResourceMetricsPerMinute, ResourceLogsPerMinute, ResourceAPIRequests} {
		if !found[rt] {
			t.Errorf("期望默认配额包含 %s", rt)
		}
	}
}

// TestQuotaManager_Increment_UnderLimit 未超限时 Increment 应返回 true
func TestQuotaManager_Increment_UnderLimit(t *testing.T) {
	m := NewManager()
	if !m.Increment(ResourceMetricsPerMinute, 1) {
		t.Error("期望 Increment 返回 true（未超限）")
	}
}

// TestQuotaManager_Increment_OverLimit 超出限制时 Increment 应返回 false
func TestQuotaManager_Increment_OverLimit(t *testing.T) {
	m := NewManager()
	m.UpdateLimit(ResourceMetricsPerMinute, 5)
	// 先消耗 5 个
	for i := 0; i < 5; i++ {
		m.Increment(ResourceMetricsPerMinute, 1)
	}
	// 第 6 次应被拒绝
	if m.Increment(ResourceMetricsPerMinute, 1) {
		t.Error("期望 Increment 返回 false（已超限）")
	}
}

// TestQuotaManager_Increment_ResetOnNewMinute 进入新分钟窗口后计数应重置
func TestQuotaManager_Increment_ResetOnNewMinute(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 30, 0, time.UTC)
	m := NewManager()
	m.SetNowFn(func() time.Time { return now })
	m.UpdateLimit(ResourceMetricsPerMinute, 5)

	// 消耗满当前分钟配额
	for i := 0; i < 5; i++ {
		m.Increment(ResourceMetricsPerMinute, 1)
	}
	if m.Increment(ResourceMetricsPerMinute, 1) {
		t.Error("期望超限后返回 false")
	}

	// 进入下一分钟
	now = now.Add(31 * time.Second) // 12:01:01
	m.SetNowFn(func() time.Time { return now })
	if !m.Increment(ResourceMetricsPerMinute, 1) {
		t.Error("期望新分钟后重置计数，Increment 返回 true")
	}
}

// TestQuotaManager_UpdateLimit 更新限额后生效
func TestQuotaManager_UpdateLimit(t *testing.T) {
	m := NewManager()
	m.UpdateLimit(ResourceMetricsPerMinute, 3)
	for i := 0; i < 3; i++ {
		if !m.Increment(ResourceMetricsPerMinute, 1) {
			t.Errorf("第 %d 次 Increment 期望 true", i+1)
		}
	}
	if m.Increment(ResourceMetricsPerMinute, 1) {
		t.Error("超限后期望 false")
	}
}

// TestQuotaAPI_ListQuotas GET /api/v1/quotas 应返回配额列表
func TestQuotaAPI_ListQuotas(t *testing.T) {
	m := NewManager()
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/quotas", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}

	var list []ResourceQuota
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if len(list) == 0 {
		t.Error("期望返回至少一个配额项")
	}
}

// TestQuotaAPI_UpdateLimit PUT /api/v1/quotas/{resource} 应更新限额
func TestQuotaAPI_UpdateLimit(t *testing.T) {
	m := NewManager()
	mux := http.NewServeMux()
	RegisterRoutes(mux, m)

	body := `{"limit":9999}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/quotas/metrics_per_minute",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("期望 204，实际 %d", rr.Code)
	}

	// 验证限额已更新
	list := m.List()
	for _, q := range list {
		if q.Resource == ResourceMetricsPerMinute && q.Limit != 9999 {
			t.Errorf("期望 limit=9999，实际 %d", q.Limit)
		}
	}
}
