package lexi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testRetryBaseDelay   = time.Millisecond
	testRetryMaxDelay    = 5 * time.Millisecond
	testMaxAttempts      = 3
	testClientTimeout    = 20 * time.Millisecond
	testSlowHandlerDelay = 200 * time.Millisecond
	testPhone            = "+5511987654321"
	testText             = "sua proposta de acordo esta pronta"
	testMessageID        = "wamid.test.1"
	testToken            = "t0k"
)

type capturedOutbound struct {
	method      string
	contentType string
	auth        string
	product     string
	to          string
	msgType     string
	body        string
}

type outboundCapture struct {
	got capturedOutbound
	err error
}

func testSenderConfig(target string, maxAttempts int) Config {
	cfg := DefaultConfig()
	cfg.WhatsAppURL = target
	cfg.MaxAttempts = maxAttempts
	cfg.RetryBaseDelay = testRetryBaseDelay
	cfg.RetryMaxDelay = testRetryMaxDelay
	cfg.OutboundTimeout = testClientTimeout
	return cfg
}

func decodeOutbound(r *http.Request) (capturedOutbound, error) {
	var payload struct {
		MessagingProduct string `json:"messaging_product"`
		To               string `json:"to"`
		Type             string `json:"type"`
		Text             struct {
			Body string `json:"body"`
		} `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return capturedOutbound{}, err
	}
	return capturedOutbound{
		method:      r.Method,
		contentType: r.Header.Get("Content-Type"),
		auth:        r.Header.Get("Authorization"),
		product:     payload.MessagingProduct,
		to:          payload.To,
		msgType:     payload.Type,
		body:        payload.Text.Body,
	}, nil
}

func TestWhatsAppSenderSuccess(t *testing.T) {
	var calls atomic.Int32
	captured := make(chan outboundCapture, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		got, err := decodeOutbound(r)
		captured <- outboundCapture{got: got, err: err}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testSenderConfig(srv.URL, testMaxAttempts)
	sender := NewWhatsAppSender(cfg, nil, silentLog())
	msg := OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText}
	attempts, err := sender.Send(t.Context(), msg)
	if err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	cap := <-captured
	if cap.err != nil {
		t.Fatalf("decode body: %v", cap.err)
	}
	got := cap.got
	if got.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", got.method)
	}
	if got.contentType != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got.contentType)
	}
	if got.product != "whatsapp" || got.to != testPhone || got.msgType != "text" || got.body != testText {
		t.Fatalf("payload = %+v, want product whatsapp to %q type text body %q", got, testPhone, testText)
	}
	if got.auth != "" {
		t.Fatalf("auth = %q, want empty without token", got.auth)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want %d", attempts, 1)
	}
}

func TestWhatsAppSenderBearerToken(t *testing.T) {
	captured := make(chan outboundCapture, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := decodeOutbound(r)
		captured <- outboundCapture{got: got, err: err}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testSenderConfig(srv.URL, testMaxAttempts)
	cfg.WhatsAppToken = testToken
	sender := NewWhatsAppSender(cfg, nil, silentLog())
	msg := OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText}
	attempts, err := sender.Send(t.Context(), msg)
	if err != nil {
		t.Fatalf("Send with token = %v, want nil", err)
	}
	cap := <-captured
	if cap.err != nil {
		t.Fatalf("decode body: %v", cap.err)
	}
	if cap.got.auth != "Bearer "+testToken {
		t.Fatalf("auth = %q, want Bearer token", cap.got.auth)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want %d", attempts, 1)
	}
}

func TestWhatsAppSenderTransientThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < testMaxAttempts {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewWhatsAppSender(testSenderConfig(srv.URL, testMaxAttempts), nil, silentLog())
	attempts, err := sender.Send(t.Context(), OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText})
	if err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	if calls.Load() != testMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls.Load(), testMaxAttempts)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want %d", attempts, 3)
	}
}

func TestWhatsAppSenderRetriesTooManyRequests(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < testMaxAttempts {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewWhatsAppSender(testSenderConfig(srv.URL, testMaxAttempts), nil, silentLog())
	if _, err := sender.Send(t.Context(), OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText}); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	if calls.Load() != testMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls.Load(), testMaxAttempts)
	}
}

func TestWhatsAppSenderPermanentBadRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	sender := NewWhatsAppSender(testSenderConfig(srv.URL, testMaxAttempts), nil, silentLog())
	attempts, err := sender.Send(t.Context(), OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText})
	if err == nil {
		t.Fatal("Send = nil, want permanent error")
	}
	if !isPermanent(err) {
		t.Fatalf("isPermanent(err) = false, want true: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want %d", attempts, 1)
	}
}

func TestWhatsAppSenderExhaustion(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sender := NewWhatsAppSender(testSenderConfig(srv.URL, testMaxAttempts), nil, silentLog())
	attempts, err := sender.Send(t.Context(), OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText})
	if err == nil {
		t.Fatal("Send = nil, want exhaustion error")
	}
	if calls.Load() != testMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls.Load(), testMaxAttempts)
	}
	if !strings.Contains(err.Error(), "attempts") {
		t.Fatalf("err = %q, want it to mention attempts", err)
	}
	if attempts != testMaxAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, testMaxAttempts)
	}
}

func TestWhatsAppSenderPreCanceledContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sender := NewWhatsAppSender(testSenderConfig(srv.URL, testMaxAttempts), nil, silentLog())
	attempts, err := sender.Send(ctx, OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapping context.Canceled", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d, want 0", calls.Load())
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want %d", attempts, 0)
	}
}

func TestWhatsAppSenderAttemptTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-time.After(testSlowHandlerDelay):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	noTimeoutClient := &http.Client{}
	sender := NewWhatsAppSender(testSenderConfig(srv.URL, testMaxAttempts), noTimeoutClient, silentLog())
	start := time.Now()
	attempts, err := sender.Send(t.Context(), OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText})
	if err == nil {
		t.Fatal("Send = nil, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping context.DeadlineExceeded", err)
	}
	if isPermanent(err) {
		t.Fatalf("isPermanent(err) = true, want false: %v", err)
	}
	if calls.Load() != testMaxAttempts {
		t.Fatalf("calls = %d, want %d", calls.Load(), testMaxAttempts)
	}
	if elapsed := time.Since(start); elapsed >= testSlowHandlerDelay {
		t.Fatalf("elapsed = %v, want below the handler delay %v", elapsed, testSlowHandlerDelay)
	}
	if attempts != testMaxAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, testMaxAttempts)
	}
}

func TestWhatsAppSenderDoesNotLogPII(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	sender := NewWhatsAppSender(testSenderConfig(srv.URL, 1), nil, log)
	msg := OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText}
	if _, err := sender.Send(t.Context(), msg); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	out := buf.String()
	if strings.Contains(out, testPhone) {
		t.Fatalf("log leaked phone: %s", out)
	}
	if strings.Contains(out, testText) {
		t.Fatalf("log leaked text: %s", out)
	}
	if !strings.Contains(out, testMessageID) {
		t.Fatalf("log missing message_id: %s", out)
	}
}

func BenchmarkWhatsAppSenderSend(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewWhatsAppSender(testSenderConfig(srv.URL, 1), nil, silentLog())
	msg := OutboundMessage{MessageID: testMessageID, To: testPhone, Text: testText}
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := sender.Send(ctx, msg); err != nil {
			b.Fatal(err)
		}
	}
}
