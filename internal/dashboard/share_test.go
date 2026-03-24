package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newShareMux() (*http.ServeMux, *ShareStore) {
	ss := NewShareStore()
	mux := http.NewServeMux()
	RegisterShareRoutes(mux, ss)
	return mux, ss
}

// TestCreateShare_Basic 创建分享令牌应返回 201 及令牌信息
func TestCreateShare_Basic(t *testing.T) {
	mux, _ := newShareMux()

	body, _ := json.Marshal(map[string]interface{}{
		"label":       "测试仪表板",
		"ttl_seconds": 3600,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/share", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("期望 201，实际 %d", rr.Code)
	}

	var st ShareToken
	if err := json.NewDecoder(rr.Body).Decode(&st); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if st.Token == "" {
		t.Error("令牌不应为空")
	}
	if st.ExpiresAt == 0 {
		t.Error("应设置过期时间")
	}
	if st.Label != "测试仪表板" {
		t.Errorf("标签不匹配: %s", st.Label)
	}
}

// TestCreateShare_NoExpiry TTL=0 时永不过期
func TestCreateShare_NoExpiry(t *testing.T) {
	mux, _ := newShareMux()

	body, _ := json.Marshal(map[string]interface{}{"label": "永久链接"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/share", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var st ShareToken
	json.NewDecoder(rr.Body).Decode(&st)
	if st.ExpiresAt != 0 {
		t.Errorf("TTL=0 时 ExpiresAt 应为 0，实际 %d", st.ExpiresAt)
	}
	if st.Expired() {
		t.Error("永久令牌不应过期")
	}
}

// TestAccessSharedDashboard 有效令牌应返回只读 HTML
func TestAccessSharedDashboard(t *testing.T) {
	mux, ss := newShareMux()

	st, _ := ss.Create("我的面板", 0, nil)

	req := httptest.NewRequest(http.MethodGet, "/share/"+st.Token, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "WatchTower") {
		t.Error("HTML 应包含 WatchTower")
	}
	if !strings.Contains(body, st.Token) {
		t.Error("HTML 应包含令牌")
	}
	if !strings.Contains(body, "只读") {
		t.Error("HTML 应标注只读")
	}
}

// TestExpiredToken_Rejected 过期令牌应返回 410
func TestExpiredToken_Rejected(t *testing.T) {
	_, ss := newShareMux()

	// 手动插入已过期令牌
	ss.mu.Lock()
	tok := "expiredtoken12345678"
	ss.tokens[tok] = &ShareToken{
		Token:     tok,
		Label:     "expired",
		CreatedAt: time.Now().Add(-2 * time.Hour).UnixMilli(),
		ExpiresAt: time.Now().Add(-1 * time.Hour).UnixMilli(), // 1 小时前过期
	}
	ss.mu.Unlock()

	mux := http.NewServeMux()
	RegisterShareRoutes(mux, ss)

	req := httptest.NewRequest(http.MethodGet, "/share/"+tok, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusGone {
		t.Errorf("过期令牌应返回 410，实际 %d", rr.Code)
	}
}

// TestRevokeToken 撤销令牌后访问应返回 404
func TestRevokeToken(t *testing.T) {
	mux, ss := newShareMux()

	st, _ := ss.Create("临时", 3600, nil)

	// 撤销
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/dashboard/shares/"+st.Token, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("撤销应返回 204，实际 %d", rr.Code)
	}

	// 再次访问应 404
	req2 := httptest.NewRequest(http.MethodGet, "/share/"+st.Token, nil)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("撤销后访问应返回 404，实际 %d", rr2.Code)
	}
}

// TestListShares 列出所有令牌
func TestListShares(t *testing.T) {
	mux, ss := newShareMux()
	ss.Create("A", 0, nil)
	ss.Create("B", 3600, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/shares", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("期望 200，实际 %d", rr.Code)
	}
	var list []*ShareToken
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("期望 2 条，实际 %d", len(list))
	}
}

// TestShareInvalidPayload 无效 JSON 应返回 400
func TestShareInvalidPayload(t *testing.T) {
	mux, _ := newShareMux()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/share",
		strings.NewReader("not json"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("期望 400，实际 %d", rr.Code)
	}
}
