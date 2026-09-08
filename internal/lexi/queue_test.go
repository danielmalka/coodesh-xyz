package lexi

import (
	"strconv"
	"testing"
)

func TestQueueDedupWindowEvictsOldest(t *testing.T) {
	q := NewQueue(dedupWindow + 2)
	for i := range dedupWindow {
		if got := q.push(Job{MessageID: strconv.Itoa(i)}); got != enqueued {
			t.Fatalf("push %d = %v", i, got)
		}
	}
	if got := q.push(Job{MessageID: "0"}); got != duplicate {
		t.Fatalf("inside window: got %v, want duplicate", got)
	}
	if got := q.push(Job{MessageID: "fresh"}); got != enqueued {
		t.Fatalf("fresh: got %v", got)
	}
	if got := q.push(Job{MessageID: "0"}); got != enqueued {
		t.Fatalf("after eviction: got %v, want enqueued", got)
	}
	if n := len(q.seen); n != dedupWindow {
		t.Fatalf("seen size = %d, want %d", n, dedupWindow)
	}
}

func TestQueueFullDoesNotRemember(t *testing.T) {
	q := NewQueue(1)
	q.push(Job{MessageID: "a"})
	if got := q.push(Job{MessageID: "b"}); got != full {
		t.Fatalf("got %v, want full", got)
	}
	<-q.jobs()
	if got := q.push(Job{MessageID: "b"}); got != enqueued {
		t.Fatalf("retry after drain: got %v, want enqueued", got)
	}
}

func BenchmarkQueuePush(b *testing.B) {
	q := NewQueue(1)
	ids := make([]string, dedupWindow)
	for i := range ids {
		ids[i] = "wamid." + strconv.Itoa(i)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		q.push(Job{MessageID: ids[i%len(ids)]})
		<-q.jobs()
		i++
	}
}
