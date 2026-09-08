package lexi

import (
	"net/http"
	"sync"
	"time"
)

// ponytail: in-memory ring; production uses a durable broker DLQ with replay tooling.
const maxDeadLetters = 1000

type DeadLetter struct {
	MessageID string
	From      string
	Text      string
	Stage     string
	Error     string
	Attempts  int
	FailedAt  time.Time
}

type DeadLetterQueue struct {
	mu      sync.Mutex
	items   []DeadLetter
	dropped int
}

func (d *DeadLetterQueue) add(letter DeadLetter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.items) >= maxDeadLetters {
		d.items = d.items[1:]
		d.dropped++
	}
	d.items = append(d.items, letter)
}

func (d *DeadLetterQueue) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

func (d *DeadLetterQueue) Dropped() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

func (d *DeadLetterQueue) List() []DeadLetter {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DeadLetter, len(d.items))
	copy(out, d.items)
	return out
}

type deadLetterView struct {
	MessageID string    `json:"message_id"`
	From      string    `json:"from"`
	Chars     int       `json:"chars"`
	Stage     string    `json:"stage"`
	Error     string    `json:"error"`
	Attempts  int       `json:"attempts"`
	FailedAt  time.Time `json:"failed_at"`
}

func (a *App) handleDLQ(w http.ResponseWriter, _ *http.Request) {
	items := a.dlq.List()
	views := make([]deadLetterView, len(items))
	for i, item := range items {
		views[i] = deadLetterView{
			MessageID: item.MessageID,
			From:      hashPhone(item.From),
			Chars:     len(item.Text),
			Stage:     item.Stage,
			Error:     item.Error,
			Attempts:  item.Attempts,
			FailedAt:  item.FailedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count":   len(views),
		"dropped": a.dlq.Dropped(),
		"items":   views,
	})
}
