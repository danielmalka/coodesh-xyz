package lexi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/time/rate"
)

func TestWebhookAcceptsAndEnqueues(t *testing.T) {
	app := newWebhookApp(8, silentLog())

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
	case job := <-app.q.jobs():
		if job.MessageID != "wamid.1" || job.From != "+5511999999999" || job.Text != "quero negociar" {
			t.Fatalf("job = %#v", job)
		}
	default:
		t.Fatal("queue empty")
	}
	if got := app.metrics.Accepted.Load(); got != 1 {
		t.Fatalf("accepted = %d, want 1", got)
	}
}

func TestWebhookRejectsInvalidPayload(t *testing.T) {
	app := newWebhookApp(8, silentLog())
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
	if got := app.metrics.Received.Load(); got != 0 {
		t.Fatalf("received = %d, want 0 for invalid payloads", got)
	}
}

func TestWebhookIdempotentByMessageID(t *testing.T) {
	app := newWebhookApp(8, silentLog())
	body := `{"message_id":"wamid.dup","from":"+5511999999999","text":"quero negociar"}`

	if rec := postWebhook(app, body); rec.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", rec.Code)
	}
	if rec := postWebhook(app, body); rec.Code != http.StatusAccepted {
		t.Fatalf("second status = %d", rec.Code)
	}
	if n := len(app.q.ch); n != 1 {
		t.Fatalf("queued = %d, want 1", n)
	}
	if got := app.metrics.Duplicate.Load(); got != 1 {
		t.Fatalf("duplicate = %d, want 1", got)
	}
}

func TestWebhookQueueFull(t *testing.T) {
	app := newWebhookApp(1, silentLog())

	first := postWebhook(app, `{"message_id":"wamid.a","from":"+5511","text":"um"}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", first.Code)
	}

	second := postWebhook(app, `{"message_id":"wamid.b","from":"+5511","text":"dois"}`)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want 503", second.Code)
	}
	if got := app.metrics.QueueFull.Load(); got != 1 {
		t.Fatalf("queue_full = %d, want 1", got)
	}
}

func TestWebhookDoesNotLogPII(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	app := newWebhookApp(8, log)

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

func newWebhookApp(queueSize int, log *slog.Logger) *App {
	cfg := DefaultConfig()
	cfg.QueueSize = queueSize
	return New(cfg, nil, nil, log)
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

func TestWebhookAfterCloseReturns503(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueSize = 8
	app := New(cfg, nil, nil, silentLog())
	app.Close()
	rec := postWebhook(app, webhookBody("wamid.closed", "x"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "shutting down") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	snap := app.metrics.Snapshot()
	if snap["queue_closed"] != 1 || snap["queue_full"] != 0 {
		t.Fatalf("queue_closed = %d, queue_full = %d", snap["queue_closed"], snap["queue_full"])
	}
}

func TestGetDLQAfterLLMFailure(t *testing.T) {
	srv, _ := captureServer(t)
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	app := testApp(t, cfg, fakeLLM{reply: func(context.Context, string) (string, error) { return "", errLLMUnavailable }}, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.dlq2", testText))
	drainWorkers(t, app, done, cancel)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dlq", nil))
	body := rec.Body.String()
	var resp struct {
		Count int              `json:"count"`
		Items []deadLetterView `json:"items"`
	}
	_ = json.Unmarshal([]byte(body), &resp)
	if strings.Contains(body, testPhone) || strings.Contains(body, testText) ||
		resp.Count != 1 || resp.Items[0].From != hashPhone(testPhone) || resp.Items[0].Chars != len(testText) {
		t.Fatalf("body=%s resp=%#v", body, resp)
	}
}

func TestMetricsDebugAndPprof(t *testing.T) {
	h := New(DefaultConfig(), nil, nil, silentLog()).Routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	var snap map[string]int64
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&snap) != nil {
		t.Fatalf("metrics status=%d", rec.Code)
	}
	for _, key := range []string{"received", "accepted", "duplicate", "queue_full", "rate_limited", "processed", "failed_llm", "failed_outbound", "in_flight", "dead_letters", "queue_depth"} {
		if _, ok := snap[key]; !ok {
			t.Fatalf("missing %q", key)
		}
	}
	for _, path := range []string{"/debug/vars", "/debug/pprof/"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
}

const benchQueueSize = 100_000

func BenchmarkWebhookHandler(b *testing.B) {
	cfg := DefaultConfig()
	cfg.QueueSize = benchQueueSize
	app := New(cfg, nil, nil, silentLog())
	app.inbound = rate.NewLimiter(rate.Inf, 0)
	h := app.Routes()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		body := `{"message_id":"wamid.bench.` + strconv.Itoa(i) + `","from":"+5511987654321","text":"x"}`
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(httptest.NewRecorder(), req)
		<-app.q.jobs()
		i++
	}
}
