package lexi

import (
	"net/http"
	"sync/atomic"
)

type Metrics struct {
	Received       atomic.Int64
	Accepted       atomic.Int64
	Duplicate      atomic.Int64
	QueueFull      atomic.Int64
	QueueClosed    atomic.Int64
	Abandoned      atomic.Int64
	RateLimited    atomic.Int64
	Processed      atomic.Int64
	FailedLLM      atomic.Int64
	FailedOutbound atomic.Int64
	InFlight       atomic.Int64
}

func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"received":        m.Received.Load(),
		"accepted":        m.Accepted.Load(),
		"duplicate":       m.Duplicate.Load(),
		"queue_full":      m.QueueFull.Load(),
		"queue_closed":    m.QueueClosed.Load(),
		"abandoned":       m.Abandoned.Load(),
		"rate_limited":    m.RateLimited.Load(),
		"processed":       m.Processed.Load(),
		"failed_llm":      m.FailedLLM.Load(),
		"failed_outbound": m.FailedOutbound.Load(),
		"in_flight":       m.InFlight.Load(),
	}
}

func (a *App) metricsSnapshot() map[string]int64 {
	s := a.metrics.Snapshot()
	s["dead_letters"] = int64(a.dlq.Len())
	s["queue_depth"] = int64(len(a.q.ch))
	s["breaker_state"] = int64(a.breaker.State())
	return s
}

func (a *App) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.metricsSnapshot())
}
