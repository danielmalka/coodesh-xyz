package lexi

import (
	"context"
	"errors"
	"sync"
	"time"
)

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

func (s breakerState) String() string {
	switch s {
	case breakerClosed:
		return "closed"
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

var errBreakerOpen = errors.New("llm: circuit open")

type Breaker struct {
	mu         sync.Mutex
	state      breakerState
	generation uint64
	failures   int
	openedAt   time.Time
	probing    bool
	threshold  int
	cooldown   time.Duration
}

func newBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

func (b *Breaker) allow() (generation uint64, ok bool) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return b.generation, true
	case breakerOpen:
		if now.Sub(b.openedAt) < b.cooldown {
			return b.generation, false
		}
		b.state = breakerHalfOpen
		b.probing = true
		return b.generation, true
	case breakerHalfOpen:
		if b.probing {
			return b.generation, false
		}
		b.probing = true
		return b.generation, true
	default:
		return b.generation, true
	}
}

func (b *Breaker) record(generation uint64, err error) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if generation != b.generation {
		return
	}
	if err == nil {
		b.state = breakerClosed
		b.failures = 0
		b.probing = false
		return
	}
	if errors.Is(err, context.Canceled) {
		if b.state == breakerHalfOpen {
			b.probing = false
		}
		return
	}
	switch b.state {
	case breakerHalfOpen:
		b.open(now)
	case breakerClosed:
		b.failures++
		if b.failures >= b.threshold {
			b.open(now)
		}
	}
}

func (b *Breaker) open(now time.Time) {
	b.state = breakerOpen
	b.openedAt = now
	b.failures = 0
	b.probing = false
	b.generation++
}

func (b *Breaker) current() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.generation
}

func (b *Breaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

type breakerLLM struct {
	inner   LLM
	breaker *Breaker
}

func (w *breakerLLM) Reply(ctx context.Context, prompt string) (string, error) {
	generation, ok := w.breaker.allow()
	if !ok {
		return "", permanent(errBreakerOpen)
	}
	reply, err := w.inner.Reply(ctx, prompt)
	w.breaker.record(generation, err)
	return reply, err
}

func withBreaker(inner LLM, threshold int, cooldown time.Duration) LLM {
	return &breakerLLM{inner: inner, breaker: newBreaker(threshold, cooldown)}
}
