// Package auth 提供 API 密钥认证：密钥存储、中间件和权限检查
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 权限常量
const (
	PermRead  = "read"  // 可读取指标、日志、告警等数据
	PermWrite = "write" // 可写入指标、日志、追踪数据
	PermAdmin = "admin" // 可管理 API 密钥（隐含 read + write 权限）
)

// adminOnlyPaths 仅 admin 权限可访问的路径前缀
var adminOnlyPaths = []string{"/api/v1/auth/keys"}

// publicPaths 无需认证的路径前缀或精确路径（/metrics = Prometheus 抓取端点）
var publicPaths = []string{"/metrics"}

// APIKey 描述一个 API 密钥及其元数据
type APIKey struct {
	Key         string   `json:"-"`           // 实际密钥值，序列化时隐藏
	Name        string   `json:"name"`        // 人类可读标识
	Permissions []string `json:"permissions"` // "read" | "write" | "admin"
	CreatedAt   int64    `json:"created_at"`  // Unix 毫秒
	LastUsedAt  int64    `json:"last_used_at"` // Unix 毫秒；0 表示从未使用
}

// MaskedKey 是 APIKey 的可安全序列化版本（仅显示末 4 位密钥）
type MaskedKey struct {
	Name        string   `json:"name"`
	Suffix      string   `json:"suffix"`      // 密钥末 4 位
	Permissions []string `json:"permissions"`
	CreatedAt   int64    `json:"created_at"`
	LastUsedAt  int64    `json:"last_used_at"`
}

// HasPermission 检查密钥是否持有指定权限；admin 权限隐含所有其他权限
func (k *APIKey) HasPermission(perm string) bool {
	for _, p := range k.Permissions {
		if p == PermAdmin || p == perm {
			return true
		}
	}
	return false
}

// KeyStore 是线程安全的 API 密钥内存存储
type KeyStore struct {
	mu     sync.RWMutex
	byKey  map[string]*APIKey // 密钥值 → APIKey
	byName map[string]*APIKey // 名称 → APIKey（便于按名称删除）
}

// NewKeyStore 创建空的 KeyStore
func NewKeyStore() *KeyStore {
	return &KeyStore{
		byKey:  make(map[string]*APIKey),
		byName: make(map[string]*APIKey),
	}
}

// Add 添加一个密钥；若名称已存在则替换
func (ks *KeyStore) Add(k APIKey) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	// 删除同名旧密钥
	if old, ok := ks.byName[k.Name]; ok {
		delete(ks.byKey, old.Key)
	}
	cp := k
	ks.byKey[k.Key] = &cp
	ks.byName[k.Name] = &cp
}

// Validate 检查密钥是否有效；若有效返回 (key, true)，否则返回 (nil, false)
func (ks *KeyStore) Validate(key string) (*APIKey, bool) {
	ks.mu.RLock()
	k, ok := ks.byKey[key]
	ks.mu.RUnlock()
	return k, ok
}

// UpdateLastUsed 更新密钥的最后使用时间（非阻塞写）
func (ks *KeyStore) UpdateLastUsed(key string) {
	ks.mu.Lock()
	if k, ok := ks.byKey[key]; ok {
		k.LastUsedAt = time.Now().UnixMilli()
	}
	ks.mu.Unlock()
}

// Delete 按名称删除密钥；若密钥不存在返回 false
func (ks *KeyStore) Delete(name string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k, ok := ks.byName[name]
	if !ok {
		return false
	}
	delete(ks.byKey, k.Key)
	delete(ks.byName, name)
	return true
}

// List 返回所有密钥的脱敏列表（末 4 位 + 元数据）
func (ks *KeyStore) List() []MaskedKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]MaskedKey, 0, len(ks.byName))
	for _, k := range ks.byName {
		suffix := k.Key
		if len(suffix) > 4 {
			suffix = suffix[len(suffix)-4:]
		}
		out = append(out, MaskedKey{
			Name:        k.Name,
			Suffix:      suffix,
			Permissions: k.Permissions,
			CreatedAt:   k.CreatedAt,
			LastUsedAt:  k.LastUsedAt,
		})
	}
	return out
}

// Count 返回当前密钥数量
func (ks *KeyStore) Count() int {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return len(ks.byKey)
}

// GenerateKey 生成一个随机的 32 字节十六进制 API 密钥（共 64 位字符）
func GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Middleware 返回一个 HTTP 中间件，对 /api/ 路径执行密钥认证。
//
// 行为规则：
//   - /metrics 及所有非 /api/ 路径：始终公开访问
//   - 若 KeyStore 为空（无密钥配置）：开放模式，允许所有请求
//   - 否则：要求请求头 X-API-Key 携带有效密钥
//   - adminOnlyPaths 下的路径需要 admin 权限
func Middleware(ks *KeyStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS 预检请求直接放行
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// 公开路径：不需要认证
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// 非 /api/ 路径（如仪表板静态文件、WebSocket）：始终公开
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// 开放模式：未配置任何密钥
		if ks == nil || ks.Count() == 0 {
			next.ServeHTTP(w, r)
			return
		}

		// 提取密钥（请求头优先，其次查询参数）
		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.URL.Query().Get("api_key")
		}

		apiKey, ok := ks.Validate(key)
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid or missing API key"})
			return
		}

		// 检查 admin-only 路径权限
		if isAdminPath(r.URL.Path) && !apiKey.HasPermission(PermAdmin) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "admin permission required"})
			return
		}

		ks.UpdateLastUsed(key)
		next.ServeHTTP(w, r)
	})
}

func isPublicPath(path string) bool {
	for _, p := range publicPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func isAdminPath(path string) bool {
	for _, p := range adminOnlyPaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
