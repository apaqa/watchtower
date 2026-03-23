// Package dashboard — custom panel store and HTTP API.
package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Panel represents a user-defined dashboard panel.
type Panel struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	PanelType       string `json:"panel_type"`       // "stat" | "chart" | "table" | "gauge"
	WQLQuery        string `json:"wql_query"`        // WQL expression to evaluate
	Width           int    `json:"width"`            // 1-4 column width units
	RefreshInterval int    `json:"refresh_interval"` // seconds between auto-refresh (min 5)
	CreatedAt       int64  `json:"created_at"`       // Unix ms
}

// PanelStore holds custom panels in memory.
type PanelStore struct {
	mu     sync.RWMutex
	panels map[string]*Panel
	order  []string // insertion order for stable listing
}

// NewPanelStore creates a new PanelStore.
func NewPanelStore() *PanelStore {
	return &PanelStore{
		panels: make(map[string]*Panel),
	}
}

// Add inserts or replaces a panel. Sets CreatedAt when new.
func (ps *PanelStore) Add(p Panel) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, exists := ps.panels[p.ID]; !exists {
		p.CreatedAt = time.Now().UnixMilli()
		ps.order = append(ps.order, p.ID)
	}
	cp := p
	ps.panels[p.ID] = &cp
}

// Get returns a copy of the panel with the given id.
func (ps *PanelStore) Get(id string) (Panel, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	p, ok := ps.panels[id]
	if !ok {
		return Panel{}, false
	}
	return *p, true
}

// List returns all panels in insertion order.
func (ps *PanelStore) List() []Panel {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]Panel, 0, len(ps.order))
	for _, id := range ps.order {
		if p, ok := ps.panels[id]; ok {
			out = append(out, *p)
		}
	}
	return out
}

// Delete removes a panel by id. Returns true if it existed.
func (ps *PanelStore) Delete(id string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	_, ok := ps.panels[id]
	if !ok {
		return false
	}
	delete(ps.panels, id)
	// Remove from order slice.
	for i, oid := range ps.order {
		if oid == id {
			ps.order = append(ps.order[:i], ps.order[i+1:]...)
			break
		}
	}
	return true
}

// RegisterPanelRoutes adds panel CRUD endpoints to mux.
//
//	POST   /api/v1/dashboard/panels       — create panel
//	GET    /api/v1/dashboard/panels       — list panels
//	DELETE /api/v1/dashboard/panels/{id}  — delete panel
func RegisterPanelRoutes(mux *http.ServeMux, ps *PanelStore) {
	mux.HandleFunc("/api/v1/dashboard/panels", func(w http.ResponseWriter, r *http.Request) {
		handlePanels(w, r, ps)
	})
	mux.HandleFunc("/api/v1/dashboard/panels/", func(w http.ResponseWriter, r *http.Request) {
		handlePanelByID(w, r, ps)
	})
}

func handlePanels(w http.ResponseWriter, r *http.Request, ps *PanelStore) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodGet:
		panels := ps.List()
		if panels == nil {
			panels = []Panel{}
		}
		json.NewEncoder(w).Encode(panels)

	case http.MethodPost:
		var p Panel
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if p.ID == "" || p.Title == "" || p.WQLQuery == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "id, title, and wql_query are required"})
			return
		}
		if p.PanelType == "" {
			p.PanelType = "stat"
		}
		if p.Width < 1 || p.Width > 4 {
			p.Width = 1
		}
		if p.RefreshInterval < 5 {
			p.RefreshInterval = 30
		}
		ps.Add(p)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(p)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
	}
}

func handlePanelByID(w http.ResponseWriter, r *http.Request, ps *PanelStore) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/dashboard/panels/")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "panel id required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		p, ok := ps.Get(id)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "panel not found"})
			return
		}
		json.NewEncoder(w).Encode(p)

	case http.MethodDelete:
		if !ps.Delete(id) {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "panel not found"})
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
	}
}
