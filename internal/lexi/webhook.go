package lexi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const maxBodyBytes = 1 << 20

type inbound struct {
	MessageID string `json:"message_id"`
	From      string `json:"from"`
	Text      string `json:"text"`
}

func (a *App) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// ponytail: global limiter; per-sender keyed limiters if fairness across customers matters.
	if !a.inbound.Allow() {
		a.metrics.RateLimited.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(a.cfg.retryAfterSeconds()))
		a.log.Warn("webhook rate limited")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var in inbound
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}

	in.MessageID = strings.TrimSpace(in.MessageID)
	in.From = strings.TrimSpace(in.From)
	in.Text = strings.TrimSpace(in.Text)
	if in.MessageID == "" || in.From == "" || in.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}

	a.metrics.Received.Add(1)
	switch a.q.push(Job(in)) {
	case duplicate:
		a.metrics.Duplicate.Add(1)
		a.log.Info("webhook duplicate", "message_id", in.MessageID)
		writeJSON(w, http.StatusAccepted, map[string]any{"received": true, "message_id": in.MessageID})
	case full:
		a.metrics.QueueFull.Add(1)
		a.log.Error("webhook queue full", "message_id", in.MessageID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
	case closed:
		a.metrics.QueueClosed.Add(1)
		a.log.Warn("webhook rejected, queue closed", "message_id", in.MessageID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "shutting down"})
	default:
		a.metrics.Accepted.Add(1)
		a.log.Info("webhook accepted", "message_id", in.MessageID, "from", hashPhone(in.From))
		writeJSON(w, http.StatusAccepted, map[string]any{"received": true, "message_id": in.MessageID})
	}
}
