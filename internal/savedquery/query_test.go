package savedquery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStoreSaveAndList(t *testing.T) {
	store := NewStore()
	created, err := store.Save(SavedQuery{
		Name:          "CPU Avg",
		WQLExpression: "avg(cpu_usage_percent[5m])",
		Description:   "Average CPU",
		CreatedBy:     "tester",
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected query ID")
	}

	list := store.List()
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("unexpected list output: %+v", list)
	}
}

func TestStoreToggleFavoritePinsQueryFirst(t *testing.T) {
	store := NewStore()
	first, _ := store.Save(SavedQuery{Name: "A", WQLExpression: "avg(a[5m])", CreatedBy: "tester"})
	second, _ := store.Save(SavedQuery{Name: "B", WQLExpression: "avg(b[5m])", CreatedBy: "tester"})

	if _, err := store.ToggleFavorite(first.ID); err != nil {
		t.Fatalf("ToggleFavorite returned error: %v", err)
	}

	list := store.List()
	if list[0].ID != first.ID {
		t.Fatalf("expected favorite query %q first, got %q", first.ID, list[0].ID)
	}
	if list[1].ID != second.ID {
		t.Fatalf("expected non-favorite query %q second, got %q", second.ID, list[1].ID)
	}
}

func TestStoreDelete(t *testing.T) {
	store := NewStore()
	query, _ := store.Save(SavedQuery{Name: "A", WQLExpression: "avg(a[5m])", CreatedBy: "tester"})

	if err := store.Delete(query.ID); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("expected store to be empty after delete")
	}
}

func TestSavedQueryAPI(t *testing.T) {
	store := NewStore()
	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/queries",
		bytes.NewBufferString(`{"name":"CPU Avg","wql_expression":"avg(cpu_usage_percent[5m])","description":"CPU","created_by":"dashboard"}`))
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createRec.Code)
	}

	var created SavedQuery
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	favReq := httptest.NewRequest(http.MethodPut, "/api/v1/queries/"+created.ID+"/favorite", nil)
	favRec := httptest.NewRecorder()
	mux.ServeHTTP(favRec, favReq)
	if favRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from favorite toggle, got %d", favRec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/queries", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from list, got %d", listRec.Code)
	}

	var list []SavedQuery
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}
	if len(list) != 1 || !list[0].IsFavorite {
		t.Fatalf("unexpected list output: %+v", list)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/queries/"+created.ID, nil)
	deleteRec := httptest.NewRecorder()
	mux.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from delete, got %d", deleteRec.Code)
	}
}
