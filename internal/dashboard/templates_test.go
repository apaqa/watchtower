package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newTemplateMux() (*http.ServeMux, *PanelStore) {
	ps := NewPanelStore()
	mux := http.NewServeMux()
	RegisterTemplateRoutes(mux, ps)
	RegisterPanelRoutes(mux, ps)
	return mux, ps
}

func TestListTemplates(t *testing.T) {
	mux, _ := newTemplateMux()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/templates", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var templates []Template
	if err := json.NewDecoder(rr.Body).Decode(&templates); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	if len(templates) != 3 {
		t.Fatalf("expected 3 templates, got %d", len(templates))
	}
}

func TestApplyTemplateCreatesCorrectPanels(t *testing.T) {
	mux, ps := newTemplateMux()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/templates/apply/"+url.PathEscape("System Overview"), nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}

	panels := ps.List()
	if len(panels) != 4 {
		t.Fatalf("expected 4 panels, got %d", len(panels))
	}
	if panels[0].Title != "CPU Usage" {
		t.Fatalf("expected first panel to be CPU Usage, got %q", panels[0].Title)
	}
	if panels[3].WQLQuery != "avg(network_bytes_total[5m])" {
		t.Fatalf("unexpected query %q", panels[3].WQLQuery)
	}
}

func TestApplyTemplateNotFound(t *testing.T) {
	mux, _ := newTemplateMux()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/templates/apply/does-not-exist", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestApplyTemplateViaPanelListEndpoint(t *testing.T) {
	mux, _ := newTemplateMux()

	applyReq := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/templates/apply/"+url.PathEscape("Infrastructure"), nil)
	applyRR := httptest.NewRecorder()
	mux.ServeHTTP(applyRR, applyReq)
	if applyRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", applyRR.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/panels", nil)
	listRR := httptest.NewRecorder()
	mux.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}

	var panels []Panel
	if err := json.NewDecoder(listRR.Body).Decode(&panels); err != nil {
		t.Fatalf("decode panels: %v", err)
	}
	if len(panels) != 3 {
		t.Fatalf("expected 3 panels, got %d", len(panels))
	}
}
