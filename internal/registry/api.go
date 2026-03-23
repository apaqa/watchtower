// Package registry — HTTP API for agent registration and management.
package registry

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes adds agent registry endpoints to mux.
//
//	POST   /api/v1/agents/register     — register or heartbeat
//	GET    /api/v1/agents              — list all agents
//	GET    /api/v1/agents/{id}         — get single agent
//	DELETE /api/v1/agents/{id}         — remove agent
func RegisterRoutes(mux *http.ServeMux, r *Registry) {
	mux.HandleFunc("/api/v1/agents/register", func(w http.ResponseWriter, req *http.Request) {
		handleRegister(w, req, r)
	})
	// exact match for list
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, req *http.Request) {
		handleList(w, req, r)
	})
	// prefix match for single-agent operations
	mux.HandleFunc("/api/v1/agents/", func(w http.ResponseWriter, req *http.Request) {
		// route /api/v1/agents/register to the register handler if somehow reached here
		if strings.HasSuffix(req.URL.Path, "/register") {
			handleRegister(w, req, r)
			return
		}
		handleAgent(w, req, r)
	})
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleRegister handles POST /api/v1/agents/register.
func handleRegister(w http.ResponseWriter, r *http.Request, reg *Registry) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var info AgentInfo
	if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
		jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if info.AgentID == "" {
		jsonError(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	reg.Register(info)
	w.WriteHeader(http.StatusNoContent)
}

// handleList handles GET /api/v1/agents.
func handleList(w http.ResponseWriter, r *http.Request, reg *Registry) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agents := reg.List()
	if agents == nil {
		agents = []AgentInfo{}
	}
	json.NewEncoder(w).Encode(agents)
}

// handleAgent handles GET /DELETE /api/v1/agents/{id}.
func handleAgent(w http.ResponseWriter, r *http.Request, reg *Registry) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Extract agent ID from path: /api/v1/agents/{id}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	if id == "" {
		jsonError(w, "agent_id required in path", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		info, ok := reg.Get(id)
		if !ok {
			jsonError(w, "agent not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(info)

	case http.MethodDelete:
		if !reg.Delete(id) {
			jsonError(w, "agent not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
