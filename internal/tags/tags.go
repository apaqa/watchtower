// Package tags 提供标签管理与资源绑定能力。
package tags

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// ResourceMetric 表示指标资源。
	ResourceMetric = "metric"
	// ResourceAlert 表示告警资源。
	ResourceAlert = "alert"
	// ResourceProbe 表示探针资源。
	ResourceProbe = "probe"
)

var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Tag 表示可复用的彩色标签。
type Tag struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Color     string `json:"color"`
	CreatedAt int64  `json:"created_at"`
}

// Attachment 表示标签与资源之间的关联。
type Attachment struct {
	TagID        string `json:"tag_id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

// TaggedResource 表示带标签的资源。
type TaggedResource struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Tags         []Tag  `json:"tags"`
}

// Manager 负责标签 CRUD 与资源绑定。
type Manager struct {
	mu          sync.RWMutex
	nextID      int64
	tags        map[string]*Tag
	tagOrder    []string
	attachments map[string]map[string]struct{}
}

// NewManager 创建新的标签管理器。
func NewManager() *Manager {
	return &Manager{
		nextID:      1,
		tags:        make(map[string]*Tag),
		attachments: make(map[string]map[string]struct{}),
	}
}

// CreateTag 新建标签。
func (m *Manager) CreateTag(tag Tag) (Tag, error) {
	if err := validateTag(tag); err != nil {
		return Tag{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	tag.ID = fmt.Sprintf("tag-%d", m.nextID)
	tag.CreatedAt = time.Now().UnixMilli()
	m.nextID++

	cp := tag
	m.tags[tag.ID] = &cp
	m.tagOrder = append(m.tagOrder, tag.ID)
	return cp, nil
}

// UpdateTag 更新既有标签。
func (m *Manager) UpdateTag(id string, update Tag) (Tag, error) {
	if err := validateTag(update); err != nil {
		return Tag{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.tags[id]
	if !ok {
		return Tag{}, fmt.Errorf("tag %q not found", id)
	}
	existing.Key = update.Key
	existing.Value = update.Value
	existing.Color = update.Color
	return *existing, nil
}

// DeleteTag 删除标签并移除所有绑定关系。
func (m *Manager) DeleteTag(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tags[id]; !ok {
		return fmt.Errorf("tag %q not found", id)
	}
	delete(m.tags, id)
	for key, tagIDs := range m.attachments {
		delete(tagIDs, id)
		if len(tagIDs) == 0 {
			delete(m.attachments, key)
		}
	}
	for i, tagID := range m.tagOrder {
		if tagID == id {
			m.tagOrder = append(m.tagOrder[:i], m.tagOrder[i+1:]...)
			break
		}
	}
	return nil
}

// ListTags 返回所有标签。
func (m *Manager) ListTags() []Tag {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Tag, 0, len(m.tagOrder))
	for _, id := range m.tagOrder {
		if tag, ok := m.tags[id]; ok {
			out = append(out, *tag)
		}
	}
	return out
}

// AttachTag 将标签绑定到资源。
func (m *Manager) AttachTag(tagID, resourceType, resourceID string) error {
	if err := validateResource(resourceType, resourceID); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.tags[tagID]; !ok {
		return fmt.Errorf("tag %q not found", tagID)
	}

	key := resourceKey(resourceType, resourceID)
	if _, ok := m.attachments[key]; !ok {
		m.attachments[key] = make(map[string]struct{})
	}
	m.attachments[key][tagID] = struct{}{}
	return nil
}

// TagsForResource 返回资源上的所有标签。
func (m *Manager) TagsForResource(resourceType, resourceID string) ([]Tag, error) {
	if err := validateResource(resourceType, resourceID); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.tagsForResourceLocked(resourceType, resourceID), nil
}

// SearchResourcesByTag 查找所有带有指定标签的资源。
func (m *Manager) SearchResourcesByTag(query string) []TaggedResource {
	query = strings.TrimSpace(query)
	if query == "" {
		return []TaggedResource{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []TaggedResource
	for key, tagIDs := range m.attachments {
		tags := make([]Tag, 0, len(tagIDs))
		matched := false
		for tagID := range tagIDs {
			tag, ok := m.tags[tagID]
			if !ok {
				continue
			}
			tags = append(tags, *tag)
			if tagMatches(*tag, query) {
				matched = true
			}
		}
		if !matched {
			continue
		}
		sortTags(tags)
		resourceType, resourceID := parseResourceKey(key)
		result = append(result, TaggedResource{
			ResourceType: resourceType,
			ResourceID:   resourceID,
			Tags:         tags,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].ResourceType != result[j].ResourceType {
			return result[i].ResourceType < result[j].ResourceType
		}
		return result[i].ResourceID < result[j].ResourceID
	})
	return result
}

func (m *Manager) tagsForResourceLocked(resourceType, resourceID string) []Tag {
	tagIDs := m.attachments[resourceKey(resourceType, resourceID)]
	if len(tagIDs) == 0 {
		return []Tag{}
	}

	out := make([]Tag, 0, len(tagIDs))
	for tagID := range tagIDs {
		if tag, ok := m.tags[tagID]; ok {
			out = append(out, *tag)
		}
	}
	sortTags(out)
	return out
}

func sortTags(tags []Tag) {
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Key != tags[j].Key {
			return tags[i].Key < tags[j].Key
		}
		if tags[i].Value != tags[j].Value {
			return tags[i].Value < tags[j].Value
		}
		return tags[i].ID < tags[j].ID
	})
}

func validateTag(tag Tag) error {
	if strings.TrimSpace(tag.Key) == "" {
		return fmt.Errorf("tag key is required")
	}
	if strings.TrimSpace(tag.Value) == "" {
		return fmt.Errorf("tag value is required")
	}
	if !hexColorPattern.MatchString(tag.Color) {
		return fmt.Errorf("tag color must be a hex value like #22c55e")
	}
	return nil
}

func validateResource(resourceType, resourceID string) error {
	switch resourceType {
	case ResourceMetric, ResourceAlert, ResourceProbe:
	default:
		return fmt.Errorf("unsupported resource_type %q", resourceType)
	}
	if strings.TrimSpace(resourceID) == "" {
		return fmt.Errorf("resource_id is required")
	}
	return nil
}

func tagMatches(tag Tag, query string) bool {
	return tag.Key == query || tag.Value == query || tag.Key+":"+tag.Value == query
}

func resourceKey(resourceType, resourceID string) string {
	return resourceType + ":" + resourceID
}

func parseResourceKey(key string) (string, string) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", key
	}
	return parts[0], parts[1]
}

// RegisterRoutes 注册标签 API 路由。
func RegisterRoutes(mux *http.ServeMux, manager *Manager) {
	mux.HandleFunc("/api/v1/tags", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, manager.ListTags())
		case http.MethodPost:
			var req Tag
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
				return
			}
			created, err := manager.CreateTag(req)
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

	mux.HandleFunc("/api/v1/tags/attach", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req Attachment
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := manager.AttachTag(req.TagID, req.ResourceType, req.ResourceID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/v1/tags/search", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		q := r.URL.Query()
		if tagQuery := strings.TrimSpace(q.Get("tag")); tagQuery != "" {
			writeJSON(w, http.StatusOK, map[string][]TaggedResource{
				"resources": manager.SearchResourcesByTag(tagQuery),
			})
			return
		}

		resourceType := q.Get("resource_type")
		resourceID := q.Get("resource_id")
		if resourceType == "" || resourceID == "" {
			writeError(w, http.StatusBadRequest, "tag or resource_type/resource_id is required")
			return
		}

		tags, err := manager.TagsForResource(resourceType, resourceID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string][]Tag{"tags": tags})
	})
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
