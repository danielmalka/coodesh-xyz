package lexi

import (
	"encoding/json"
	"expvar"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sync/atomic"

	"golang.org/x/time/rate"
)

type App struct {
	cfg         Config
	q           *Queue
	dlq         *DeadLetterQueue
	metrics     *Metrics
	llm         LLM
	sender      *WhatsAppSender
	log         *slog.Logger
	inbound     *rate.Limiter
	upstream    *rate.Limiter
	mockCounter atomic.Uint64
}

func New(cfg Config, llm LLM, sender *WhatsAppSender, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	a := &App{
		cfg:      cfg,
		q:        NewQueue(cfg.QueueSize),
		dlq:      &DeadLetterQueue{},
		metrics:  &Metrics{},
		llm:      llm,
		sender:   sender,
		log:      log,
		inbound:  cfg.inboundLimiter(),
		upstream: cfg.upstreamLimiter(),
	}
	if sender != nil {
		sender.limiter = a.upstream
	}
	return a
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", a.handleWebhook)
	mux.HandleFunc(mockRoutePattern, a.handleMockWhatsApp)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /dlq", a.handleDLQ)
	mux.HandleFunc("GET /metrics", a.handleMetrics)
	mux.Handle("GET /debug/vars", expvar.Handler())
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}

func (a *App) Close() {
	a.q.Close()
}

func (a *App) AbandonPending() int {
	return a.abandonPending()
}

func (a *App) Metrics() map[string]int64 {
	return a.metricsSnapshot()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
