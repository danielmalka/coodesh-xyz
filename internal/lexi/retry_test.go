package lexi

import (
	"context"
	"errors"
	"math"
	"testing"
	"testing/synctest"
	"time"
)

var errBoom = errors.New("boom")

func TestRetrySuccessFirstAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		p := retryPolicy{maxAttempts: 3, baseDelay: 10 * time.Millisecond, maxDelay: time.Second}
		err := retry(t.Context(), p, func(context.Context) error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})
}

func TestRetryTransientThenSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		p := retryPolicy{maxAttempts: 5, baseDelay: 10 * time.Millisecond, maxDelay: time.Second}
		err := retry(t.Context(), p, func(context.Context) error {
			calls++
			if calls < 3 {
				return errBoom
			}
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if calls != 3 {
			t.Fatalf("calls = %d, want 3", calls)
		}
	})
}

func TestRetryExhaustion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		p := retryPolicy{maxAttempts: 4, baseDelay: 10 * time.Millisecond, maxDelay: time.Second}
		err := retry(t.Context(), p, func(context.Context) error {
			calls++
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want wrapping errBoom", err)
		}
		if calls != p.maxAttempts {
			t.Fatalf("calls = %d, want %d", calls, p.maxAttempts)
		}
		if got := err.Error(); got == "" {
			t.Fatalf("empty error message")
		}
	})
}

func TestRetryPermanentStopsImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		p := retryPolicy{maxAttempts: 5, baseDelay: 10 * time.Millisecond, maxDelay: time.Second}
		err := retry(t.Context(), p, func(context.Context) error {
			calls++
			return permanent(errBoom)
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want wrapping errBoom", err)
		}
		if !isPermanent(err) {
			t.Fatalf("isPermanent(err) = false, want true")
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})
}

func TestRetryContextCanceledDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		p := retryPolicy{maxAttempts: 5, baseDelay: 10 * time.Second, maxDelay: time.Minute}
		done := make(chan error, 1)
		go func() {
			done <- retry(ctx, p, func(context.Context) error {
				calls++
				return errBoom
			})
		}()
		synctest.Wait()
		cancel()
		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want wrapping context.Canceled", err)
		}
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want wrapping errBoom", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})
}

func TestRetryPreCanceledContextSkipsFn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		calls := 0
		p := retryPolicy{maxAttempts: 3, baseDelay: 10 * time.Millisecond, maxDelay: time.Second}
		err := retry(ctx, p, func(context.Context) error {
			calls++
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want wrapping context.Canceled", err)
		}
		if calls != 0 {
			t.Fatalf("calls = %d, want 0", calls)
		}
	})
}

func TestRetryZeroMaxAttemptsStillCallsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		p := retryPolicy{maxAttempts: 0, baseDelay: 10 * time.Millisecond, maxDelay: time.Second}
		err := retry(t.Context(), p, func(context.Context) error {
			calls++
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})
}

func TestRetryBackoffTable(t *testing.T) {
	p := retryPolicy{maxAttempts: 10, baseDelay: 100 * time.Millisecond, maxDelay: time.Second}
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}
	for i, w := range want {
		attempt := i + 1
		if got := p.backoff(attempt); got != w {
			t.Fatalf("backoff(%d) = %v, want %v", attempt, got, w)
		}
	}
	if got := p.backoff(64); got != p.maxDelay {
		t.Fatalf("backoff(64) = %v, want %v", got, p.maxDelay)
	}

	huge := retryPolicy{maxAttempts: 10, baseDelay: 3 * time.Nanosecond, maxDelay: math.MaxInt64}
	if got := huge.backoff(63); got != huge.maxDelay {
		t.Fatalf("backoff(63) with wide range = %v, want %v", got, huge.maxDelay)
	}
}

func TestJitter(t *testing.T) {
	if got := jitter(0); got != 0 {
		t.Fatalf("jitter(0) = %v, want 0", got)
	}
	bound := time.Duration(math.MaxInt64)
	if got := jitter(bound); got < 0 || got >= bound {
		t.Fatalf("jitter(MaxInt64) = %v, want in [0, %v)", got, bound)
	}
	for range 1000 {
		got := jitter(100 * time.Millisecond)
		if got < 0 || got >= 100*time.Millisecond {
			t.Fatalf("jitter(100ms) = %v, want in [0, 100ms)", got)
		}
	}
}

func TestRetryElapsedTimeBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		p := retryPolicy{maxAttempts: 3, baseDelay: 100 * time.Millisecond, maxDelay: time.Second}
		start := time.Now()
		_ = retry(t.Context(), p, func(context.Context) error {
			calls++
			return errBoom
		})
		elapsed := time.Since(start)
		maxWait := p.backoff(1) + p.backoff(2)
		if elapsed < 0 || elapsed > maxWait {
			t.Fatalf("elapsed = %v, want in [0, %v]", elapsed, maxWait)
		}
	})
}

func BenchmarkRetryFirstTrySuccess(b *testing.B) {
	p := retryPolicy{maxAttempts: 3, baseDelay: time.Millisecond, maxDelay: time.Second}
	fn := func(context.Context) error { return nil }
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if err := retry(ctx, p, fn); err != nil {
			b.Fatal(err)
		}
	}
}
