// Package registry manages connected WatchTower agents and their health status.
package registry

import (
	"sync"
	"time"
)

const (
	offlineThreshold = 30 * time.Second // agent is offline if last_seen > 30s ago
	healthCheckEvery = 10 * time.Second // background health check interval
)

// AgentInfo describes a registered monitoring agent.
type AgentInfo struct {
	AgentID      string            `json:"agent_id"`
	Hostname     string            `json:"hostname"`
	IPAddress    string            `json:"ip_address"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	RegisteredAt int64             `json:"registered_at"` // Unix ms
	LastSeenAt   int64             `json:"last_seen_at"`  // Unix ms
	Status       string            `json:"status"`        // "online" | "offline"
	Labels       map[string]string `json:"labels,omitempty"`
}

// Registry is a thread-safe store of AgentInfo keyed by agent_id.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*AgentInfo
	stopCh chan struct{}
}

// New creates a new Registry and starts the background health-check loop.
func New() *Registry {
	r := &Registry{
		agents: make(map[string]*AgentInfo),
		stopCh: make(chan struct{}),
	}
	go r.healthLoop()
	return r
}

// Stop shuts down the background goroutine.
func (r *Registry) Stop() {
	close(r.stopCh)
}

// Register upserts an agent: creates a new entry or updates last_seen_at / metadata
// for an existing one. Always sets status to "online".
func (r *Registry) Register(info AgentInfo) {
	now := time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.agents[info.AgentID]; ok {
		// Heartbeat: update last_seen and mutable fields.
		existing.LastSeenAt = now
		existing.Status = "online"
		existing.Hostname = info.Hostname
		existing.IPAddress = info.IPAddress
		existing.OS = info.OS
		existing.Arch = info.Arch
		existing.Labels = info.Labels
	} else {
		// First registration.
		info.RegisteredAt = now
		info.LastSeenAt = now
		info.Status = "online"
		cp := info
		r.agents[info.AgentID] = &cp
	}
}

// Get returns a copy of the AgentInfo for the given agent_id.
func (r *Registry) Get(agentID string) (AgentInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[agentID]
	if !ok {
		return AgentInfo{}, false
	}
	return *a, true
}

// List returns a snapshot of all registered agents.
func (r *Registry) List() []AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentInfo, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, *a)
	}
	return out
}

// Delete removes an agent by ID. Returns true if it existed.
func (r *Registry) Delete(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.agents[agentID]
	delete(r.agents, agentID)
	return ok
}

// TotalCount returns the number of registered agents.
func (r *Registry) TotalCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

// healthLoop runs every healthCheckEvery seconds and marks stale agents offline.
func (r *Registry) healthLoop() {
	ticker := time.NewTicker(healthCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.markStale()
		case <-r.stopCh:
			return
		}
	}
}

// markStale sets status="offline" for agents that have not sent a heartbeat recently.
func (r *Registry) markStale() {
	cutoff := time.Now().Add(-offlineThreshold).UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.LastSeenAt < cutoff {
			a.Status = "offline"
		}
	}
}
