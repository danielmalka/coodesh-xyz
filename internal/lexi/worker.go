package lexi

import (
	"context"
	"sync"
	"time"
)

func (a *App) RunWorkers(ctx context.Context) {
	var wg sync.WaitGroup
	for range a.cfg.Workers {
		wg.Go(func() { a.worker(ctx) })
	}
	wg.Wait()
}

func (a *App) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-a.q.jobs():
			if !ok {
				return
			}
			a.process(ctx, job)
		}
	}
}

func (a *App) process(ctx context.Context, job Job) {
	if ctx.Err() != nil {
		a.abandon(job)
		return
	}
	a.metrics.InFlight.Add(1)
	defer a.metrics.InFlight.Add(-1)
	start := time.Now()

	reply, attempts, err := a.askLLM(ctx, job.Text)
	if err != nil {
		a.dlq.add(DeadLetter{
			MessageID: job.MessageID,
			From:      job.From,
			Text:      job.Text,
			Stage:     "llm",
			Error:     err.Error(),
			Attempts:  attempts,
			FailedAt:  time.Now(),
		})
		a.metrics.FailedLLM.Add(1)
		a.log.Error("job failed", "message_id", job.MessageID, "stage", "llm", "attempts", attempts, "err", err)
		return
	}

	attempts, err = a.sender.Send(ctx, OutboundMessage{MessageID: job.MessageID, To: job.From, Text: reply})
	if err != nil {
		a.dlq.add(DeadLetter{
			MessageID: job.MessageID,
			From:      job.From,
			Text:      job.Text,
			Stage:     "outbound",
			Error:     err.Error(),
			Attempts:  attempts,
			FailedAt:  time.Now(),
		})
		a.metrics.FailedOutbound.Add(1)
		a.log.Error("job failed", "message_id", job.MessageID, "stage", "outbound", "attempts", attempts, "err", err)
		return
	}

	a.metrics.Processed.Add(1)
	a.log.Info("reply delivered", "message_id", job.MessageID, "from", hashPhone(job.From), "duration", time.Since(start))
}

func (a *App) askLLM(ctx context.Context, text string) (reply string, attempts int, err error) {
	var out string
	var n int
	err = retry(ctx, a.cfg.retryPolicy(), func(ctx context.Context) error {
		n++
		callCtx, cancel := context.WithTimeout(ctx, a.cfg.LLMTimeout)
		defer cancel()
		if err := a.upstream.Wait(callCtx); err != nil {
			return err
		}
		r, e := a.llm.Reply(callCtx, text)
		if e != nil {
			return e
		}
		out = r
		return nil
	})
	return out, n, err
}

func (a *App) abandon(job Job) {
	a.dlq.add(DeadLetter{
		MessageID: job.MessageID,
		From:      job.From,
		Text:      job.Text,
		Stage:     "shutdown",
		Error:     "aborted by shutdown",
		FailedAt:  time.Now(),
	})
	a.metrics.Abandoned.Add(1)
}

func (a *App) abandonPending() int {
	n := 0
	for job := range a.q.jobs() {
		a.abandon(job)
		n++
	}
	return n
}
