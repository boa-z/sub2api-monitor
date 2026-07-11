package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/boa/sub2api-monitor/internal/alerter"
	"github.com/boa/sub2api-monitor/internal/collector"
	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/state"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/telegram"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("sub2api-monitor %s (commit=%s date=%s)\n", version, commit, date)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Logging.Level, cfg.Logging.Format)
	slog.SetDefault(logger)

	logger.Info("starting sub2api-monitor",
		"version", version,
		"instance", cfg.Instance,
		"base_url", cfg.Sub2API.BaseURL,
	)

	client, err := sub2api.NewClient(cfg.Sub2API)
	if err != nil {
		logger.Error("create sub2api client", "err", err)
		os.Exit(1)
	}

	tg, err := telegram.New(cfg.Telegram)
	if err != nil {
		logger.Error("create telegram client", "err", err)
		os.Exit(1)
	}

	st, err := state.New(cfg.State)
	if err != nil {
		logger.Error("create state store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	engine := alerter.New(cfg, st, tg, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := client.Health(ctx); err != nil {
		logger.Warn("initial health check failed (will keep retrying)", "err", err)
	} else {
		logger.Info("sub2api health ok")
	}

	if cfg.Telegram.SendStartupMessage {
		_ = tg.Send(ctx, fmt.Sprintf(
			"🟢 %s\n实例: %s\n版本: %s",
			telegram.Bold("sub2api-monitor started"),
			telegram.Code(cfg.Instance),
			telegram.Code(version),
		))
	}

	runners := collector.Build(cfg, client, engine, logger)
	names := make([]string, 0, len(runners))
	for _, r := range runners {
		names = append(names, r.Name)
	}
	logger.Info("collectors ready", "count", len(runners), "names", strings.Join(names, ","))

	errCh := make(chan error, 1)
	go func() {
		errCh <- collector.RunAll(ctx, runners, cfg.Poll, logger)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			logger.Error("runner exited", "err", err)
			os.Exit(1)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = shutdownCtx
	logger.Info("bye")
}

func newLogger(level, format string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
