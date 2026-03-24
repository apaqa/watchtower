package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PanelConfig describes a panel inside a predefined dashboard template.
type PanelConfig struct {
	Title           string `json:"title"`
	PanelType       string `json:"panel_type"`
	WQLQuery        string `json:"wql_query"`
	Width           int    `json:"width"`
	RefreshInterval int    `json:"refresh_interval"`
}

// Template is a built-in dashboard layout.
type Template struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Panels      []PanelConfig `json:"panels"`
}

var builtInTemplates = []Template{
	{
		Name:        "System Overview",
		Description: "CPU、RAM、Disk 与 Network 的常用总览面板。",
		Panels: []PanelConfig{
			{Title: "CPU Usage", PanelType: "stat", WQLQuery: "avg(cpu_usage_percent[5m])", Width: 1, RefreshInterval: 15},
			{Title: "RAM Usage", PanelType: "stat", WQLQuery: "avg(memory_usage_percent[5m])", Width: 1, RefreshInterval: 15},
			{Title: "Disk Usage", PanelType: "stat", WQLQuery: "avg(disk_usage_percent[5m])", Width: 1, RefreshInterval: 30},
			{Title: "Network Throughput", PanelType: "chart", WQLQuery: "avg(network_bytes_total[5m])", Width: 2, RefreshInterval: 30},
		},
	},
	{
		Name:        "Application Monitoring",
		Description: "覆盖请求速率、错误率与延迟的应用监控模板。",
		Panels: []PanelConfig{
			{Title: "Request Rate", PanelType: "stat", WQLQuery: "avg(http_request_rate[5m])", Width: 1, RefreshInterval: 15},
			{Title: "Error Rate", PanelType: "stat", WQLQuery: "avg(http_error_rate[5m])", Width: 1, RefreshInterval: 15},
			{Title: "Latency P95", PanelType: "stat", WQLQuery: "avg(http_latency_p95_ms[5m])", Width: 1, RefreshInterval: 15},
		},
	},
	{
		Name:        "Infrastructure",
		Description: "适合基础设施巡检，包含探针、进程和磁盘预测。",
		Panels: []PanelConfig{
			{Title: "Probe Availability", PanelType: "stat", WQLQuery: "avg(probe_success_percent[5m])", Width: 1, RefreshInterval: 15},
			{Title: "Process Health", PanelType: "stat", WQLQuery: "avg(process_up[5m])", Width: 1, RefreshInterval: 15},
			{Title: "Disk Forecast", PanelType: "chart", WQLQuery: "avg(disk_usage_forecast_percent[1h])", Width: 2, RefreshInterval: 60},
		},
	},
}

// ListTemplates returns a copy of the built-in template gallery.
func ListTemplates() []Template {
	out := make([]Template, len(builtInTemplates))
	copy(out, builtInTemplates)
	return out
}

// ApplyTemplate creates panels from the named template and returns the new panels.
func ApplyTemplate(ps *PanelStore, name string) ([]Panel, error) {
	tpl, ok := findTemplate(name)
	if !ok {
		return nil, fmt.Errorf("template not found")
	}

	now := time.Now().UnixMilli()
	created := make([]Panel, 0, len(tpl.Panels))
	for idx, cfg := range tpl.Panels {
		panel := Panel{
			ID:              templatePanelID(tpl.Name, idx),
			Title:           cfg.Title,
			PanelType:       defaultPanelType(cfg.PanelType),
			WQLQuery:        cfg.WQLQuery,
			Width:           defaultPanelWidth(cfg.Width),
			RefreshInterval: defaultPanelRefresh(cfg.RefreshInterval),
			CreatedAt:       now,
		}
		ps.Add(panel)
		created = append(created, panel)
	}
	return created, nil
}

// RegisterTemplateRoutes registers dashboard template APIs.
func RegisterTemplateRoutes(mux *http.ServeMux, ps *PanelStore) {
	mux.HandleFunc("/api/v1/dashboard/templates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}
		json.NewEncoder(w).Encode(ListTemplates())
	})

	mux.HandleFunc("/api/v1/dashboard/templates/apply/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/api/v1/dashboard/templates/apply/")
		name, _ = url.PathUnescape(name)
		if strings.TrimSpace(name) == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "template name required"})
			return
		}

		panels, err := ApplyTemplate(ps, name)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(panels)
	})
}

func findTemplate(name string) (Template, bool) {
	for _, tpl := range builtInTemplates {
		if strings.EqualFold(tpl.Name, name) {
			return tpl, true
		}
	}
	return Template{}, false
}

func templatePanelID(name string, idx int) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, " ", "-")
	return fmt.Sprintf("template-%s-%d-%d", slug, idx+1, time.Now().UnixNano())
}

func defaultPanelType(value string) string {
	if value == "" {
		return "stat"
	}
	return value
}

func defaultPanelWidth(value int) int {
	if value < 1 || value > 4 {
		return 1
	}
	return value
}

func defaultPanelRefresh(value int) int {
	if value < 5 {
		return 30
	}
	return value
}
