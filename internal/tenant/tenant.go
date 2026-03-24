// Package tenant 提供多租户隔离：指标前缀、配额覆盖和租户管理 API
package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/apaqa/watchtower/internal/auth"
)

// DefaultTenantID 是默认租户的标识符（向后兼容，无前缀）
const DefaultTenantID = "default"

// Tenant 描述一个隔离租户
type Tenant struct {
	ID             string            `json:"id"`             // 唯一标识符
	Name           string            `json:"name"`           // 可读名称
	APIKeyPrefix   string            `json:"api_key_prefix"` // API key 名称前缀（用于归属判断）
	MetricPrefix   string            `json:"metric_prefix"`  // 写入指标时自动添加的前缀；default 租户为空
	QuotaOverrides map[string]int64  `json:"quota_overrides,omitempty"` // 资源配额覆盖
}

// tenantContextKey 是存储当前租户信息的 context 键类型
type tenantContextKey struct{}

// TenantContextKey 供中间件和处理函数读取租户信息
var TenantContextKey = tenantContextKey{}

// Store 是租户的内存存储，支持 CRUD
type Store struct {
	mu      sync.RWMutex
	tenants map[string]*Tenant // id → Tenant
}

// New 创建一个新的 TenantStore，并预置 "default" 租户
func New() *Store {
	s := &Store{
		tenants: make(map[string]*Tenant),
	}
	// 默认租户始终存在，无指标前缀（向后兼容）
	s.tenants[DefaultTenantID] = &Tenant{
		ID:           DefaultTenantID,
		Name:         "Default",
		MetricPrefix: "",
	}
	return s
}

// Create 创建一个新租户；若 ID 已存在则返回错误
func (s *Store) Create(t Tenant) error {
	if t.ID == "" {
		return fmt.Errorf("租户 ID 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[t.ID]; exists {
		return fmt.Errorf("租户 %q 已存在", t.ID)
	}
	cp := t
	s.tenants[t.ID] = &cp
	return nil
}

// Get 按 ID 返回租户（不存在时返回 nil）
func (s *Store) Get(id string) *Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tenants[id]
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

// Delete 删除租户；不允许删除 default 租户
func (s *Store) Delete(id string) error {
	if id == DefaultTenantID {
		return fmt.Errorf("不能删除默认租户")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[id]; !exists {
		return fmt.Errorf("租户 %q 不存在", id)
	}
	delete(s.tenants, id)
	return nil
}

// List 返回所有租户的快照
func (s *Store) List() []Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		result = append(result, *t)
	}
	return result
}

// GetForAPIKey 根据 API key 名称查找对应租户；未匹配时返回 default 租户
func (s *Store) GetForAPIKey(keyName string) *Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 精确匹配 TenantID（已由 auth 中间件注入 context）
	// 此处用 api_key_prefix 做前缀匹配
	for _, t := range s.tenants {
		if t.ID == DefaultTenantID {
			continue
		}
		if t.APIKeyPrefix != "" && strings.HasPrefix(keyName, t.APIKeyPrefix) {
			cp := *t
			return &cp
		}
	}
	return s.tenants[DefaultTenantID]
}

// GetByTenantID 根据租户 ID（来自 APIKey.TenantID）返回租户；未找到时返回 default
func (s *Store) GetByTenantID(tenantID string) *Tenant {
	if tenantID == "" {
		tenantID = DefaultTenantID
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantID]
	if !ok {
		return s.tenants[DefaultTenantID]
	}
	cp := *t
	return &cp
}

// ── Middleware ────────────────────────────────────────────────────────────────

// Middleware 从已认证的 APIKey 中提取租户信息并注入 request context
func Middleware(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := auth.GetTenantID(r)
		tenant := store.GetByTenantID(tenantID)
		ctx := context.WithValue(r.Context(), TenantContextKey, tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetTenant 从 request context 中获取当前租户；未设置时返回 nil
func GetTenant(r *http.Request) *Tenant {
	t, _ := r.Context().Value(TenantContextKey).(*Tenant)
	return t
}

// ApplyMetricPrefix 为切片中每个指标名称加上租户前缀（默认租户不加前缀）
func ApplyMetricPrefix(prefix string, names []string) []string {
	if prefix == "" {
		return names
	}
	result := make([]string, len(names))
	for i, n := range names {
		if strings.HasPrefix(n, prefix) {
			result[i] = n // 已有前缀，不重复添加
		} else {
			result[i] = prefix + n
		}
	}
	return result
}

// ── HTTP API 路由 ─────────────────────────────────────────────────────────────

// RegisterRoutes 注册租户管理 API 路由（由 auth 中间件保证 admin-only）
func RegisterRoutes(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("/api/v1/tenants", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(store.List())
		case http.MethodPost:
			var t Tenant
			if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
				http.Error(w, `{"error":"JSON 解析失败"}`, http.StatusBadRequest)
				return
			}
			if err := store.Create(t); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(t)
		default:
			http.Error(w, `{"error":"不支持的方法"}`, http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v1/tenants/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/")
		if id == "" {
			http.Error(w, `{"error":"缺少租户 ID"}`, http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			t := store.Get(id)
			if t == nil {
				http.Error(w, `{"error":"租户不存在"}`, http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(t)
		case http.MethodDelete:
			if err := store.Delete(id); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, `{"error":"不支持的方法"}`, http.StatusMethodNotAllowed)
		}
	})
}
