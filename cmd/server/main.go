package main

import (
	"context"
	"errors"
	"expvar"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/danielmalka/coodesh-xyz/internal/lexi"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := lexi.ConfigFromEnv()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	llm := lexi.NewSimulatedLLM(cfg.LLMSimulation(), uint64(time.Now().UnixNano()))
	sender := lexi.NewWhatsAppSender(cfg, nil, log)
	app := lexi.New(cfg, llm, sender, log)
	expvar.Publish("lexi", expvar.Func(func() any { return app.Metrics() }))

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	workersDone := make(chan struct{})
	go func() {
		app.RunWorkers(workerCtx)
		close(workersDone)
	}()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening",
			"addr", cfg.Addr,
			"inbound_rps", cfg.InboundRPS,
			"inbound_burst", cfg.InboundBurst,
			"upstream_rps", cfg.UpstreamRPS,
			"upstream_burst", cfg.UpstreamBurst,
		)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		stopWorkers()
		<-workersDone
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down http", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		stopWorkers()
		<-workersDone
		return err
	}

	log.Info("closing queue")
	app.Close()

	log.Info("waiting for workers")
	select {
	case <-workersDone:
		log.Info("workers drained")
	case <-shutdownCtx.Done():
		log.Warn("shutdown deadline reached, aborting in-flight jobs", "queue_depth", app.Metrics()["queue_depth"])
		stopWorkers()
		<-workersDone
		log.Warn("abandoned queued jobs", "count", app.AbandonPending())
	}
	return nil
}
