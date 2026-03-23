package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ── ConsoleChannel ──────────────────────────────────────────────────────────

func TestConsoleChannelType(t *testing.T) {
	ch := NewConsoleChannel()
	if ch.ChannelType() != "console" {
		t.Errorf("expected console, got %s", ch.ChannelType())
	}
}

func TestConsoleChannelSend(t *testing.T) {
	ch := NewConsoleChannel()
	n := Notification{AlertName: "test", Severity: "warning", State: "firing", Value: 42.0, Message: "hello"}
	if err := ch.Send(n); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── WebhookChannel ──────────────────────────────────────────────────────────

func TestWebhookChannelType(t *testing.T) {
	ch := NewWebhookChannel("http://localhost")
	if ch.ChannelType() != "webhook" {
		t.Errorf("expected webhook, got %s", ch.ChannelType())
	}
}

func TestWebhookChannelSend(t *testing.T) {
	var received webhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := NewWebhookChannel(srv.URL)
	n := Notification{AlertName: "cpu_high", Severity: "critical", State: "firing", Value: 95.5, Message: "CPU critical"}
	if err := ch.Send(n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if received.AlertName != "cpu_high" {
		t.Errorf("expected cpu_high, got %s", received.AlertName)
	}
	if received.Value != 95.5 {
		t.Errorf("expected 95.5, got %f", received.Value)
	}
}

func TestWebhookChannelSendHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch := NewWebhookChannel(srv.URL)
	n := Notification{AlertName: "x", Severity: "info", State: "firing"}
	if err := ch.Send(n); err == nil {
		t.Error("expected error for 500 response")
	}
}

// ── Router ──────────────────────────────────────────────────────────────────

func TestRouterDispatchAllSeverities(t *testing.T) {
	var sent []Notification
	ch := &mockChannel{sends: &sent}

	r := NewRouter()
	r.AddChannel(ch, nil) // no filter = all severities

	r.Dispatch(Notification{AlertName: "a", Severity: "critical", State: "firing"})
	r.Dispatch(Notification{AlertName: "b", Severity: "warning", State: "firing"})
	time.Sleep(50 * time.Millisecond) // goroutines

	if len(sent) != 2 {
		t.Errorf("expected 2 sent, got %d", len(sent))
	}
}

func TestRouterDispatchSeverityFilter(t *testing.T) {
	var sent []Notification
	ch := &mockChannel{sends: &sent}

	r := NewRouter()
	r.AddChannel(ch, []string{"critical"})

	r.Dispatch(Notification{AlertName: "a", Severity: "critical", State: "firing"})
	r.Dispatch(Notification{AlertName: "b", Severity: "warning", State: "firing"})
	time.Sleep(50 * time.Millisecond)

	if len(sent) != 1 {
		t.Errorf("expected 1 sent (critical only), got %d", len(sent))
	}
	if sent[0].AlertName != "a" {
		t.Errorf("expected alert 'a', got %s", sent[0].AlertName)
	}
}

func TestRouterRecordsHistory(t *testing.T) {
	r := NewRouter()
	ch := NewConsoleChannel()
	r.AddChannel(ch, nil)

	r.Dispatch(Notification{AlertName: "hist_test", Severity: "info", State: "firing"})
	time.Sleep(50 * time.Millisecond)

	records := r.History().List()
	if len(records) == 0 {
		t.Fatal("expected history record")
	}
	if records[0].AlertName != "hist_test" {
		t.Errorf("unexpected alert name: %s", records[0].AlertName)
	}
	if records[0].Status != "sent" {
		t.Errorf("expected status sent, got %s", records[0].Status)
	}
}

// ── History ─────────────────────────────────────────────────────────────────

func TestHistoryLimit(t *testing.T) {
	h := newHistory()
	for i := 0; i < maxHistorySize+10; i++ {
		h.Add(NotificationRecord{AlertName: "x"})
	}
	if h.Count() != maxHistorySize {
		t.Errorf("expected %d, got %d", maxHistorySize, h.Count())
	}
}

func TestHistoryNewestFirst(t *testing.T) {
	h := newHistory()
	h.Add(NotificationRecord{AlertName: "first"})
	h.Add(NotificationRecord{AlertName: "second"})
	records := h.List()
	if records[0].AlertName != "second" {
		t.Errorf("expected newest first, got %s", records[0].AlertName)
	}
}

// ── API ─────────────────────────────────────────────────────────────────────

func TestNotificationsAPIEndpoint(t *testing.T) {
	h := newHistory()
	h.Add(NotificationRecord{
		Timestamp:   time.Now(),
		ChannelType: "console",
		AlertName:   "api_test",
		Severity:    "warning",
		State:       "firing",
		Status:      "sent",
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var records []NotificationRecord
	if err := json.NewDecoder(rec.Body).Decode(&records); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].AlertName != "api_test" {
		t.Errorf("unexpected alert name: %s", records[0].AlertName)
	}
}

func TestNotificationsAPIMethodNotAllowed(t *testing.T) {
	h := newHistory()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

type mockChannel struct {
	sends *[]Notification
}

func (m *mockChannel) ChannelType() string { return "mock" }
func (m *mockChannel) Send(n Notification) error {
	*m.sends = append(*m.sends, n)
	return nil
}
