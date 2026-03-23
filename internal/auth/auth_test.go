// Package auth — 单元测试：密钥存储、中间件和 HTTP API
package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── KeyStore 测试 ─────────────────────────────────────────────────────────────

// TestAddAndValidateKey 验证添加密钥后可以通过 Validate 找到它
func TestAddAndValidateKey(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "secret123", Name: "test", Permissions: []string{PermRead}})

	k, ok := ks.Validate("secret123")
	if !ok {
		t.Fatal("Validate 应返回 true")
	}
	if k.Name != "test" {
		t.Errorf("期望名称 test，得到 %s", k.Name)
	}
}

// TestValidateInvalidKey 验证无效密钥返回 false
func TestValidateInvalidKey(t *testing.T) {
	ks := NewKeyStore()
	_, ok := ks.Validate("nonexistent")
	if ok {
		t.Error("无效密钥应返回 false")
	}
}

// TestDeleteKey 验证按名称删除密钥后无法再验证
func TestDeleteKey(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "todelete", Name: "remove-me", Permissions: []string{PermWrite}})

	if !ks.Delete("remove-me") {
		t.Fatal("Delete 应返回 true")
	}
	_, ok := ks.Validate("todelete")
	if ok {
		t.Error("已删除密钥应无法验证")
	}
	// 二次删除应返回 false
	if ks.Delete("remove-me") {
		t.Error("二次删除应返回 false")
	}
}

// TestHasPermission 验证权限检查逻辑：admin 隐含所有权限
func TestHasPermission(t *testing.T) {
	readKey  := &APIKey{Permissions: []string{PermRead}}
	writeKey := &APIKey{Permissions: []string{PermWrite}}
	adminKey := &APIKey{Permissions: []string{PermAdmin}}

	if !readKey.HasPermission(PermRead)   { t.Error("read key 应有 read 权限") }
	if  readKey.HasPermission(PermWrite)  { t.Error("read key 不应有 write 权限") }
	if !adminKey.HasPermission(PermRead)  { t.Error("admin key 应有 read 权限") }
	if !adminKey.HasPermission(PermWrite) { t.Error("admin key 应有 write 权限") }
	if !adminKey.HasPermission(PermAdmin) { t.Error("admin key 应有 admin 权限") }
	if  writeKey.HasPermission(PermAdmin) { t.Error("write key 不应有 admin 权限") }
}

// TestListKeysMasked 验证列出密钥时只暴露末 4 位
func TestListKeysMasked(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "abcdefgh12345678", Name: "k1", Permissions: []string{PermRead}})

	list := ks.List()
	if len(list) != 1 {
		t.Fatalf("期望 1 个密钥，得到 %d", len(list))
	}
	if list[0].Suffix != "5678" {
		t.Errorf("期望末 4 位 5678，得到 %s", list[0].Suffix)
	}
}

// TestUpdateLastUsed 验证 UpdateLastUsed 会刷新 last_used_at
func TestUpdateLastUsed(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "mykey", Name: "k", Permissions: []string{PermRead}})

	k, _ := ks.Validate("mykey")
	if k.LastUsedAt != 0 {
		t.Error("初始 LastUsedAt 应为 0")
	}

	before := time.Now().UnixMilli()
	ks.UpdateLastUsed("mykey")
	k, _ = ks.Validate("mykey")
	if k.LastUsedAt < before {
		t.Error("UpdateLastUsed 未刷新 LastUsedAt")
	}
}

// TestGenerateKey 验证生成的密钥为 64 位十六进制字符串
func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey 失败: %v", err)
	}
	if len(key) != 64 {
		t.Errorf("期望 64 位，得到 %d 位", len(key))
	}
	// 验证是合法十六进制字符串
	for _, c := range key {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			t.Errorf("密钥包含非十六进制字符: %c", c)
		}
	}
}

// ── 中间件测试 ─────────────────────────────────────────────────────────────────

// okHandler 是一个简单的 HTTP 处理器，始终返回 200
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestMiddlewareOpenMode 验证无密钥时（开放模式）所有请求均可访问
func TestMiddlewareOpenMode(t *testing.T) {
	ks := NewKeyStore() // 空密钥库 = 开放模式
	h := Middleware(ks, okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("开放模式应返回 200，得到 %d", w.Code)
	}
}

