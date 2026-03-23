// Package registry — unit and API tests.
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── Registry store tests ─────────────────────────────────────────────────────

func makeAgent(id, hostname, ip, os, arch string) AgentInfo {
	return AgentInfo{
		AgentID:   id,
		Hostname:  hostname,
		IPAddress: ip,
		OS:        os,
		Arch:      arch,
	}
}

// TestRegisterNewAgent verifies a new agent is stored and marked online.
func TestRegisterNewAgent(t *testing.T) {
	r := New()
	defer r.Stop()

	r.Register(makeAgent("agent-1", "host1", "10.0.0.1", "linux", "amd64"))

	info, ok := r.Get("agent-1")
	if !ok {
		t.Fatal("agent-1 not found after Register")
	}
	if info.Status != "online" {
		t.Errorf("expected status online, got %s", info.Status)
	}
	if info.Hostname != "host1" {
		t.Errorf("expected hostname host1, got %s", info.Hostname)
	}
	if info.RegisteredAt <= 0 {
		t.Error("registered_at should be set")
	}
}

// TestRegisterHeartbeatUpdatesLastSeen verifies that re-registering updates last_seen_at.
func TestRegisterHeartbeatUpdatesLastSeen(t *testing.T) {
	r := New()
	defer r.Stop()

	r.Register(makeAgent("agent-2", "host2", "10.0.0.2", "linux", "amd64"))
	first, _ := r.Get("agent-2")
	firstSeen := first.LastSeenAt

	time.Sleep(5 * time.Millisecond) // ensure time advances
	r.Register(makeAgent("agent-2", "host2-updated", "10.0.0.2", "linux", "amd64"))
	second, _ := r.Get("agent-2")

	if second.LastSeenAt <= firstSeen {
		t.Error("last_seen_at should increase on heartbeat")
	}
	if second.Hostname != "host2-updated" {
		t.Error("hostname should be updated on heartbeat")
	}
	// registered_at should not change on heartbeat
	if second.RegisteredAt != first.RegisteredAt {
		t.Error("registered_at should not change on heartbeat")
	}
}

// TestListAgents verifies all registered agents are returned.
func TestListAgents(t *testing.T) {
	r := New()
	defer r.Stop()

	for i := 0; i < 3; i++ {
		r.Register(makeAgent(fmt.Sprintf("ag-%d", i), "host", "1.2.3.4", "linux", "amd64"))
	}

	agents := r.List()
	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}
}

// TestDeleteAgent verifies an agent can be removed.
func TestDeleteAgent(t *testing.T) {
	r := New()
	defer r.Stop()

	r.Register(makeAgent("del-me", "host", "1.2.3.4", "linux", "amd64"))
	if !r.Delete("del-me") {
		t.Fatal("Delete returned false for existing agent")
	}
	if _, ok := r.Get("del-me"); ok {
		t.Error("agent should be gone after Delete")
	}
	// Deleting again should return false
	if r.Delete("del-me") {
		t.Error("second Delete should return false")
	}
}

// TestGetNotFound verifies Get returns false for unknown agents.
func TestGetNotFound(t *testing.T) {
	r := New()
	defer r.Stop()

	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected false for unknown agent")
	}
}

// TestMarkStaleOffline verifies markStale sets status=offline for old agents.
func TestMarkStaleOffline(t *testing.T) {
	r := New()
	defer r.Stop()

	r.Register(makeAgent("stale-1", "host", "1.2.3.4", "linux", "amd64"))

	// Manually backdate last_seen_at to simulate a stale agent.
	r.mu.Lock()
	r.agents["stale-1"].LastSeenAt = time.Now().Add(-60 * time.Second).UnixMilli()
	r.mu.Unlock()

	r.markStale()

	info, _ := r.Get("stale-1")
	if info.Status != "offline" {
		t.Errorf("expected offline, got %s", info.Status)
	}
}

// TestMarkStaleKeepsOnlineRecent verifies recently seen agents remain online.
func TestMarkStaleKeepsOnlineRecent(t *testing.T) {
	r := New()
	defer r.Stop()

	r.Register(makeAgent("fresh-1", "host", "1.2.3.4", "linux", "amd64"))
	r.markStale()

	info, _ := r.Get("fresh-1")
	if info.Status != "online" {
		t.Errorf("expected online, got %s", info.Status)
	}
}

// TestTotalCount verifies TotalCount returns the correct number.
func TestTotalCount(t *testing.T) {
	r := New()
	defer r.Stop()

	if r.TotalCount() != 0 {
		t.Error("empty registry should have count 0")
	}
	r.Register(makeAgent("c1", "h", "1.2.3.4", "linux", "amd64"))
	r.Register(makeAgent("c2", "h", "1.2.3.4", "linux", "amd64"))
	if r.TotalCount() != 2 {
		t.Errorf("expected 2, got %d", r.TotalCount())
	}
}

// ── HTTP API tests ───────────────────────────────────────────────────────────

func newTestMux(t *testing.T) (*Registry, *http.ServeMux) {
	t.Helper()
	reg := New()
	t.Cleanup(func() { reg.Stop() })
	mux := http.NewServeMux()
	RegisterRoutes(mux, reg)
	return reg, mux
}

// TestAPIRegister verifies POST /api/v1/agents/register returns 204.
func TestAPIRegister(t *testing.T) {
	_, mux := newTestMux(t)

	body, _ := json.Marshal(makeAgent("api-agent", "host", "1.2.3.4", "linux", "amd64"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAPIRegisterMissingID verifies POST without agent_id returns 400.
func TestAPIRegisterMissingID(t *testing.T) {
	_, mux := newTestMux(t)

	body, _ := json.Marshal(map[string]string{"hostname": "host"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/register", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestAPIListAgents verifies GET /api/v1/agents returns all agents.
func TestAPIListAgents(t *testing.T) {
	reg, mux := newTestMux(t)
	reg.Register(makeAgent("list-1", "h1", "1.1.1.1", "linux", "amd64"))
	reg.Register(makeAgent("list-2", "h2", "1.1.1.2", "linux", "amd64"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var agents []AgentInfo
	if err := json.NewDecoder(w.Body).Decode(&agents); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("expected 2, got %d", len(agents))
	}
}

// TestAPIGetAgent verifies GET /api/v1/agents/{id} returns the agent.
func TestAPIGetAgent(t *testing.T) {
	reg, mux := newTestMux(t)
	reg.Register(makeAgent("get-1", "myhost", "192.168.1.1", "linux", "amd64"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/get-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var info AgentInfo
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if info.AgentID != "get-1" {
		t.Errorf("expected agent_id get-1, got %s", info.AgentID)
	}
}

// TestAPIGetAgentNotFound verifies GET for unknown agent returns 404.
func TestAPIGetAgentNotFound(t *testing.T) {
	_, mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/does-not-exist", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestAPIDeleteAgent verifies DELETE /api/v1/agents/{id} removes the agent.
func TestAPIDeleteAgent(t *testing.T) {
	reg, mux := newTestMux(t)
	reg.Register(makeAgent("rm-1", "h", "1.2.3.4", "linux", "amd64"))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/rm-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if _, ok := reg.Get("rm-1"); ok {
		t.Error("agent should be deleted")
	}
}

// TestAPIDeleteAgentNotFound verifies DELETE for unknown agent returns 404.
func TestAPIDeleteAgentNotFound(t *testing.T) {
	_, mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/nobody", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
