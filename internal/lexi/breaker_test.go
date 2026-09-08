package lexi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/time/rate"
)

func TestBreakerOpensAtThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBreaker(3, time.Second)
		b.record(b.current(), errLLMUnavailable)
		b.record(b.current(), errLLMUnavailable)
		if b.State() != breakerClosed {
			t.Fatalf("state = %v, want closed after 2 failures", b.State())
		}
		b.record(b.current(), errLLMUnavailable)
		if b.State() != breakerOpen {
			t.Fatalf("state = %v, want open after 3 failures", b.State())
		}
		if _, ok := b.allow(); ok {
			t.Fatal("allow() = true right after open, want false")
		}
	})
}

func TestBreakerSuccessResetsFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBreaker(3, time.Second)
		b.record(b.current(), errLLMUnavailable)
		b.record(b.current(), errLLMUnavailable)
		b.record(b.current(), nil)
		b.record(b.current(), errLLMUnavailable)
		b.record(b.current(), errLLMUnavailable)
		if b.State() != breakerClosed {
			t.Fatalf("state = %v, want closed after reset", b.State())
		}
	})
}

func TestBreakerCooldownAdmitsOneProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cooldown = time.Second
		b := newBreaker(1, cooldown)
		b.record(b.current(), errLLMUnavailable)
		if _, ok := b.allow(); ok {
			t.Fatal("allow() = true before cooldown, want false")
		}
		time.Sleep(cooldown)
		if _, ok := b.allow(); !ok {
			t.Fatal("allow() = false after cooldown, want true")
		}
		if _, ok := b.allow(); ok {
			t.Fatal("allow() = true while probing, want false")
		}
		if b.State() != breakerHalfOpen {
			t.Fatalf("state = %v, want half_open", b.State())
		}
	})
}

func TestBreakerProbeSuccessAndFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cooldown = time.Second
		b := newBreaker(1, cooldown)
		b.record(b.current(), errLLMUnavailable)
		time.Sleep(cooldown)
		if _, ok := b.allow(); !ok {
			t.Fatal("probe not admitted")
		}
		b.record(b.current(), nil)
		if b.State() != breakerClosed {
			t.Fatalf("state = %v after probe success, want closed", b.State())
		}

		b.record(b.current(), errLLMUnavailable)
		time.Sleep(cooldown)
		if _, ok := b.allow(); !ok {
			t.Fatal("probe not admitted before failure")
		}
		b.record(b.current(), errLLMUnavailable)
		if b.State() != breakerOpen {
			t.Fatalf("state = %v after probe failure, want open", b.State())
		}
		if _, ok := b.allow(); ok {
			t.Fatal("allow() = true immediately after reopen, want false")
		}
		time.Sleep(cooldown)
		if _, ok := b.allow(); !ok {
			t.Fatal("allow() = false after second cooldown, want true")
		}
	})
}

func TestBreakerCanceledNotFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cooldown = time.Second
		b := newBreaker(1, cooldown)
		b.record(b.current(), context.Canceled)
		if b.State() != breakerClosed {
			t.Fatalf("state = %v after canceled, want closed", b.State())
		}

		b.record(b.current(), errLLMUnavailable)
		time.Sleep(cooldown)
		if _, ok := b.allow(); !ok {
			t.Fatal("probe not admitted")
		}
		b.record(b.current(), context.Canceled)
		if b.State() != breakerHalfOpen {
			t.Fatalf("state = %v after canceled probe, want half_open", b.State())
		}
		if _, ok := b.allow(); !ok {
			t.Fatal("allow() = false after canceled probe, want true")
		}
	})
}

func TestBreakerAllowConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			cooldown   = time.Second
			goroutines = 20
		)
		b := newBreaker(1, cooldown)
		b.record(b.current(), errLLMUnavailable)
		time.Sleep(cooldown)
		ch := make(chan bool, goroutines)
		for range goroutines {
			go func() {
				_, ok := b.allow()
				ch <- ok
			}()
		}
		synctest.Wait()
		allowed := 0
		for range goroutines {
			if <-ch {
				allowed++
			}
		}
		if allowed != 1 {
			t.Fatalf("allowed = %d, want 1", allowed)
		}
	})
}

func TestBreakerLLMFailsFastWhenOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		inner := fakeLLM{reply: func(context.Context, string) (string, error) {
			calls.Add(1)
			return "", errLLMUnavailable
		}}
		llm := withBreaker(inner, 2, time.Hour)
		for range 2 {
			_, err := llm.Reply(t.Context(), "hi")
			if !errors.Is(err, errLLMUnavailable) {
				t.Fatalf("err = %v, want errLLMUnavailable", err)
			}
		}
		reply, err := llm.Reply(t.Context(), "hi")
		if reply != "" {
			t.Fatalf("reply = %q, want empty", reply)
		}
		if !errors.Is(err, errBreakerOpen) {
			t.Fatalf("err = %v, want errBreakerOpen", err)
		}
		if !isPermanent(err) {
			t.Fatal("isPermanent = false, want true")
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("inner calls = %d, want 2", got)
		}
	})
}

func TestBreakerStateString(t *testing.T) {
	cases := []struct {
		state breakerState
		want  string
	}{
		{breakerClosed, "closed"},
		{breakerOpen, "open"},
		{breakerHalfOpen, "half_open"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Fatalf("state %d String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestBreakerIgnoresStaleCompletions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBreaker(1, time.Minute)
		stale, ok := b.allow()
		if !ok {
			t.Fatal("first allow() = false")
		}
		tripping, _ := b.allow()
		b.record(tripping, errLLMUnavailable)
		if b.State() != breakerOpen {
			t.Fatalf("state = %v, want open", b.State())
		}
		b.record(stale, nil)
		if b.State() != breakerOpen {
			t.Fatalf("stale success closed the breaker: state = %v", b.State())
		}
		time.Sleep(time.Minute)
		probe, ok := b.allow()
		if !ok || b.State() != breakerHalfOpen {
			t.Fatalf("probe admitted = %v, state = %v", ok, b.State())
		}
		b.record(stale, errLLMUnavailable)
		if b.State() != breakerHalfOpen {
			t.Fatalf("stale failure changed state to %v", b.State())
		}
		b.record(probe, nil)
		if b.State() != breakerClosed {
			t.Fatalf("state = %v, want closed", b.State())
		}
	})
}

func TestBreakerOpenFailsFastBeforeLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		inner := fakeLLM{reply: func(context.Context, string) (string, error) {
			calls++
			return "", errLLMUnavailable
		}}
		limiter := rate.NewLimiter(rate.Every(time.Hour), 1)
		llm := &breakerLLM{inner: &limitedLLM{inner: inner, limiter: limiter}, breaker: newBreaker(1, time.Hour)}

		if _, err := llm.Reply(t.Context(), "hi"); !errors.Is(err, errLLMUnavailable) {
			t.Fatalf("first err = %v", err)
		}
		start := time.Now()
		_, err := llm.Reply(t.Context(), "hi")
		if !errors.Is(err, errBreakerOpen) || !isPermanent(err) {
			t.Fatalf("second err = %v, want permanent errBreakerOpen", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("open breaker waited %v on the limiter", elapsed)
		}
		if calls != 1 {
			t.Fatalf("inner calls = %d, want 1", calls)
		}
	})
}