// TestMiddlewareBlocksWithoutKey 验证有密钥配置时，缺少 X-API-Key 返回 401
func TestMiddlewareBlocksWithoutKey(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "valid", Name: "k", Permissions: []string{PermRead}})
	h := Middleware(ks, okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("缺少密钥应返回 401，得到 %d", w.Code)
	}
}

// TestMiddlewareAllowsWithValidKey 验证携带有效 X-API-Key 的请求被放行
func TestMiddlewareAllowsWithValidKey(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "goodkey", Name: "k", Permissions: []string{PermRead}})
	h := Middleware(ks, okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/query", nil)
	req.Header.Set("X-API-Key", "goodkey")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("有效密钥应返回 200，得到 %d", w.Code)
	}
}

// TestMiddlewarePublicPaths 验证 /metrics 等公开路径无需密钥
func TestMiddlewarePublicPaths(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "key", Name: "k", Permissions: []string{PermRead}})
	h := Middleware(ks, okHandler)

	for _, path := range []string{"/metrics", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		// 不设置 X-API-Key
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("路径 %s 应公开，得到 %d", path, w.Code)
		}
	}
}

// TestMiddlewareAdminOnly 验证 /api/v1/auth/keys 仅 admin 可访问
func TestMiddlewareAdminOnly(t *testing.T) {
	ks := NewKeyStore()
	ks.Add(APIKey{Key: "readkey", Name: "reader", Permissions: []string{PermRead}})
	ks.Add(APIKey{Key: "adminkey", Name: "admin", Permissions: []string{PermAdmin}})
	h := Middleware(ks, okHandler)

	// 非 admin 密钥应被拒绝
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/keys", nil)
	req.Header.Set("X-API-Key", "readkey")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("非 admin 访问 /api/v1/auth/keys 应返回 403，得到 %d", w.Code)
	}

	// admin 密钥应放行
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/keys", nil)
	req2.Header.Set("X-API-Key", "adminkey")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("admin 访问 /api/v1/auth/keys 应返回 200，得到 %d", w2.Code)
	}
}

// ── HTTP API 测试 ──────────────────────────────────────────────────────────────

func newTestMux(t *testing.T) (*KeyStore, *http.ServeMux) {
	t.Helper()
	ks := NewKeyStore()
	mux := http.NewServeMux()
	RegisterRoutes(mux, ks)
	return ks, mux
}

// TestAPICreateKey 验证 POST /api/v1/auth/keys 创建密钥并返回明文
func TestAPICreateKey(t *testing.T) {
	_, mux := newTestMux(t)

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "mykey",
		"permissions": []string{"read", "write"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("期望 201，得到 %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp["key"] == "" || resp["key"] == nil {
		t.Error("响应应包含明文密钥")
	}
}

// TestAPIListKeys 验证 GET /api/v1/auth/keys 返回脱敏列表
func TestAPIListKeys(t *testing.T) {
	ks, mux := newTestMux(t)
	ks.Add(APIKey{Key: "abcd1234efgh5678", Name: "listed", Permissions: []string{PermRead}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/keys", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", w.Code)
	}
	var keys []MaskedKey
	if err := json.NewDecoder(w.Body).Decode(&keys); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("期望 1 个密钥，得到 %d", len(keys))
	}
	if keys[0].Suffix != "5678" {
		t.Errorf("末 4 位应为 5678，得到 %s", keys[0].Suffix)
	}
}

// TestAPIDeleteKey 验证 DELETE /api/v1/auth/keys/{name} 吊销密钥
func TestAPIDeleteKey(t *testing.T) {
	ks, mux := newTestMux(t)
	ks.Add(APIKey{Key: "tokill", Name: "victim", Permissions: []string{PermRead}})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/keys/victim", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("期望 204，得到 %d", w.Code)
	}
	if _, ok := ks.Validate("tokill"); ok {
		t.Error("密钥应已被删除")
	}
}

// TestAPIDeleteKeyNotFound 验证删除不存在的密钥返回 404
func TestAPIDeleteKeyNotFound(t *testing.T) {
	_, mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/keys/nobody", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("期望 404，得到 %d", w.Code)
	}
}
