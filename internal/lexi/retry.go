package lexi

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

const maxBackoffShift = 63

type retryPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

func (p retryPolicy) backoff(attempt int) time.Duration {
	if attempt <= 1 {
		return min(p.baseDelay, p.maxDelay)
	}
	shift := attempt - 1
	if shift >= maxBackoffShift || p.baseDelay > p.maxDelay>>shift {
		return p.maxDelay
	}
	return p.baseDelay << shift
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

func isPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

func retry(ctx context.Context, p retryPolicy, fn func(context.Context) error) error {
	if p.maxAttempts < 1 {
		p.maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= p.maxAttempts; attempt++ {
		if ctx.Err() != nil {
			if lastErr == nil {
				return ctx.Err()
			}
			return errors.Join(lastErr, ctx.Err())
		}
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if isPermanent(lastErr) {
			return lastErr
		}
		if ctx.Err() != nil {
			return errors.Join(lastErr, ctx.Err())
		}
		if attempt == p.maxAttempts {
			break
		}
		if err := sleep(ctx, jitter(p.backoff(attempt))); err != nil {
			return errors.Join(lastErr, err)
		}
	}
	return fmt.Errorf("retry: gave up after %d attempts: %w", p.maxAttempts, lastErr)
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}

func sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
