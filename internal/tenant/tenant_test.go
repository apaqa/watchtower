package tenant

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTenantStore_DefaultExists 创建后 default 租户应存在
func TestTenantStore_DefaultExists(t *testing.T) {
	s := New()
	def := s.Get(DefaultTenantID)
	if def == nil {
		t.Fatal("期望 default 租户存在")
	}
	if def.MetricPrefix != "" {
		t.Errorf("default 租户 MetricPrefix 应为空，实际 %q", def.MetricPrefix)
	}
}

// TestTenantStore_CreateAndGet 创建租户后可按 ID 读取
func TestTenantStore_CreateAndGet(t *testing.T) {
	s := New()
	err := s.Create(Tenant{
		ID:           "acme",
		Name:         "Acme Corp",
		MetricPrefix: "acme.",
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	got := s.Get("acme")
	if got == nil || got.Name != "Acme Corp" {
		t.Errorf("期望 Acme Corp，实际 %v", got)
	}
}

// TestTenantStore_DuplicateCreate 重复创建同 ID 应返回错误
func TestTenantStore_DuplicateCreate(t *testing.T) {
	s := New()
	s.Create(Tenant{ID: "dup", Name: "Dup"})
	if err := s.Create(Tenant{ID: "dup", Name: "Dup2"}); err == nil {
		t.Error("期望重复创建返回错误")
	}
}

// TestTenantStore_Delete 删除后租户不可访问
func TestTenantStore_Delete(t *testing.T) {
	s := New()
	s.Create(Tenant{ID: "rm", Name: "Remove"})
	if err := s.Delete("rm"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if s.Get("rm") != nil {
		t.Error("删除后租户应不存在")
	}
}

// TestTenantStore_CannotDeleteDefault 默认租户不可删除
func TestTenantStore_CannotDeleteDefault(t *testing.T) {
	s := New()
	if err := s.Delete(DefaultTenantID); err == nil {
		t.Error("期望删除 default 返回错误")
	}
}

// TestApplyMetricPrefix 有前缀时应正确添加；无前缀时原样返回
func TestApplyMetricPrefix(t *testing.T) {
	names := []string{"cpu_usage", "memory_usage"}
	result := ApplyMetricPrefix("acme.", names)
	if result[0] != "acme.cpu_usage" || result[1] != "acme.memory_usage" {
		t.Errorf("期望添加前缀，实际 %v", result)
	}

	// 空前缀不修改
	orig := ApplyMetricPrefix("", names)
	if orig[0] != "cpu_usage" {
		t.Errorf("空前缀应原样返回，实际 %v", orig)
	}

	// 已有前缀不重复添加
	withPrefix := ApplyMetricPrefix("acme.", []string{"acme.cpu_usage"})
	if withPrefix[0] != "acme.cpu_usage" {
		t.Errorf("已有前缀不应重复，实际 %v", withPrefix)
	}
}

// TestTenantStore_GetByTenantID 按 TenantID 查找；未知 ID 返回 default
func TestTenantStore_GetByTenantID(t *testing.T) {
	s := New()
	s.Create(Tenant{ID: "t1", Name: "T1", MetricPrefix: "t1."})

	if got := s.GetByTenantID("t1"); got == nil || got.ID != "t1" {
		t.Errorf("期望找到 t1，实际 %v", got)
	}
	if got := s.GetByTenantID("nonexistent"); got == nil || got.ID != DefaultTenantID {
		t.Errorf("未知 ID 应返回 default，实际 %v", got)
	}
	if got := s.GetByTenantID(""); got == nil || got.ID != DefaultTenantID {
		t.Errorf("空 ID 应返回 default，实际 %v", got)
	}
}

// TestTenantAPI_CRUD REST CRUD 操作正确性
func TestTenantAPI_CRUD(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	// POST 创建
	body, _ := json.Marshal(Tenant{ID: "api_tenant", Name: "API Test", MetricPrefix: "api."})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Errorf("POST 期望 201，实际 %d: %s", rr.Code, rr.Body.String())
	}

	// GET 列表
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/tenants", nil)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	var list []Tenant
	json.NewDecoder(rr2.Body).Decode(&list)
	found := false
	for _, t2 := range list {
		if t2.ID == "api_tenant" {
			found = true
		}
	}
	if !found {
		t.Error("期望列表中包含 api_tenant")
	}

	// GET 单个
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/api_tenant", nil)
	rr3 := httptest.NewRecorder()
	mux.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Errorf("GET 期望 200，实际 %d", rr3.Code)
	}

	// DELETE
	req4 := httptest.NewRequest(http.MethodDelete, "/api/v1/tenants/api_tenant", nil)
	rr4 := httptest.NewRecorder()
	mux.ServeHTTP(rr4, req4)
	if rr4.Code != http.StatusNoContent {
		t.Errorf("DELETE 期望 204，实际 %d", rr4.Code)
	}
}

// TestTenantStore_GetForAPIKey API key 前缀匹配
func TestTenantStore_GetForAPIKey(t *testing.T) {
	s := New()
	s.Create(Tenant{ID: "co", Name: "Co", APIKeyPrefix: "co_", MetricPrefix: "co."})

	got := s.GetForAPIKey("co_user1")
	if got == nil || got.ID != "co" {
		t.Errorf("期望匹配 co 租户，实际 %v", got)
	}
	got2 := s.GetForAPIKey("other_user")
	if got2 == nil || got2.ID != DefaultTenantID {
		t.Errorf("期望匹配 default，实际 %v", got2)
	}
}
