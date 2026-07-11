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
	"github.com/boa/sub2api-monitor/internal/notify"
	"github.com/boa/sub2api-monitor/internal/notify/factory"
	"github.com/boa/sub2api-monitor/internal/panel"
	"github.com/boa/sub2api-monitor/internal/panel/discordpanel"
	"github.com/boa/sub2api-monitor/internal/state"
	"github.com/boa/sub2api-monitor/internal/sub2api"
	"github.com/boa/sub2api-monitor/internal/userstore"
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
		"telegram_panel", cfg.Telegram.Panel.Enabled,
		"discord_panel", cfg.Discord.Panel.Enabled,
	)

	// Pluggable notification channels (telegram / feishu / ...)
	nb, err := factory.BuildFromConfig(cfg, logger)
	if err != nil {
		logger.Error("build notify channels", "err", err)
		os.Exit(1)
	}

	st, err := state.New(cfg.State)
	if err != nil {
		logger.Error("create state store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Multi implements both Notifier.SendTo and MessageNotifier.Send
	engine := alerter.New(cfg, st, nb.Multi, logger)
	engine.SetMulti(nb.Multi)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Global ops collectors (optional when only panel mode)
	var runners []collector.Runner
	if cfg.Sub2API.BaseURL != "" && (cfg.Sub2API.AdminAPIKey != "" || cfg.Sub2API.JWT != "") {
		client, err := sub2api.NewClient(cfg.Sub2API)
		if err != nil {
			logger.Error("create sub2api client", "err", err)
			os.Exit(1)
		}
		if err := client.Health(ctx); err != nil {
			logger.Warn("initial health check failed (will keep retrying)", "err", err)
		} else {
			logger.Info("sub2api health ok")
		}
		runners = collector.Build(cfg, client, engine, logger)
	} else {
		logger.Info("global sub2api not fully configured; ops collectors skipped (panel-only mode ok)")
	}

	if cfg.Telegram.SendStartupMessage {
		_ = nb.Multi.Send(ctx, notify.Message{
			Title: "sub2api-monitor started",
			Text: fmt.Sprintf("🟢 sub2api-monitor started\n实例: %s\n版本: %s\n面板: %v\n通道: %s",
				cfg.Instance, version, cfg.Telegram.Panel.Enabled || cfg.Discord.Panel.Enabled, strings.Join(nb.Multi.EnabledNames(), ",")),
			HTML: fmt.Sprintf("🟢 <b>sub2api-monitor started</b>\n实例: <code>%s</code>\n版本: <code>%s</code>\n面板: <code>%v</code>\n通道: <code>%s</code>",
				notify.EscapeHTML(cfg.Instance), notify.EscapeHTML(version), cfg.Telegram.Panel.Enabled, notify.EscapeHTML(strings.Join(nb.Multi.EnabledNames(), ","))),
			Markdown: fmt.Sprintf("🟢 **sub2api-monitor started**\n实例: `%s`\n版本: `%s`\n面板: `%v`\n通道: `%s`",
				cfg.Instance, version, cfg.Telegram.Panel.Enabled || cfg.Discord.Panel.Enabled, strings.Join(nb.Multi.EnabledNames(), ",")),
		})
	}

	errCh := make(chan error, 4)

	if len(runners) > 0 {
		names := make([]string, 0, len(runners))
		for _, r := range runners {
			names = append(names, r.Name)
		}
		logger.Info("collectors ready", "count", len(runners), "names", strings.Join(names, ","))
		go func() {
			errCh <- collector.RunAll(ctx, runners, cfg.Poll, logger)
		}()
	}

	// User panels (Telegram and/or Discord) share userstore when paths match.
	panelEnabled := cfg.Telegram.Panel.Enabled || cfg.Discord.Panel.Enabled
	if panelEnabled {
		usersPath := cfg.Telegram.Panel.UsersPath
		if usersPath == "" {
			usersPath = cfg.Discord.Panel.UsersPath
		}
		if usersPath == "" {
			usersPath = "./data/users.json"
		}
		users, err := userstore.Open(usersPath)
		if err != nil {
			logger.Error("open user store", "err", err)
			os.Exit(1)
		}
		defer users.Close()

		if cfg.Telegram.Panel.Enabled {
			if nb.Telegram == nil {
				logger.Error("telegram panel enabled but telegram channel not available")
				os.Exit(1)
			}
			bot := panel.New(nb.Telegram, users, cfg, logger)
			go func() {
				errCh <- bot.Run(ctx)
			}()
			logger.Info("telegram panel enabled", "users_path", usersPath, "check_interval", cfg.Telegram.Panel.CheckInterval)
		}

		if cfg.Discord.Panel.Enabled {
			if nb.Discord == nil {
				logger.Error("discord panel enabled but discord client not available (set discord.bot_token)")
				os.Exit(1)
			}
			dbot := discordpanel.New(nb.Discord, users, cfg, logger)
			go func() {
				errCh <- dbot.Run(ctx)
			}()
			logger.Info("discord panel enabled", "users_path", usersPath, "check_interval", cfg.Discord.Panel.CheckInterval)
		}

		uuc := collector.NewUserUsageCollector(cfg, users, engine, logger)
		go func() {
			errCh <- uuc.RunLoop(ctx)
		}()
	}

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
