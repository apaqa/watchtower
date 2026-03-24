package tags

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagerTagCRUD(t *testing.T) {
	m := NewManager()

	created, err := m.CreateTag(Tag{Key: "env", Value: "production", Color: "#22c55e"})
	if err != nil {
		t.Fatalf("CreateTag returned error: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected created tag ID")
	}

	updated, err := m.UpdateTag(created.ID, Tag{Key: "env", Value: "prod", Color: "#16a34a"})
	if err != nil {
		t.Fatalf("UpdateTag returned error: %v", err)
	}
	if updated.Value != "prod" {
		t.Fatalf("expected updated value prod, got %q", updated.Value)
	}

	if err := m.DeleteTag(created.ID); err != nil {
		t.Fatalf("DeleteTag returned error: %v", err)
	}
	if got := m.ListTags(); len(got) != 0 {
		t.Fatalf("expected 0 tags after delete, got %d", len(got))
	}
}

func TestManagerAttachAndTagsForResource(t *testing.T) {
	m := NewManager()
	tag, _ := m.CreateTag(Tag{Key: "team", Value: "core", Color: "#2563eb"})

	if err := m.AttachTag(tag.ID, ResourceMetric, "cpu_usage_percent"); err != nil {
		t.Fatalf("AttachTag returned error: %v", err)
	}

	tags, err := m.TagsForResource(ResourceMetric, "cpu_usage_percent")
	if err != nil {
		t.Fatalf("TagsForResource returned error: %v", err)
	}
	if len(tags) != 1 || tags[0].ID != tag.ID {
		t.Fatalf("unexpected tags: %+v", tags)
	}
}

func TestManagerSearchResourcesByTag(t *testing.T) {
	m := NewManager()
	prod, _ := m.CreateTag(Tag{Key: "env", Value: "production", Color: "#22c55e"})
	core, _ := m.CreateTag(Tag{Key: "team", Value: "core", Color: "#2563eb"})

	_ = m.AttachTag(prod.ID, ResourceMetric, "cpu_usage_percent")
	_ = m.AttachTag(core.ID, ResourceMetric, "cpu_usage_percent")
	_ = m.AttachTag(prod.ID, ResourceProbe, "api-home")

	results := m.SearchResourcesByTag("production")
	if len(results) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(results))
	}
}

func TestCreateTagAPI(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewManager())

	body := bytes.NewBufferString(`{"key":"env","value":"production","color":"#22c55e"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tags", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var tag Tag
	if err := json.Unmarshal(rec.Body.Bytes(), &tag); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if tag.ID == "" {
		t.Fatal("expected response tag ID")
	}
}

func TestAttachAndSearchAPI(t *testing.T) {
	manager := NewManager()
	tag, _ := manager.CreateTag(Tag{Key: "env", Value: "production", Color: "#22c55e"})
	mux := http.NewServeMux()
	RegisterRoutes(mux, manager)

	attachBody := bytes.NewBufferString(`{"tag_id":"` + tag.ID + `","resource_type":"probe","resource_id":"homepage"}`)
	attachReq := httptest.NewRequest(http.MethodPost, "/api/v1/tags/attach", attachBody)
	attachRec := httptest.NewRecorder()
	mux.ServeHTTP(attachRec, attachReq)
	if attachRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from attach, got %d", attachRec.Code)
	}

	searchReq := httptest.NewRequest(http.MethodGet, "/api/v1/tags/search?tag=production", nil)
	searchRec := httptest.NewRecorder()
	mux.ServeHTTP(searchRec, searchReq)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from search, got %d", searchRec.Code)
	}

	var resp struct {
		Resources []TaggedResource `json:"resources"`
	}
	if err := json.Unmarshal(searchRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Resources) != 1 || resp.Resources[0].ResourceID != "homepage" {
		t.Fatalf("unexpected search result: %+v", resp.Resources)
	}
}
