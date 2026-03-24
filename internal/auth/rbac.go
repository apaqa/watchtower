// RBAC — 基于角色的访问控制
package auth

import (
	"net/http"
	"strings"
)

// 角色常量
const (
	RoleAdmin    = "admin"    // 完全权限：可管理密钥、配置、租户
	RoleOperator = "operator" // 操作权限：可创建/修改告警、探针、事故，不能管理密钥/配置
	RoleViewer   = "viewer"   // 只读权限：仅 GET 请求
)

// rbacAdminOnlyPaths 只有 admin 角色才能访问的路径前缀
var rbacAdminOnlyPaths = []string{
	"/api/v1/auth/keys",
	"/api/v1/admin/",
	"/api/v1/tenants",
	"/api/v1/audit",
}

// checkRBACRole 根据角色、HTTP 方法和路径判断是否允许请求
func checkRBACRole(role, method, path string) bool {
	if role == RoleAdmin {
		return true // admin 可以做任何事
	}

	// 检查 admin-only 路径
	for _, p := range rbacAdminOnlyPaths {
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p) {
			return false // 非 admin 不可访问
		}
	}

	// viewer 只能 GET
	if role == RoleViewer {
		return method == http.MethodGet || method == http.MethodOptions || method == http.MethodHead
	}

	// operator 可以执行所有非 admin 路径的操作
	return true
}

// GetRole 从请求 context 中获取当前用户的角色；若无认证信息返回空字符串
func GetRole(r *http.Request) string {
	apiKey, _ := r.Context().Value(AuthContextKey).(*APIKey)
	if apiKey == nil {
		return ""
	}
	return apiKey.Role
}

// GetKeyName 从请求 context 中获取当前 API key 名称（用于审计日志）
func GetKeyName(r *http.Request) string {
	apiKey, _ := r.Context().Value(AuthContextKey).(*APIKey)
	if apiKey == nil {
		return "anonymous"
	}
	return apiKey.Name
}

// GetTenantID 从请求 context 中获取租户 ID；未设置时返回 "default"
func GetTenantID(r *http.Request) string {
	apiKey, _ := r.Context().Value(AuthContextKey).(*APIKey)
	if apiKey == nil || apiKey.TenantID == "" {
		return "default"
	}
	return apiKey.TenantID
}
