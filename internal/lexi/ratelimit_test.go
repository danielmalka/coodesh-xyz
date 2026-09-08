package lexi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const (
	inboundRecoverySleep   = 20 * time.Millisecond
	upstreamLLMTimeout     = 30 * time.Millisecond
	outboundLimiterTimeout = 30 * time.Millisecond
	inboundRecoveryRPS     = 200
	retryAfterScaleRPS     = 0.25
	dlqPollInterval        = time.Millisecond
)

func TestInboundRateLimitRejectDoesNotCountReceived(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InboundRPS = 1
	cfg.InboundBurst = 2
	cfg.Workers = 0
	cfg.QueueSize = 8
	app := New(cfg, nil, nil, silentLog())

	for i := range 3 {
		rec := postWebhook(app, webhookBody("wamid.rl."+strconv.Itoa(i), "x"))
		switch i {
		case 0, 1:
			if rec.Code != http.StatusAccepted {
				t.Fatalf("post %d status = %d, want 202", i, rec.Code)
			}
		default:
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("post %d status = %d, want 429", i, rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
			if !strings.Contains(rec.Body.String(), "rate limited") {
				t.Fatalf("body = %s", rec.Body.String())
			}
		}
	}
	snap := app.metrics.Snapshot()
	if snap["rate_limited"] != 1 || snap["received"] != 2 {
		t.Fatalf("rate_limited=%d received=%d (rejected must not count as received)", snap["rate_limited"], snap["received"])
	}
}

func TestInboundRateLimitRecoversAfterTokenRefill(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InboundRPS = inboundRecoveryRPS
	cfg.InboundBurst = 1
	cfg.QueueSize = 8
	app := New(cfg, nil, nil, silentLog())

	if rec := postWebhook(app, webhookBody("wamid.rec.1", "x")); rec.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", rec.Code)
	}
	if rec := postWebhook(app, webhookBody("wamid.rec.2", "x")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", rec.Code)
	}
	time.Sleep(inboundRecoverySleep)
	if rec := postWebhook(app, webhookBody("wamid.rec.3", "x")); rec.Code != http.StatusAccepted {
		t.Fatalf("third status = %d, want 202 after refill", rec.Code)
	}
}

func TestInboundRetryAfterScalesWithRPS(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InboundRPS = retryAfterScaleRPS
	cfg.InboundBurst = 1
	cfg.QueueSize = 8
	app := New(cfg, nil, nil, silentLog())

	if rec := postWebhook(app, webhookBody("wamid.ra.1", "x")); rec.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", rec.Code)
	}
	rec := postWebhook(app, webhookBody("wamid.ra.2", "x"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "4" {
		t.Fatalf("Retry-After = %q, want 4", got)
	}
}

func TestUpstreamLimiterSharedPacesLLM(t *testing.T) {
	srv, out := captureServer(t)
	cfg := DefaultConfig()
	cfg.UpstreamRPS = 1
	cfg.UpstreamBurst = 2
	cfg.LLMTimeout = upstreamLLMTimeout
	cfg.MaxAttempts = 1
	cfg.Workers = 1
	cfg.QueueSize = 8
	app := testApp(t, cfg, fakeLLM{reply: func(context.Context, string) (string, error) {
		return fakeReplyBody, nil
	}}, srv.URL, nil)
	done, cancel := runWorkers(t, app)

	if postWebhook(app, webhookBody("wamid.up.1", "x")).Code != http.StatusAccepted {
		t.Fatal("first webhook rejected")
	}
	if postWebhook(app, webhookBody("wamid.up.2", "x")).Code != http.StatusAccepted {
		t.Fatal("second webhook rejected")
	}

	_ = waitFor(t, out, workerTestTimeout)
	waitUntil(t, workerTestTimeout, func() bool {
		return app.dlq.Len() >= 1 && app.metrics.FailedLLM.Load() >= 1
	})
	drainWorkers(t, app, done, cancel)

	if app.dlq.Len() != 1 || app.metrics.Processed.Load() != 1 {
		t.Fatalf("dlq=%d processed=%d", app.dlq.Len(), app.metrics.Processed.Load())
	}
	item := app.dlq.List()[0]
	if item.Stage != "llm" {
		t.Fatalf("stage = %q, want llm", item.Stage)
	}
	errText := strings.ToLower(item.Error)
	if !strings.Contains(errText, "deadline") && !strings.Contains(errText, "exceed") {
		t.Fatalf("error = %q, want deadline/exceed", item.Error)
	}
}

func TestOutboundLimiterHonorsContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := testSenderConfig(srv.URL, 1)
	sender := NewWhatsAppSender(cfg, nil, silentLog())
	sender.limiter = rate.NewLimiter(1, 1)

	if _, err := sender.Send(t.Context(), OutboundMessage{MessageID: "wamid.ol.1", To: testPhone, Text: testText}); err != nil {
		t.Fatalf("first Send = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), outboundLimiterTimeout)
	defer cancel()
	_, err := sender.Send(ctx, OutboundMessage{MessageID: "wamid.ol.2", To: testPhone, Text: testText})
	if err == nil {
		t.Fatal("second Send = nil, want error")
	}
	if isPermanent(err) {
		t.Fatalf("isPermanent = true, want false: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func waitUntil(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(dlqPollInterval)
	}
	t.Fatal("timeout waiting for condition")
}
