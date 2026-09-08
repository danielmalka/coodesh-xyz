package lexi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type App struct {
	q   *Queue
	log *slog.Logger
}

func New(q *Queue, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	return &App{q: q, log: log}
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", a.handleWebhook)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
