package lexi

import (
	"encoding/json"
	"net/http"
	"strings"
)

type inbound struct {
	MessageID string `json:"message_id"`
	From      string `json:"from"`
	Text      string `json:"text"`
}

func (a *App) handleWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

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

	job := Job{MessageID: in.MessageID, From: in.From, Text: in.Text}
	switch a.q.push(job) {
	case duplicate:
		a.log.Info("webhook duplicate", "message_id", in.MessageID)
		writeJSON(w, http.StatusAccepted, map[string]any{"received": true, "message_id": in.MessageID})
	case full:
		a.log.Error("webhook queue full", "message_id", in.MessageID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
	default:
		a.log.Info("webhook accepted", "message_id", in.MessageID, "from", hashFrom(in.From))
		writeJSON(w, http.StatusAccepted, map[string]any{"received": true, "message_id": in.MessageID})
	}
}
