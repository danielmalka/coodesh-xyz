package lexi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeadLetterQueueAddListOrder(t *testing.T) {
	d := &DeadLetterQueue{}
	d.add(DeadLetter{MessageID: "a", FailedAt: time.Unix(1, 0)})
	d.add(DeadLetter{MessageID: "b", FailedAt: time.Unix(2, 0)})
	got := d.List()
	if len(got) != 2 || got[0].MessageID != "a" || got[1].MessageID != "b" {
		t.Fatalf("list = %#v", got)
	}
	got[0].MessageID = "mutated"
	if d.List()[0].MessageID != "a" {
		t.Fatal("List did not return a copy")
	}
}

func TestDeadLetterQueueEvictsOldest(t *testing.T) {
	d := &DeadLetterQueue{}
	for i := range maxDeadLetters + 5 {
		d.add(DeadLetter{MessageID: strconv.Itoa(i)})
	}
	if d.Len() != maxDeadLetters {
		t.Fatalf("len = %d, want %d", d.Len(), maxDeadLetters)
	}
	if d.Dropped() != 5 {
		t.Fatalf("dropped = %d, want 5", d.Dropped())
	}
	items := d.List()
	if items[0].MessageID != strconv.Itoa(5) {
		t.Fatalf("oldest kept = %q, want %q", items[0].MessageID, strconv.Itoa(5))
	}
}

func TestHandleDLQJSONIsPIISafe(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueSize = 8
	app := New(cfg, nil, nil, silentLog())
	phone := "+5511987654321"
	text := "divida secreta"
	app.dlq.add(DeadLetter{
		MessageID: "wamid.dlq",
		From:      phone,
		Text:      text,
		Stage:     "llm",
		Error:     "boom",
		Attempts:  3,
		FailedAt:  time.Unix(100, 0).UTC(),
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dlq", nil)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, phone) {
		t.Fatalf("body leaked phone: %s", body)
	}
	if strings.Contains(body, text) {
		t.Fatalf("body leaked text: %s", body)
	}
	var resp struct {
		Count   int              `json:"count"`
		Dropped int              `json:"dropped"`
		Items   []deadLetterView `json:"items"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || len(resp.Items) != 1 {
		t.Fatalf("resp = %#v", resp)
	}
	if resp.Items[0].From != hashPhone(phone) {
		t.Fatalf("from = %q, want hashed", resp.Items[0].From)
	}
	if resp.Items[0].Chars != len(text) {
		t.Fatalf("chars = %d, want %d", resp.Items[0].Chars, len(text))
	}
}
