package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kkdai/line-card-assistant/internal/config"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cardStore := store.NewMemoryStore()

	llm, err := workflow.NewLLMModel(ctx, cfg)
	if err != nil {
		slog.Warn("falling back to MockLLM due to LLM initialization error", "err", err)
		llm = workflow.NewMockLLM()
	}

	engine, err := workflow.NewEngine(cfg, cardStore, llm)
	if err != nil {
		slog.Error("failed to initialize ADK 2.0 workflow engine", "err", err)
		os.Exit(1)
	}

	botHandler, err := linebot.NewHandler(cfg, engine.LinebotRunner())
	if err != nil {
		slog.Error("failed to initialize LINE Bot webhook handler", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/webhook", botHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"line-card-assistant"}`))
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("starting line-card-assistant server",
			"port", cfg.Port,
			"model", cfg.ModelName,
			"dry_run_llm", cfg.IsDryRunLLM(),
			"dry_run_line", cfg.IsDryRunLINE(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	botHandler.Wait()
	slog.Info("server stopped cleanly")
}
