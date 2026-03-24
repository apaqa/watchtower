// Package audit 提供不可变的操作审计日志功能
package audit

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/auth"
)

const maxEntries = 5000 // 环形缓冲区最大容量

// Action 描述被审计的操作类型
type Action string

const (
	ActionCreate Action = "create"
	ActionRead   Action = "read"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

// ResourceType 描述被操作的资源类型
type ResourceType string

const (
	ResourceAlertRule ResourceType = "alert_rule"
	ResourceProbe     ResourceType = "probe"
	ResourceIncident  ResourceType = "incident"
	ResourceConfig    ResourceType = "config"
	ResourceAPIKey    ResourceType = "api_key"
	ResourceTenant    ResourceType = "tenant"
	ResourceGeneric   ResourceType = "generic"
)

// Status 描述操作结果
type Status string

const (
	StatusSuccess Status = "success"
	StatusDenied  Status = "denied"
)

// AuditEntry 是一条不可变的审计记录
type AuditEntry struct {
	Timestamp    int64        `json:"timestamp_ms"`
	User         string       `json:"user"`          // API key 名称或 "anonymous"
	Action       Action       `json:"action"`
	ResourceType ResourceType `json:"resource_type"`
	ResourceID   string       `json:"resource_id"`
	Details      string       `json:"details"`
	IPAddress    string       `json:"ip_address"`
	Status       Status       `json:"status"`
}

// Store 是线程安全的审计日志环形缓冲区（最多保存 maxEntries 条）
type Store struct {
	mu      sync.RWMutex
	entries [maxEntries]AuditEntry
	head    int // 下一次写入的位置
	count   int // 已写入总条数（不超过 maxEntries）
}

// New 创建一个新的审计日志存储
func New() *Store {
	return &Store{}
}

// Log 向存储中追加一条审计记录
func (s *Store) Log(e AuditEntry) {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	s.mu.Lock()
	s.entries[s.head%maxEntries] = e
	s.head++
	if s.count < maxEntries {
		s.count++
	}
	s.mu.Unlock()
}

// FilterOptions 描述查询审计日志的过滤条件
type FilterOptions struct {
	User         string
	Action       Action
	ResourceType ResourceType
	StartMS      int64
	EndMS        int64
	Limit        int // 0 = 不限制
}

// List 按过滤条件返回审计日志（最新的在前）
func (s *Store) List(opts FilterOptions) []AuditEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.count == 0 {
		return []AuditEntry{}
	}

	// 从最新条目倒序迭代
	results := make([]AuditEntry, 0, s.count)
	for i := 0; i < s.count; i++ {
		idx := (s.head - 1 - i + maxEntries*2) % maxEntries
		e := s.entries[idx]

		if opts.User != "" && e.User != opts.User {
			continue
		}
		if opts.Action != "" && e.Action != opts.Action {
			continue
		}
		if opts.ResourceType != "" && e.ResourceType != opts.ResourceType {
			continue
		}
		if opts.StartMS > 0 && e.Timestamp < opts.StartMS {
			continue
		}
		if opts.EndMS > 0 && e.Timestamp > opts.EndMS {
			continue
		}
		results = append(results, e)
		if opts.Limit > 0 && len(results) >= opts.Limit {
			break
		}
	}
	return results
}

// ── HTTP 中间件 ───────────────────────────────────────────────────────────────

// httpMethodToAction 将 HTTP 方法映射为审计 Action
func httpMethodToAction(method string) (Action, bool) {
	switch method {
	case http.MethodPost:
		return ActionCreate, true
	case http.MethodPut, http.MethodPatch:
		return ActionUpdate, true
	case http.MethodDelete:
		return ActionDelete, true
	default:
		return "", false // GET/HEAD/OPTIONS 不记录（只记录写操作）
	}
}

// pathToResourceType 根据 URL 路径推断资源类型
func pathToResourceType(path string) ResourceType {
	switch {
	case strings.Contains(path, "/auth/keys"):
		return ResourceAPIKey
	case strings.Contains(path, "/alerts"):
		return ResourceAlertRule
	case strings.Contains(path, "/probes"):
		return ResourceProbe
	case strings.Contains(path, "/incidents"):
		return ResourceIncident
	case strings.Contains(path, "/admin"):
		return ResourceConfig
	case strings.Contains(path, "/tenants"):
		return ResourceTenant
	default:
		return ResourceGeneric
	}
}

// extractIP 从请求中提取客户端 IP（优先 X-Forwarded-For）
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// responseRecorder 包装 ResponseWriter 以捕获状态码
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

// Middleware 返回一个 HTTP 中间件，自动记录所有写操作（POST/PUT/DELETE/PATCH）
func Middleware(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action, shouldLog := httpMethodToAction(r.Method)
		if !shouldLog || store == nil {
			next.ServeHTTP(w, r)
			return
		}

		rec := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)

		status := StatusSuccess
		if rec.statusCode == http.StatusForbidden || rec.statusCode == http.StatusUnauthorized {
			status = StatusDenied
		}

		store.Log(AuditEntry{
			User:         auth.GetKeyName(r),
			Action:       action,
			ResourceType: pathToResourceType(r.URL.Path),
			ResourceID:   extractResourceID(r.URL.Path),
			Details:      r.Method + " " + r.URL.Path,
			IPAddress:    extractIP(r),
			Status:       status,
		})
	})
}

// extractResourceID 从 URL 路径中提取资源 ID（路径最后一段，若非 API 动词）
func extractResourceID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	// 跳过已知的动词后缀
	verbs := map[string]bool{"start": true, "stop": true, "resolve": true, "comment": true}
	if verbs[last] && len(parts) > 1 {
		return parts[len(parts)-2]
	}
	return last
}

// ── HTTP API 路由 ─────────────────────────────────────────────────────────────

// RegisterRoutes 注册审计日志 API 路由（仅 admin 可访问，由 auth 中间件保证）
func RegisterRoutes(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("/api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"仅支持 GET 方法"}`, http.StatusMethodNotAllowed)
			return
		}

		q := r.URL.Query()
		opts := FilterOptions{
			User:         q.Get("user"),
			ResourceType: ResourceType(q.Get("resource_type")),
			Action:       Action(q.Get("action")),
		}
		if v := q.Get("limit"); v != "" {
			var body struct{ N int }
			if err := json.NewDecoder(strings.NewReader(`{"N":` + v + `}`)).Decode(&body); err == nil {
				opts.Limit = body.N
			}
		}
		// 默认限制 500 条（API 响应大小保护）
		if opts.Limit <= 0 || opts.Limit > 500 {
			opts.Limit = 500
		}

		entries := store.List(opts)
		json.NewEncoder(w).Encode(entries)
	})
}
