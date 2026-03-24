// Package savedquery 提供 WQL 已保存查询的管理能力。
package savedquery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// SavedQuery 表示用户保存的 WQL 查询。
type SavedQuery struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	WQLExpression string `json:"wql_expression"`
	Description   string `json:"description"`
	CreatedBy     string `json:"created_by"`
	CreatedAt     int64  `json:"created_at"`
	IsFavorite    bool   `json:"is_favorite"`
}

// Store 保存查询定义。
type Store struct {
	mu      sync.RWMutex
	nextID  int64
	queries map[string]*SavedQuery
}

// NewStore 创建新的查询存储。
func NewStore() *Store {
	return &Store{
		nextID:  1,
		queries: make(map[string]*SavedQuery),
	}
}

// Save 新建一个已保存查询。
func (s *Store) Save(query SavedQuery) (SavedQuery, error) {
	if strings.TrimSpace(query.Name) == "" {
		return SavedQuery{}, fmt.Errorf("query name is required")
	}
	if strings.TrimSpace(query.WQLExpression) == "" {
		return SavedQuery{}, fmt.Errorf("wql_expression is required")
	}
	if strings.TrimSpace(query.CreatedBy) == "" {
		query.CreatedBy = "unknown"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	query.ID = fmt.Sprintf("query-%d", s.nextID)
	query.CreatedAt = time.Now().UnixMilli()
	s.nextID++

	cp := query
	s.queries[query.ID] = &cp
	return cp, nil
}

// List 返回所有查询，收藏项会固定在前面。
func (s *Store) List() []SavedQuery {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]SavedQuery, 0, len(s.queries))
	for _, query := range s.queries {
		out = append(out, *query)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsFavorite != out[j].IsFavorite {
			return out[i].IsFavorite
		}
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Delete 删除查询。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.queries[id]; !ok {
		return fmt.Errorf("saved query %q not found", id)
	}
	delete(s.queries, id)
	return nil
}

// ToggleFavorite 切换收藏状态。
func (s *Store) ToggleFavorite(id string) (SavedQuery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query, ok := s.queries[id]
	if !ok {
		return SavedQuery{}, fmt.Errorf("saved query %q not found", id)
	}
	query.IsFavorite = !query.IsFavorite
	return *query, nil
}

// RegisterRoutes 注册保存查询 API 路由。
func RegisterRoutes(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("/api/v1/queries", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, store.List())
		case http.MethodPost:
			var req SavedQuery
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			created, err := store.Save(req)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, created)
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})

	mux.HandleFunc("/api/v1/queries/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/v1/queries/")
		path = strings.Trim(path, "/")
		if path == "" {
			writeError(w, http.StatusBadRequest, "query id is required")
			return
		}

		if strings.HasSuffix(path, "/favorite") {
			id := strings.TrimSuffix(path, "/favorite")
			id = strings.Trim(id, "/")
			if r.Method != http.MethodPut {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			updated, err := store.ToggleFavorite(id)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, updated)
			return
		}

		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := store.Delete(path); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
