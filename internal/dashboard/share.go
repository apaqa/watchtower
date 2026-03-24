package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ShareToken 描述一个可分享的仪表板链接
type ShareToken struct {
	Token     string `json:"token"`
	Label     string `json:"label"`               // 人类可读说明
	CreatedAt int64  `json:"created_at_ms"`
	ExpiresAt int64  `json:"expires_at_ms"`        // 0 = 永不过期
	PanelIDs  []string `json:"panel_ids,omitempty"` // 限定展示的面板；空 = 全部
}

// Expired 判断令牌是否已过期
func (s *ShareToken) Expired() bool {
	return s.ExpiresAt > 0 && time.Now().UnixMilli() > s.ExpiresAt
}

// ShareStore 管理所有已颁发的分享令牌
type ShareStore struct {
	mu     sync.RWMutex
	tokens map[string]*ShareToken // token → *ShareToken
}

// NewShareStore 创建空的 ShareStore
func NewShareStore() *ShareStore {
	return &ShareStore{tokens: make(map[string]*ShareToken)}
}

// Create 颁发新令牌；ttlSeconds <= 0 表示永不过期
func (ss *ShareStore) Create(label string, ttlSeconds int, panelIDs []string) (*ShareToken, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("生成令牌失败: %w", err)
	}
	tok := hex.EncodeToString(b)

	now := time.Now().UnixMilli()
	var exp int64
	if ttlSeconds > 0 {
		exp = now + int64(ttlSeconds)*1000
	}

	st := &ShareToken{
		Token:     tok,
		Label:     label,
		CreatedAt: now,
		ExpiresAt: exp,
		PanelIDs:  panelIDs,
	}

	ss.mu.Lock()
	ss.tokens[tok] = st
	ss.mu.Unlock()
	return st, nil
}

// Get 查找令牌（不检查过期）
func (ss *ShareStore) Get(token string) (*ShareToken, bool) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	st, ok := ss.tokens[token]
	return st, ok
}

// Delete 撤销令牌
func (ss *ShareStore) Delete(token string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	_, ok := ss.tokens[token]
	if ok {
		delete(ss.tokens, token)
	}
	return ok
}

// List 返回所有令牌（含已过期）
func (ss *ShareStore) List() []*ShareToken {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	result := make([]*ShareToken, 0, len(ss.tokens))
	for _, st := range ss.tokens {
		result = append(result, st)
	}
	return result
}

// ── HTTP API ──────────────────────────────────────────────────────────────────

// RegisterShareRoutes 注册分享相关路由
//
//   POST   /api/v1/dashboard/share           颁发新令牌
//   GET    /api/v1/dashboard/shares          列出所有令牌
//   DELETE /api/v1/dashboard/shares/{token}  撤销令牌
//   GET    /share/{token}                    只读分享视图（返回 HTML）
func RegisterShareRoutes(mux *http.ServeMux, ss *ShareStore) {
	mux.HandleFunc("/api/v1/dashboard/share", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"仅支持 POST"}`, http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Label      string   `json:"label"`
			TTLSeconds int      `json:"ttl_seconds"`
			PanelIDs   []string `json:"panel_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"JSON 解析失败"}`, http.StatusBadRequest)
			return
		}

		st, err := ss.Create(req.Label, req.TTLSeconds, req.PanelIDs)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(st)
	})

	mux.HandleFunc("/api/v1/dashboard/shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			list := ss.List()
			if list == nil {
				list = []*ShareToken{}
			}
			json.NewEncoder(w).Encode(list)
		default:
			http.Error(w, `{"error":"仅支持 GET"}`, http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/v1/dashboard/shares/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodDelete {
			http.Error(w, `{"error":"仅支持 DELETE"}`, http.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/api/v1/dashboard/shares/")
		if token == "" {
			http.Error(w, `{"error":"缺少 token"}`, http.StatusBadRequest)
			return
		}
		if !ss.Delete(token) {
			http.Error(w, `{"error":"令牌不存在"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// 只读分享视图：GET /share/{token}
	mux.HandleFunc("/share/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "仅支持 GET", http.StatusMethodNotAllowed)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/share/")
		if token == "" {
			http.Error(w, "缺少 token", http.StatusBadRequest)
			return
		}

		st, ok := ss.Get(token)
		if !ok {
			http.Error(w, "分享链接不存在", http.StatusNotFound)
			return
		}
		if st.Expired() {
			http.Error(w, "分享链接已过期", http.StatusGone)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, sharedDashboardHTML, st.Label, token, st.Token)
	})
}

// sharedDashboardHTML 是只读分享视图的 HTML 模板
// %s 参数：1=Label, 2=token（url用）, 3=token（JS用）
const sharedDashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>WatchTower 共享仪表板 – %s</title>
<style>
  body{font-family:sans-serif;margin:0;background:#0f172a;color:#e2e8f0}
  header{padding:1rem 2rem;background:#1e293b;border-bottom:1px solid #334155}
  h1{margin:0;font-size:1.2rem;color:#38bdf8}
  .badge{display:inline-block;padding:.2rem .6rem;border-radius:.25rem;
    font-size:.75rem;background:#0284c7;color:#fff;margin-left:.5rem}
  .expired{background:#dc2626}
  #metrics{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));
    gap:1rem;padding:1.5rem 2rem}
  .card{background:#1e293b;border-radius:.5rem;padding:1rem;
    border:1px solid #334155}
  .card-name{font-size:.8rem;color:#94a3b8;margin-bottom:.25rem}
  .card-val{font-size:1.5rem;font-weight:bold;color:#38bdf8}
  .note{padding:2rem;color:#64748b;text-align:center}
</style>
</head>
<body>
<header>
  <h1>WatchTower <span class="badge">只读共享</span></h1>
  <p style="margin:.25rem 0 0;color:#94a3b8;font-size:.9rem">%s</p>
</header>
<div id="metrics"><p class="note">正在加载指标…</p></div>
<script>
const TOKEN = %q;
async function load() {
  try {
    const r = await fetch('/api/v1/query?q=*');
    if (!r.ok) { document.getElementById('metrics').innerHTML='<p class="note">加载失败</p>'; return; }
    const data = await r.json();
    const pts = Array.isArray(data) ? data : (data.points || []);
    if (!pts.length) { document.getElementById('metrics').innerHTML='<p class="note">暂无数据</p>'; return; }
    const seen = {};
    pts.forEach(p => { if (!(p.name in seen) || p.timestamp > seen[p.name].timestamp) seen[p.name]=p; });
    document.getElementById('metrics').innerHTML = Object.values(seen).map(p=>
      '<div class="card"><div class="card-name">'+p.name+'</div><div class="card-val">'+p.value.toFixed(2)+'</div></div>'
    ).join('');
  } catch(e) { document.getElementById('metrics').innerHTML='<p class="note">加载出错</p>'; }
}
load();
setInterval(load, 30000);
</script>
</body>
</html>`
