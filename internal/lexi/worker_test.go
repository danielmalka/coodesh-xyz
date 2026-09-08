package lexi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	workerTestTimeout = 2 * time.Second
	workerOutboundTO  = 200 * time.Millisecond
	workerLLMSleep    = 5 * time.Millisecond
	workerSlowLLMTO   = 10 * time.Millisecond
	fakeReplyBody     = "proposta pronta"
	gracefulJobCount  = 20
	abandonJobCount   = 5
	testUpstreamRPS   = 1e6
	testUpstreamBurst = 1_000_000
)

type fakeLLM struct {
	reply func(ctx context.Context, prompt string) (string, error)
}

func (f fakeLLM) Reply(ctx context.Context, prompt string) (string, error) {
	return f.reply(ctx, prompt)
}

type capturedReply struct{ to, body string }

func captureServer(t *testing.T) (*httptest.Server, <-chan capturedReply) {
	t.Helper()
	ch := make(chan capturedReply, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			To   string `json:"to"`
			Text struct {
				Body string `json:"body"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		ch <- capturedReply{p.To, p.Text.Body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.mock.cap"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func testApp(t *testing.T, cfg Config, llm LLM, target string, log *slog.Logger) *App {
	t.Helper()
	if log == nil {
		log = silentLog()
	}
	cfg.WhatsAppURL = target
	cfg.RetryBaseDelay = time.Millisecond
	cfg.RetryMaxDelay = 5 * time.Millisecond
	cfg.OutboundTimeout = workerOutboundTO
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.QueueSize < 1 {
		cfg.QueueSize = 64
	}
	def := DefaultConfig()
	if cfg.UpstreamRPS == def.UpstreamRPS && cfg.UpstreamBurst == def.UpstreamBurst {
		cfg.UpstreamRPS = testUpstreamRPS
		cfg.UpstreamBurst = testUpstreamBurst
	}
	return New(cfg, llm, NewWhatsAppSender(cfg, nil, log), log)
}

func runWorkers(t *testing.T, app *App) (<-chan struct{}, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { app.RunWorkers(ctx); close(done) }()
	return done, cancel
}

func waitFor[T any](t *testing.T, ch <-chan T, timeout time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatal("timeout waiting for channel")
	}
	var zero T
	return zero
}

func drainWorkers(t *testing.T, app *App, done <-chan struct{}, cancel context.CancelFunc) {
	t.Helper()
	app.Close()
	select {
	case <-done:
	case <-time.After(workerTestTimeout):
		t.Fatal("timeout waiting for workers")
	}
	cancel()
}

func webhookBody(id, text string) string {
	return `{"message_id":"` + id + `","from":"` + testPhone + `","text":"` + text + `"}`
}

func TestWorkerHappyPath(t *testing.T) {
	srv, out := captureServer(t)
	app := testApp(t, DefaultConfig(), fakeLLM{reply: func(context.Context, string) (string, error) { return fakeReplyBody, nil }}, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	if postWebhook(app, webhookBody("wamid.ok", testText)).Code != http.StatusAccepted {
		t.Fatal("webhook rejected")
	}
	got := waitFor(t, out, workerTestTimeout)
	drainWorkers(t, app, done, cancel)
	if got.to != testPhone || got.body != fakeReplyBody || app.metrics.Processed.Load() != 1 || app.metrics.InFlight.Load() != 0 || app.dlq.Len() != 0 {
		t.Fatalf("got=%#v processed=%d inflight=%d dlq=%d", got, app.metrics.Processed.Load(), app.metrics.InFlight.Load(), app.dlq.Len())
	}
}

func TestWorkerLLMTransientThenSuccess(t *testing.T) {
	srv, out := captureServer(t)
	var calls atomic.Int32
	llm := fakeLLM{reply: func(context.Context, string) (string, error) {
		if calls.Add(1) <= 2 {
			return "", errLLMUnavailable
		}
		return fakeReplyBody, nil
	}}
	cfg := DefaultConfig()
	cfg.MaxAttempts = 5
	app := testApp(t, cfg, llm, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.t", "oi"))
	_ = waitFor(t, out, workerTestTimeout)
	drainWorkers(t, app, done, cancel)
	if app.metrics.Processed.Load() != 1 {
		t.Fatalf("processed = %d", app.metrics.Processed.Load())
	}
}

func TestWorkerLLMAlwaysFails(t *testing.T) {
	srv, out := captureServer(t)
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	app := testApp(t, cfg, fakeLLM{reply: func(context.Context, string) (string, error) { return "", errLLMUnavailable }}, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.fail", testText))
	drainWorkers(t, app, done, cancel)
	item := app.dlq.List()[0]
	if app.metrics.FailedLLM.Load() != 1 || item.Stage != "llm" || item.Attempts != 3 || !strings.Contains(item.Error, "attempts") {
		t.Fatalf("item=%#v failed_llm=%d", item, app.metrics.FailedLLM.Load())
	}
	select {
	case <-out:
		t.Fatal("outbound should not be called")
	default:
	}
}

func TestWorkerLLMPermanent(t *testing.T) {
	srv, _ := captureServer(t)
	cfg := DefaultConfig()
	cfg.MaxAttempts = 5
	app := testApp(t, cfg, fakeLLM{reply: func(context.Context, string) (string, error) { return "", permanent(errBoom) }}, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.perm", "x"))
	drainWorkers(t, app, done, cancel)
	if app.dlq.List()[0].Attempts != 1 {
		t.Fatalf("attempts = %d", app.dlq.List()[0].Attempts)
	}
}

func TestWorkerLLMTooSlow(t *testing.T) {
	srv, _ := captureServer(t)
	llm := fakeLLM{reply: func(ctx context.Context, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }}
	cfg := DefaultConfig()
	cfg.LLMTimeout, cfg.MaxAttempts = workerSlowLLMTO, 2
	app := testApp(t, cfg, llm, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	start := time.Now()
	postWebhook(app, webhookBody("wamid.slow", "x"))
	drainWorkers(t, app, done, cancel)
	item := app.dlq.List()[0]
	if time.Since(start) >= time.Second || item.Stage != "llm" || item.Attempts != 2 || !strings.Contains(item.Error, "deadline") {
		t.Fatalf("elapsed=%v item=%#v", time.Since(start), item)
	}
}

func TestWorkerOutboundPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	app := testApp(t, DefaultConfig(), fakeLLM{reply: func(context.Context, string) (string, error) { return fakeReplyBody, nil }}, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.400", "x"))
	drainWorkers(t, app, done, cancel)
	if app.dlq.List()[0].Stage != "outbound" || app.metrics.FailedOutbound.Load() != 1 {
		t.Fatalf("stage=%q failed_outbound=%d", app.dlq.List()[0].Stage, app.metrics.FailedOutbound.Load())
	}
}

func TestWorkerFailureLogsNoPII(t *testing.T) {
	var buf bytes.Buffer
	srv, _ := captureServer(t)
	cfg := DefaultConfig()
	cfg.MaxAttempts = 3
	app := testApp(t, cfg, fakeLLM{reply: func(context.Context, string) (string, error) { return "", errLLMUnavailable }}, srv.URL, slog.New(slog.NewTextHandler(&buf, nil)))
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.pii2", testText))
	drainWorkers(t, app, done, cancel)
	out := buf.String()
	if strings.Contains(out, testPhone) || strings.Contains(out, testText) || !strings.Contains(out, "wamid.pii2") {
		t.Fatalf("log = %s", out)
	}
}

func TestWorkerGracefulDrain(t *testing.T) {
	srv, out := captureServer(t)
	llm := fakeLLM{reply: func(ctx context.Context, _ string) (string, error) {
		timer := time.NewTimer(workerLLMSleep)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return fakeReplyBody, nil
		}
	}}
	cfg := DefaultConfig()
	cfg.Workers, cfg.QueueSize, cfg.MaxAttempts = 4, gracefulJobCount, 1
	app := testApp(t, cfg, llm, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	for i := range gracefulJobCount {
		if postWebhook(app, webhookBody("wamid.g."+strconv.Itoa(i), "x")).Code != http.StatusAccepted {
			t.Fatalf("enqueue %d failed", i)
		}
	}
	drainWorkers(t, app, done, cancel)
	for range gracefulJobCount {
		_ = waitFor(t, out, workerTestTimeout)
	}
	if app.metrics.Processed.Load() != gracefulJobCount {
		t.Fatalf("processed = %d", app.metrics.Processed.Load())
	}
	if _, ok := <-app.q.jobs(); ok {
		t.Fatal("queue channel still open")
	}
}

func TestWorkerHardStop(t *testing.T) {
	srv, _ := captureServer(t)
	started := make(chan struct{})
	llm := fakeLLM{reply: func(ctx context.Context, _ string) (string, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	cfg := DefaultConfig()
	cfg.MaxAttempts, cfg.LLMTimeout = 3, time.Minute
	app := testApp(t, cfg, llm, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	postWebhook(app, webhookBody("wamid.hard", "x"))
	waitFor(t, started, workerTestTimeout)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hard stop timeout")
	}
	if app.dlq.Len() != 1 || !strings.Contains(app.dlq.List()[0].Error, "cancel") {
		t.Fatalf("dlq = %#v", app.dlq.List())
	}
}

func eventually(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timeout waiting for condition")
}

func TestWorkerBreakerTripsToDLQ(t *testing.T) {
	srv, _ := captureServer(t)
	var calls atomic.Int32
	llm := fakeLLM{reply: func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", errLLMUnavailable
	}}
	cfg := DefaultConfig()
	cfg.BreakerFailures = 2
	cfg.BreakerCooldown = time.Hour
	cfg.MaxAttempts = 1
	cfg.Workers = 1
	app := testApp(t, cfg, llm, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	for i := range 3 {
		if postWebhook(app, webhookBody("wamid.brk."+strconv.Itoa(i), "x")).Code != http.StatusAccepted {
			t.Fatalf("enqueue %d failed", i)
		}
	}
	eventually(t, func() bool { return app.dlq.Len() == 3 }, workerTestTimeout)
	drainWorkers(t, app, done, cancel)
	if got := calls.Load(); got != 2 {
		t.Fatalf("inner calls = %d, want 2", got)
	}
	letters := app.dlq.List()
	third := letters[2]
	if !strings.Contains(third.Error, "circuit open") || third.Attempts != 1 {
		t.Fatalf("third letter = %#v", third)
	}
	if got := app.Metrics()["breaker_state"]; got != 1 {
		t.Fatalf("breaker_state = %d, want 1", got)
	}
}

func TestAbandonPendingAfterHardStop(t *testing.T) {
	srv, _ := captureServer(t)
	started := make(chan struct{})
	llm := fakeLLM{reply: func(ctx context.Context, _ string) (string, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	cfg := DefaultConfig()
	cfg.Workers, cfg.MaxAttempts, cfg.LLMTimeout = 1, 3, time.Minute
	app := testApp(t, cfg, llm, srv.URL, nil)
	done, cancel := runWorkers(t, app)
	for i := range abandonJobCount {
		postWebhook(app, webhookBody("wamid.abandon."+strconv.Itoa(i), "x"))
	}
	waitFor(t, started, workerTestTimeout)
	cancel()
	waitFor(t, done, workerTestTimeout)
	app.Close()

	app.abandonPending()
	letters := app.dlq.List()
	if len(letters) != abandonJobCount {
		t.Fatalf("dlq len = %d, want %d", len(letters), abandonJobCount)
	}
	shutdown, canceled := 0, 0
	for _, l := range letters {
		switch {
		case l.Stage == "shutdown" && l.Error == "aborted by shutdown":
			shutdown++
		case l.Stage == "llm" && strings.Contains(l.Error, "cancel"):
			canceled++
		default:
			t.Fatalf("unexpected letter %#v", l)
		}
	}
	if shutdown != abandonJobCount-1 || canceled != 1 {
		t.Fatalf("shutdown = %d, canceled = %d", shutdown, canceled)
	}
	if snap := app.metrics.Snapshot(); snap["abandoned"] != abandonJobCount-1 || snap["queue_depth"] != 0 {
		t.Fatalf("abandoned = %d, queue_depth = %d", snap["abandoned"], snap["queue_depth"])
	}
}
