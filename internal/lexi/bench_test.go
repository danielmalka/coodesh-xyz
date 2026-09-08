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
	benchParallelQueue = 1 << 16
	benchInboundRPS    = 1e9
	benchBreakerThresh = 5
	benchBreakerCool   = time.Minute
	mockWamidJSON      = `{"messages":[{"id":"wamid.mock.cap"}]}`
)

func BenchmarkProcessJob(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockWamidJSON))
	}))
	b.Cleanup(srv.Close)

	cfg := DefaultConfig()
	cfg.WhatsAppURL = srv.URL
	cfg.RetryBaseDelay = time.Millisecond
	cfg.RetryMaxDelay = 5 * time.Millisecond
	cfg.OutboundTimeout = workerOutboundTO
	cfg.Workers = 1
	cfg.QueueSize = 64
	cfg.UpstreamRPS = testUpstreamRPS
	cfg.UpstreamBurst = testUpstreamBurst
	llm := fakeLLM{reply: func(context.Context, string) (string, error) { return fakeReplyBody, nil }}
	app := New(cfg, llm, NewWhatsAppSender(cfg, nil, silentLog()), silentLog())
	ctx := context.Background()
	job := Job{MessageID: "wamid.bench", From: testPhone, Text: testText}
	b.ReportAllocs()
	for b.Loop() {
		app.process(ctx, job)
	}
	if got := app.metrics.Processed.Load(); got == 0 || app.dlq.Len() != 0 {
		b.Fatalf("processed = %d, dead letters = %d", got, app.dlq.Len())
	}
}

func BenchmarkWebhookParallel(b *testing.B) {
	cfg := DefaultConfig()
	cfg.QueueSize = benchParallelQueue
	cfg.InboundRPS = benchInboundRPS
	app := New(cfg, nil, nil, silentLog())
	app.inbound = rate.NewLimiter(rate.Inf, 0)
	h := app.Routes()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case _, ok := <-app.q.jobs():
				if !ok {
					return
				}
			}
		}
	}()
	b.Cleanup(func() {
		close(done)
		app.Close()
	})

	b.ReportAllocs()
	var n atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := n.Add(1)
			body := `{"message_id":"wamid.par.` + strconv.FormatInt(id, 10) + `","from":"+5511987654321","text":"x"}`
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhook", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				b.Fatalf("status = %d, want 202", rec.Code)
			}
		}
	})
}

func BenchmarkBreakerAllow(b *testing.B) {
	br := newBreaker(benchBreakerThresh, benchBreakerCool)
	b.ReportAllocs()
	for b.Loop() {
		gen, _ := br.allow()
		br.record(gen, nil)
	}
}

func BenchmarkDeadLetterAdd(b *testing.B) {
	d := &DeadLetterQueue{}
	for i := range maxDeadLetters {
		d.add(DeadLetter{MessageID: strconv.Itoa(i)})
	}
	letter := DeadLetter{MessageID: "x", From: testPhone, Text: testText, Stage: "llm", Error: "e", Attempts: 1}
	b.ReportAllocs()
	for b.Loop() {
		d.add(letter)
	}
}
