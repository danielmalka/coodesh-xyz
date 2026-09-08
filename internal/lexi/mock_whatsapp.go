package lexi

import (
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
)

const (
	mockRoutePattern = "POST /mock/whatsapp/messages"
	mockIDPrefix     = "wamid.mock."
)

type mockWhatsAppText struct {
	Body string `json:"body"`
}

type mockWhatsAppRequest struct {
	To   string           `json:"to"`
	Text mockWhatsAppText `json:"text"`
}

type mockWhatsAppMessage struct {
	ID string `json:"id"`
}

type mockWhatsAppResponse struct {
	Messages []mockWhatsAppMessage `json:"messages"`
}

type mockErrorResponse struct {
	Error string `json:"error"`
}

func (a *App) handleMockWhatsApp(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var payload mockWhatsAppRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, mockErrorResponse{Error: "invalid payload"})
		return
	}
	if payload.To == "" || payload.Text.Body == "" {
		writeJSON(w, http.StatusBadRequest, mockErrorResponse{Error: "invalid payload"})
		return
	}
	if a.cfg.MockWhatsAppFailureRate > 0 && rand.Float64() < a.cfg.MockWhatsAppFailureRate {
		writeJSON(w, http.StatusServiceUnavailable, mockErrorResponse{Error: "simulated outage"})
		return
	}
	n := a.mockCounter.Add(1)
	a.log.Info("mock whatsapp delivered", slog.String("to", hashPhone(payload.To)), slog.Int("chars", len(payload.Text.Body)))
	writeJSON(w, http.StatusOK, mockWhatsAppResponse{
		Messages: []mockWhatsAppMessage{{ID: mockIDPrefix + strconv.FormatUint(n, 10)}},
	})
}
