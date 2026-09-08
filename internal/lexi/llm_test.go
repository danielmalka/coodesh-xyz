package lexi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestSimulatedLLMReplyDeterministicSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim := LLMSimulation{MinLatency: 100 * time.Millisecond, MaxLatency: 300 * time.Millisecond}
		llm := NewSimulatedLLM(sim, 7)
		start := time.Now()
		reply, err := llm.Reply(t.Context(), "hi")
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if reply != simulatedReply {
			t.Fatalf("reply = %q, want %q", reply, simulatedReply)
		}
		if elapsed < sim.MinLatency || elapsed > sim.MaxLatency {
			t.Fatalf("elapsed = %v, want in [%v, %v]", elapsed, sim.MinLatency, sim.MaxLatency)
		}
	})
}

func TestSimulatedLLMReplyAlwaysFails(t *testing.T) {
	sim := LLMSimulation{FailureRate: 1}
	llm := NewSimulatedLLM(sim, 1)
	reply, err := llm.Reply(context.Background(), "hi")
	if !errors.Is(err, errLLMUnavailable) {
		t.Fatalf("err = %v, want wrapping errLLMUnavailable", err)
	}
	if isPermanent(err) {
		t.Fatalf("isPermanent(err) = true, want false")
	}
	if reply != "" {
		t.Fatalf("reply = %q, want empty", reply)
	}
}

func TestSimulatedLLMReplySlowPathTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim := LLMSimulation{SlowRate: 1, MaxLatency: time.Second}
		llm := NewSimulatedLLM(sim, 3)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		start := time.Now()
		reply, err := llm.Reply(ctx, "hi")
		elapsed := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want wrapping context.DeadlineExceeded", err)
		}
		if reply != "" {
			t.Fatalf("reply = %q, want empty", reply)
		}
		if elapsed != 2*time.Second {
			t.Fatalf("elapsed = %v, want 2s", elapsed)
		}
	})
}

func TestSimulatedLLMReplyContextCanceledReturnsImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim := LLMSimulation{MinLatency: time.Second, MaxLatency: time.Second}
		llm := NewSimulatedLLM(sim, 4)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		start := time.Now()
		_, err := llm.Reply(ctx, "hi")
		elapsed := time.Since(start)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want wrapping context.Canceled", err)
		}
		if elapsed != 0 {
			t.Fatalf("elapsed = %v, want 0", elapsed)
		}
	})
}

func TestSimulatedLLMReplyCanceledContextZeroLatency(t *testing.T) {
	const calls = 200
	sim := LLMSimulation{FailureRate: 0}
	llm := NewSimulatedLLM(sim, 6)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range calls {
		reply, err := llm.Reply(ctx, "hi")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: err = %v, want wrapping context.Canceled", i, err)
		}
		if reply != "" {
			t.Fatalf("call %d: reply = %q, want empty", i, reply)
		}
	}
}

func TestSimulatedLLMReplyMixedRatesClassified(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			failureRate   = 0.3
			slowRate      = 0.3
			latency       = 100 * time.Millisecond
			calls         = 1000
			minFailures   = 250
			maxFailures   = 350
			minSlow       = 250
			maxSlow       = 350
			minNormal     = 340
			maxNormal     = 460
			slowLatency   = latency * slowLatencyFactor
			normalLatency = latency
		)
		sim := LLMSimulation{FailureRate: failureRate, SlowRate: slowRate, MinLatency: latency, MaxLatency: latency}
		llm := NewSimulatedLLM(sim, 7)
		var failures, slow, normal int
		for range calls {
			start := time.Now()
			_, err := llm.Reply(t.Context(), "hi")
			elapsed := time.Since(start)
			switch {
			case err != nil:
				failures++
			case elapsed == slowLatency:
				slow++
			case elapsed == normalLatency:
				normal++
			default:
				t.Fatalf("unclassifiable call: err = %v, elapsed = %v", err, elapsed)
			}
		}
		if failures < minFailures || failures > maxFailures {
			t.Fatalf("failures = %d, want in [%d, %d]", failures, minFailures, maxFailures)
		}
		if slow < minSlow || slow > maxSlow {
			t.Fatalf("slow = %d, want in [%d, %d]", slow, minSlow, maxSlow)
		}
		if normal < minNormal || normal > maxNormal {
			t.Fatalf("normal = %d, want in [%d, %d]", normal, minNormal, maxNormal)
		}
	})
}

func TestSimulatedLLMReplySameSeedReproducible(t *testing.T) {
	const seed = 42
	const failureRate = 0.5
	const calls = 50
	sim := LLMSimulation{FailureRate: failureRate}
	a := NewSimulatedLLM(sim, seed)
	b := NewSimulatedLLM(sim, seed)
	for i := range calls {
		_, errA := a.Reply(context.Background(), "hi")
		_, errB := b.Reply(context.Background(), "hi")
		if (errA == nil) != (errB == nil) {
			t.Fatalf("call %d: errA = %v, errB = %v, want matching outcomes", i, errA, errB)
		}
	}
}

func TestSimulatedLLMReplyStatisticalSanity(t *testing.T) {
	const seed = 1
	const failureRate = 0.3
	const calls = 1000
	const minFailures = 250
	const maxFailures = 350
	sim := LLMSimulation{FailureRate: failureRate}
	llm := NewSimulatedLLM(sim, seed)
	failures := 0
	for range calls {
		if _, err := llm.Reply(context.Background(), "hi"); err != nil {
			failures++
		}
	}
	if failures < minFailures || failures > maxFailures {
		t.Fatalf("failures = %d, want in [%d, %d]", failures, minFailures, maxFailures)
	}
}

func TestSimulatedLLMReplyConcurrentSafety(t *testing.T) {
	const goroutines = 50
	const callsPerGoroutine = 20
	sim := LLMSimulation{FailureRate: 0.5}
	llm := NewSimulatedLLM(sim, 9)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range callsPerGoroutine {
				reply, err := llm.Reply(context.Background(), "hi")
				if err != nil {
					if !errors.Is(err, errLLMUnavailable) {
						t.Errorf("err = %v, want errLLMUnavailable", err)
					}
					continue
				}
				if reply != simulatedReply {
					t.Errorf("reply = %q, want %q", reply, simulatedReply)
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkSimulatedLLMReply(b *testing.B) {
	sim := LLMSimulation{}
	llm := NewSimulatedLLM(sim, 5)
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := llm.Reply(ctx, "hi"); err != nil {
			b.Fatal(err)
		}
	}
}
