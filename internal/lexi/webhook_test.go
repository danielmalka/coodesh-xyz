package lexi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookAcceptsAndEnqueues(t *testing.T) {
	q := NewQueue(8)
	app := New(q, silentLog())

	rec := postWebhook(app, `{"message_id":"wamid.1","from":"+5511999999999","text":"quero negociar"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["received"] != true || body["message_id"] != "wamid.1" {
		t.Fatalf("body = %#v", body)
	}

	select {
	case job := <-q.jobs():
		if job.MessageID != "wamid.1" || job.From != "+5511999999999" || job.Text != "quero negociar" {
			t.Fatalf("job = %#v", job)
		}
	default:
		t.Fatal("queue empty")
	}
}

func TestWebhookRejectsInvalidPayload(t *testing.T) {
	app := New(NewQueue(8), silentLog())
	cases := []string{
		``,
		`{`,
		`{}`,
		`{"message_id":"wamid.1","from":"+5511","text":""}`,
		`{"message_id":"","from":"+5511","text":"oi"}`,
		`{"message_id":"wamid.1","from":"  ","text":"oi"}`,
	}
	for _, body := range cases {
		rec := postWebhook(app, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestWebhookIdempotentByMessageID(t *testing.T) {
	q := NewQueue(8)
	app := New(q, silentLog())
	body := `{"message_id":"wamid.dup","from":"+5511999999999","text":"quero negociar"}`

	if rec := postWebhook(app, body); rec.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", rec.Code)
	}
	if rec := postWebhook(app, body); rec.Code != http.StatusAccepted {
		t.Fatalf("second status = %d", rec.Code)
	}
	if n := len(q.ch); n != 1 {
		t.Fatalf("queued = %d, want 1", n)
	}
}

func TestWebhookQueueFull(t *testing.T) {
	q := NewQueue(1)
	app := New(q, silentLog())

	first := postWebhook(app, `{"message_id":"wamid.a","from":"+5511","text":"um"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", first.Code)
	}

	second := postWebhook(app, `{"message_id":"wamid.b","from":"+5511","text":"dois"}`)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want 503", second.Code)
	}
}

func TestWebhookDoesNotLogPII(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	app := New(NewQueue(8), log)

	phone := "+5511987654321"
	text := "divida do cartao 1234"
	body := `{"message_id":"wamid.pii","from":"` + phone + `","text":"` + text + `"}`
	if rec := postWebhook(app, body); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}

	out := buf.String()
	if strings.Contains(out, phone) {
		t.Fatalf("log leaked phone: %s", out)
	}
	if strings.Contains(out, text) {
		t.Fatalf("log leaked text: %s", out)
	}
	if !strings.Contains(out, "wamid.pii") {
		t.Fatalf("log missing message_id: %s", out)
	}
}

func postWebhook(app *App, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	return rec
}

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
