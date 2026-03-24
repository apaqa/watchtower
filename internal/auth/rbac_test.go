package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRBAC_AdminCanDoAnything admin 角色对所有路径和方法均返回 true
func TestRBAC_AdminCanDoAnything(t *testing.T) {
	paths := []string{
		"/api/v1/metrics",
		"/api/v1/auth/keys",
		"/api/v1/admin/status",
		"/api/v1/tenants",
		"/api/v1/audit",
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}
	for _, p := range paths {
		for _, m := range methods {
			if !checkRBACRole(RoleAdmin, m, p) {
				t.Errorf("admin 应允许 %s %s", m, p)
			}
		}
	}
}

// TestRBAC_OperatorCannotManageKeys operator 不能访问密钥管理路径
func TestRBAC_OperatorCannotManageKeys(t *testing.T) {
	restricted := []string{
		"/api/v1/auth/keys",
		"/api/v1/admin/status",
		"/api/v1/tenants",
		"/api/v1/audit",
	}
	for _, p := range restricted {
		if checkRBACRole(RoleOperator, http.MethodPost, p) {
			t.Errorf("operator 不应被允许访问 %s", p)
		}
	}
}

// TestRBAC_OperatorCanWriteData operator 可以写入数据（告警、探针等）
func TestRBAC_OperatorCanWriteData(t *testing.T) {
	allowed := []string{
		"/api/v1/metrics",
		"/api/v1/alerts/rules",
		"/api/v1/incidents",
		"/api/v1/probes",
	}
	for _, p := range allowed {
		if !checkRBACRole(RoleOperator, http.MethodPost, p) {
			t.Errorf("operator 应允许 POST %s", p)
		}
	}
}

// TestRBAC_ViewerReadOnly viewer 只能 GET，不能写
func TestRBAC_ViewerReadOnly(t *testing.T) {
	// GET 应允许
	if !checkRBACRole(RoleViewer, http.MethodGet, "/api/v1/metrics") {
		t.Error("viewer 应允许 GET /api/v1/metrics")
	}
	// POST/PUT/DELETE 应拒绝
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if checkRBACRole(RoleViewer, m, "/api/v1/metrics") {
			t.Errorf("viewer 不应被允许 %s /api/v1/metrics", m)
		}
	}
}

// TestRBAC_Middleware_ForbidViewer RBAC 中间件在 viewer 尝试写时应返回 403
func TestRBAC_Middleware_ForbidViewer(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{
		Key:         "viewerkey",
		Name:        "alice",
		Permissions: []string{PermRead},
		Role:        RoleViewer,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := Middleware(ks, mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", nil)
	req.Header.Set("X-API-Key", "viewerkey")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer POST 期望 403，实际 %d", rr.Code)
	}
}

// TestRBAC_Middleware_AllowViewer_GET viewer GET 请求应通过
func TestRBAC_Middleware_AllowViewer_GET(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{
		Key:         "viewerkey2",
		Name:        "bob",
		Permissions: []string{PermRead},
		Role:        RoleViewer,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := Middleware(ks, mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/query?q=cpu", nil)
	req.Header.Set("X-API-Key", "viewerkey2")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("viewer GET 期望 200，实际 %d", rr.Code)
	}
}

// TestGetKeyName_FromContext context 中有 APIKey 时应返回正确名称
func TestGetKeyName_FromContext(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{
		Key:         "ctx_key",
		Name:        "ctx-user",
		Permissions: []string{PermRead},
	})

	var captured string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		captured = GetKeyName(r)
		w.WriteHeader(http.StatusOK)
	})

	handler := Middleware(ks, mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)
	req.Header.Set("X-API-Key", "ctx_key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if captured != "ctx-user" {
		t.Errorf("期望 ctx-user，实际 %q", captured)
	}
}
