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
	"github.com/kkdai/line-card-assistant/internal/gateway"
	"github.com/kkdai/line-card-assistant/internal/linebot"
	"github.com/kkdai/line-card-assistant/internal/store"
	"github.com/kkdai/line-card-assistant/internal/workflow"
	"github.com/kkdai/line-card-assistant/sessions"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var runner linebot.WorkflowRunner
	if cfg.AgentRuntimeURL != "" {
		gwClient, err := gateway.NewClient(
			ctx,
			cfg.AgentRuntimeURL,
			cfg.AgentRuntimeUseADC,
			gateway.WithAppName(cfg.AgentRuntimeAppName),
		)
		if err != nil {
			slog.Error("failed to initialize remote Agent Runtime gateway client", "url", cfg.AgentRuntimeURL, "err", err)
			os.Exit(1)
		}
		slog.Info("running in decoupled LINE Webhook Gateway mode",
			"agent_runtime_url", cfg.AgentRuntimeURL,
			"agent_runtime_app", cfg.AgentRuntimeAppName,
			"use_adc", cfg.AgentRuntimeUseADC,
		)
		runner = gwClient
	} else {
		cardStore, err := store.NewStore(ctx)
		if err != nil {
			slog.Warn("falling back to MemoryStore due to store initialization error", "err", err)
			cardStore = store.NewMemoryStore()
		}

		llm, err := workflow.NewLLMModel(ctx, cfg)
		if err != nil {
			slog.Warn("falling back to MockLLM due to LLM initialization error", "err", err)
			llm = workflow.NewMockLLM()
		}

		sessionService, err := sessions.NewService(ctx)
		if err != nil {
			slog.Warn("falling back to default in-memory session service", "err", err)
		}
		artifactService, err := sessions.NewArtifactService(ctx)
		if err != nil {
			slog.Warn("falling back to default in-memory artifact service", "err", err)
		}

		engine, err := workflow.NewEngine(
			cfg,
			cardStore,
			llm,
			workflow.WithSessionService(sessionService),
			workflow.WithArtifactService(artifactService),
		)
		if err != nil {
			slog.Error("failed to initialize ADK 2.0 workflow engine", "err", err)
			os.Exit(1)
		}
		runner = engine.LinebotRunner()
	}

	botHandler, err := linebot.NewHandler(cfg, runner)
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
