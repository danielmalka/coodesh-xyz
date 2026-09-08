package lexi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	mockTestMessage = "wamid.mock.e2e"
	mockRoutePath   = "/mock/whatsapp/messages"
	mockValidBody   = `{"messaging_product":"whatsapp","to":"` + testPhone + `","type":"text","text":{"body":"` + testText + `"}}`
)

func postMockWhatsApp(app *App, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, mockRoutePath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	return rec
}

func TestMockWhatsAppDelivers(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	app := New(NewQueue(8), log).WithMockFailureRate(0)

	rec := postMockWhatsApp(app, mockValidBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 1 || !strings.HasPrefix(resp.Messages[0].ID, "wamid.mock.") {
		t.Fatalf("resp = %+v, want one id with wamid.mock. prefix", resp)
	}
	out := buf.String()
	if strings.Contains(out, testPhone) {
		t.Fatalf("log leaked phone: %s", out)
	}
	if strings.Contains(out, testText) {
		t.Fatalf("log leaked body: %s", out)
	}
	if !strings.Contains(out, hashPhone(testPhone)) {
		t.Fatalf("log missing hashed to: %s", out)
	}
}

func TestMockWhatsAppRejectsInvalidPayload(t *testing.T) {
	app := New(NewQueue(8), silentLog())
	cases := []string{
		``,
		`{`,
		`{}`,
		`{"to":"","text":{"body":"oi"}}`,
		`{"to":"` + testPhone + `","text":{"body":""}}`,
		`{"to":"` + testPhone + `"}`,
		`{"text":{"body":"oi"}}`,
	}
	for _, body := range cases {
		if rec := postMockWhatsApp(app, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestMockWhatsAppSimulatedOutage(t *testing.T) {
	app := New(NewQueue(8), silentLog()).WithMockFailureRate(1)

	rec := postMockWhatsApp(app, mockValidBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "simulated outage" {
		t.Fatalf("resp = %#v, want simulated outage error", resp)
	}
}

func TestMockWhatsAppEndToEnd(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	app := New(NewQueue(8), silentLog()).WithMockFailureRate(0)
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.WhatsAppURL = srv.URL + mockRoutePath
	cfg.MaxAttempts = 3
	cfg.RetryBaseDelay = testRetryBaseDelay
	cfg.RetryMaxDelay = testRetryMaxDelay
	sender := NewWhatsAppSender(cfg, nil, log)
	msg := OutboundMessage{MessageID: mockTestMessage, To: testPhone, Text: testText}
	if err := sender.Send(t.Context(), msg); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	out := buf.String()
	if !strings.Contains(out, "wamid.mock.") {
		t.Fatalf("log missing wamid.mock. id: %s", out)
	}
}
