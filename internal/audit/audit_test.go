package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAuditStore_Log 写入条目后应可读取
func TestAuditStore_Log(t *testing.T) {
	s := New()
	s.Log(AuditEntry{
		User:         "alice",
		Action:       ActionCreate,
		ResourceType: ResourceAlertRule,
		Details:      "POST /api/v1/alerts/rules",
		Status:       StatusSuccess,
	})

	entries := s.List(FilterOptions{})
	if len(entries) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d", len(entries))
	}
	if entries[0].User != "alice" {
		t.Errorf("期望 user=alice，实际 %q", entries[0].User)
	}
	if entries[0].Timestamp == 0 {
		t.Error("期望自动填充时间戳")
	}
}

// TestAuditStore_FilterByUser 按 user 过滤应只返回匹配的条目
func TestAuditStore_FilterByUser(t *testing.T) {
	s := New()
	s.Log(AuditEntry{User: "alice", Action: ActionCreate, Status: StatusSuccess})
	s.Log(AuditEntry{User: "bob", Action: ActionDelete, Status: StatusSuccess})
	s.Log(AuditEntry{User: "alice", Action: ActionUpdate, Status: StatusSuccess})

	results := s.List(FilterOptions{User: "alice"})
	if len(results) != 2 {
		t.Errorf("期望 alice 的 2 条记录，实际 %d", len(results))
	}
}

// TestAuditStore_FilterByAction 按 action 过滤
func TestAuditStore_FilterByAction(t *testing.T) {
	s := New()
	s.Log(AuditEntry{User: "u1", Action: ActionCreate, Status: StatusSuccess})
	s.Log(AuditEntry{User: "u2", Action: ActionDelete, Status: StatusSuccess})
	s.Log(AuditEntry{User: "u3", Action: ActionCreate, Status: StatusSuccess})

	results := s.List(FilterOptions{Action: ActionDelete})
	if len(results) != 1 {
		t.Errorf("期望 1 条 delete 记录，实际 %d", len(results))
	}
	if results[0].User != "u2" {
		t.Errorf("期望 user=u2，实际 %q", results[0].User)
	}
}

// TestAuditStore_FilterByResourceType 按资源类型过滤
func TestAuditStore_FilterByResourceType(t *testing.T) {
	s := New()
	s.Log(AuditEntry{User: "a", Action: ActionCreate, ResourceType: ResourceAlertRule, Status: StatusSuccess})
	s.Log(AuditEntry{User: "b", Action: ActionCreate, ResourceType: ResourceProbe, Status: StatusSuccess})

	results := s.List(FilterOptions{ResourceType: ResourceAlertRule})
	if len(results) != 1 || results[0].ResourceType != ResourceAlertRule {
		t.Errorf("期望 1 条 alert_rule 记录，实际 %v", results)
	}
}

// TestAuditStore_RingBufferEviction 写超过 maxEntries 时旧记录应被覆盖
func TestAuditStore_RingBufferEviction(t *testing.T) {
	s := New()
	// 写入 maxEntries+10 条
	for i := 0; i < maxEntries+10; i++ {
		s.Log(AuditEntry{
			User:      "fill",
			Action:    ActionCreate,
			Status:    StatusSuccess,
			Details:   "entry",
			Timestamp: int64(i + 1),
		})
	}

	entries := s.List(FilterOptions{})
	if len(entries) > maxEntries {
		t.Errorf("环形缓冲区应最多保存 %d 条，实际 %d", maxEntries, len(entries))
	}
	// 最新的条目应有最大时间戳
	if entries[0].Timestamp < int64(maxEntries) {
		t.Errorf("期望最新条目时间戳 >= %d，实际 %d", maxEntries, entries[0].Timestamp)
	}
}

// TestAuditStore_FilterByTimeRange 按时间范围过滤
func TestAuditStore_FilterByTimeRange(t *testing.T) {
	s := New()
	now := time.Now().UnixMilli()
	s.Log(AuditEntry{User: "x", Action: ActionCreate, Status: StatusSuccess, Timestamp: now - 10000})
	s.Log(AuditEntry{User: "y", Action: ActionCreate, Status: StatusSuccess, Timestamp: now})
	s.Log(AuditEntry{User: "z", Action: ActionCreate, Status: StatusSuccess, Timestamp: now + 10000})

	// 只取中间那条
	results := s.List(FilterOptions{StartMS: now - 1000, EndMS: now + 1000})
	if len(results) != 1 || results[0].User != "y" {
		t.Errorf("时间范围过滤期望 1 条 user=y，实际 %v", results)
	}
}

// TestAuditMiddleware_LogsWriteOps 写操作应被记录，读操作不应被记录
func TestAuditMiddleware_LogsWriteOps(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := Middleware(s, mux)

	// POST 应被记录
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	entries := s.List(FilterOptions{})
	if len(entries) != 1 {
		t.Errorf("期望 1 条审计记录，实际 %d", len(entries))
	}
	if entries[0].Action != ActionCreate {
		t.Errorf("期望 action=create，实际 %s", entries[0].Action)
	}

	// GET 不应被记录
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)

	entries2 := s.List(FilterOptions{})
	if len(entries2) != 1 {
		t.Errorf("GET 不应产生新审计记录，实际 %d", len(entries2))
	}
}

// TestAuditAPI_List GET /api/v1/audit 应返回日志列表
func TestAuditAPI_List(t *testing.T) {
	s := New()
	s.Log(AuditEntry{User: "admin", Action: ActionDelete, ResourceType: ResourceProbe, Status: StatusSuccess})

	mux := http.NewServeMux()
	RegisterRoutes(mux, s)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
	var entries []AuditEntry
	if err := json.NewDecoder(rr.Body).Decode(&entries); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("期望 1 条审计记录，实际 %d", len(entries))
	}
}

// TestAuditMiddleware_StatusDenied 403 响应应标记为 denied
func TestAuditMiddleware_StatusDenied(t *testing.T) {
	s := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/keys", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})

	handler := Middleware(s, mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	entries := s.List(FilterOptions{})
	if len(entries) != 1 {
		t.Fatalf("期望 1 条审计记录，实际 %d", len(entries))
	}
	if entries[0].Status != StatusDenied {
		t.Errorf("期望 status=denied，实际 %s", entries[0].Status)
	}
}
