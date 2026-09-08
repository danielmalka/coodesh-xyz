package lexi

import "sync"

// ponytail: fixed dedup window; production dedups at the broker or with a TTL cache.
const dedupWindow = 10_000

type Job struct {
	MessageID string
	From      string
	Text      string
	Attempts  int
}

type enqueueResult int

const (
	enqueued enqueueResult = iota
	duplicate
	full
)

type Queue struct {
	ch   chan Job
	mu   sync.Mutex
	seen map[string]struct{}
	ring []string
	next int
}

func NewQueue(size int) *Queue {
	if size < 1 {
		size = 64
	}
	return &Queue{
		ch:   make(chan Job, size),
		seen: make(map[string]struct{}, dedupWindow),
		ring: make([]string, dedupWindow),
	}
}

func (q *Queue) push(j Job) enqueueResult {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.seen[j.MessageID]; ok {
		return duplicate
	}
	select {
	case q.ch <- j:
		q.remember(j.MessageID)
		return enqueued
	default:
		return full
	}
}

func (q *Queue) remember(id string) {
	if old := q.ring[q.next]; old != "" {
		delete(q.seen, old)
	}
	q.ring[q.next] = id
	q.next = (q.next + 1) % len(q.ring)
	q.seen[id] = struct{}{}
}

func (q *Queue) jobs() <-chan Job {
	return q.ch
}
