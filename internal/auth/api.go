// Package auth — API 密钥管理的 HTTP 端点
package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// RegisterRoutes 将 API 密钥管理路由注册到 mux。
//
//	POST   /api/v1/auth/keys        — 创建新密钥（需要 admin 权限，或开放模式）
//	GET    /api/v1/auth/keys        — 列出所有密钥（脱敏）
//	DELETE /api/v1/auth/keys/{name} — 吊销密钥
func RegisterRoutes(mux *http.ServeMux, ks *KeyStore) {
	mux.HandleFunc("/api/v1/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		handleKeys(w, r, ks)
	})
	mux.HandleFunc("/api/v1/auth/keys/", func(w http.ResponseWriter, r *http.Request) {
		handleKeyByName(w, r, ks)
	})
}

func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleKeys 处理 POST（创建）和 GET（列出）请求
func handleKeys(w http.ResponseWriter, r *http.Request, ks *KeyStore) {
	cors(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// 返回脱敏密钥列表
		keys := ks.List()
		if keys == nil {
			keys = []MaskedKey{}
		}
		json.NewEncoder(w).Encode(keys)

	case http.MethodPost:
		// 解析请求体
		var req struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonErr(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			jsonErr(w, "name is required", http.StatusBadRequest)
			return
		}
		if len(req.Permissions) == 0 {
			req.Permissions = []string{PermRead, PermWrite}
		}

		// 生成随机密钥
		rawKey, err := GenerateKey()
		if err != nil {
			jsonErr(w, "failed to generate key", http.StatusInternalServerError)
			return
		}

		k := APIKey{
			Key:         rawKey,
			Name:        req.Name,
			Permissions: req.Permissions,
			CreatedAt:   time.Now().UnixMilli(),
		}
		ks.Add(k)

		// 仅在创建时返回明文密钥（之后无法再查看）
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":        k.Name,
			"key":         rawKey, // 仅此一次暴露明文
			"permissions": k.Permissions,
			"created_at":  k.CreatedAt,
		})

	default:
		jsonErr(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleKeyByName 处理 DELETE /api/v1/auth/keys/{name}
func handleKeyByName(w http.ResponseWriter, r *http.Request, ks *KeyStore) {
	cors(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/keys/")
	if name == "" {
		jsonErr(w, "key name required in path", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if !ks.Delete(name) {
			jsonErr(w, "key not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		jsonErr(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
