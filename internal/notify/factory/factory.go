package factory

import (
	"fmt"
	"log/slog"

	"github.com/boa/sub2api-monitor/internal/config"
	"github.com/boa/sub2api-monitor/internal/discord"
	"github.com/boa/sub2api-monitor/internal/notify"
	"github.com/boa/sub2api-monitor/internal/notify/feishu"
	"github.com/boa/sub2api-monitor/internal/telegram"
)

// BuildResult is the wired notification stack.
type BuildResult struct {
	Multi    *notify.Multi
	Telegram *telegram.Client // may be nil
	Feishu   *feishu.Channel  // may be nil
	Discord  *discord.Client  // may be nil
}

// BuildFromConfig constructs enabled channels from config.
// Telegram remains optional when only Feishu is configured (or vice versa).
func BuildFromConfig(cfg *config.Config, logger *slog.Logger) (*BuildResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	var (
		channels []notify.Channel
		tg       *telegram.Client
		fs       *feishu.Channel
		dc       *discord.Client
	)

	// Telegram: enabled if bot_token present (legacy) or notify.telegram.enabled
	tgCfg := cfg.Notify.Telegram
	// bridge legacy top-level telegram config
	if tgCfg.BotToken == "" {
		tgCfg.BotToken = cfg.Telegram.BotToken
	}
	if tgCfg.ChatID == "" {
		tgCfg.ChatID = cfg.Telegram.ChatID
	}
	if len(tgCfg.ExtraChatIDs) == 0 {
		tgCfg.ExtraChatIDs = cfg.Telegram.ExtraChatIDs
	}
	if tgCfg.ParseMode == "" {
		tgCfg.ParseMode = cfg.Telegram.ParseMode
	}
	if tgCfg.APIBase == "" {
		tgCfg.APIBase = cfg.Telegram.APIBase
	}
	if tgCfg.MinSendInterval == 0 {
		tgCfg.MinSendInterval = cfg.Telegram.MinSendInterval
	}
	// Telegram channel: on when token exists. Set notify.telegram.enabled=false to force-disable
	// even if a legacy bot_token remains in config (rare).
	tgEnabled := tgCfg.BotToken != ""
	if cfg.Notify.HasExplicit() && !tgCfg.Enabled && tgCfg.BotToken != "" {
		// explicit notify.telegram.enabled: false
		// only disable when the field was intentionally set; zero-value false would break legacy.
		// Heuristic: disable only if feishu is enabled (multi-channel config) AND telegram.enabled is false.
		if cfg.Notify.Feishu.Enabled {
			tgEnabled = false
		}
	}
	if tgCfg.Enabled {
		tgEnabled = tgCfg.BotToken != ""
	}
	if tgEnabled && tgCfg.BotToken != "" {
		// map into existing telegram config type for constructor
		legacy := config.TelegramConfig{
			BotToken:            tgCfg.BotToken,
			ChatID:              tgCfg.ChatID,
			ExtraChatIDs:        tgCfg.ExtraChatIDs,
			ParseMode:           tgCfg.ParseMode,
			DisableNotification: tgCfg.DisableNotification || cfg.Telegram.DisableNotification,
			APIBase:             tgCfg.APIBase,
			SendStartupMessage:  cfg.Telegram.SendStartupMessage,
			MinSendInterval:     tgCfg.MinSendInterval,
			Panel:               cfg.Telegram.Panel,
		}
		client, err := telegram.New(legacy)
		if err != nil {
			return nil, fmt.Errorf("telegram: %w", err)
		}
		tg = client
		channels = append(channels, client.AsChannel(logger))
	}

	// Feishu
	fsCfg := cfg.Notify.Feishu
	if fsCfg.Enabled {
		ch, err := feishu.New(fsCfg)
		if err != nil {
			return nil, fmt.Errorf("feishu: %w", err)
		}
		if ch.Enabled() {
			fs = ch
			channels = append(channels, ch)
		}
	}

	// Discord
	dcCfg := cfg.Notify.Discord
	if dcCfg.BotToken == "" {
		dcCfg.BotToken = cfg.Discord.BotToken
	}
	if dcCfg.DefaultChannelID == "" {
		dcCfg.DefaultChannelID = cfg.Discord.DefaultChannelID
	}
	if len(dcCfg.ExtraChannelIDs) == 0 {
		dcCfg.ExtraChannelIDs = cfg.Discord.ExtraChannelIDs
	}
	dcEnabled := dcCfg.BotToken != ""
	if cfg.Notify.HasExplicit() && !dcCfg.Enabled && dcCfg.BotToken != "" {
		// if notify block present and discord.enabled false with other channels, skip
		if cfg.Notify.Feishu.Enabled || tgEnabled {
			dcEnabled = false
		}
	}
	if dcCfg.Enabled {
		dcEnabled = dcCfg.BotToken != ""
	}
	// Always enable client when panel needs it even if not used as notify default
	if dcEnabled || (cfg.Discord.Panel.Enabled && cfg.Discord.BotToken != "") {
		client, err := discord.NewFromNotify(dcCfg, cfg.Discord)
		if err != nil {
			return nil, fmt.Errorf("discord: %w", err)
		}
		dc = client
		if dcEnabled {
			channels = append(channels, client.AsChannel(logger))
		} else {
			// panel-only: still expose client via BuildResult
		}
	}

	if len(channels) == 0 && dc == nil {
		return nil, fmt.Errorf("no notification channels enabled (configure telegram / feishu / discord)")
	}
	if len(channels) == 0 {
		// allow panel-only discord with empty Multi via a no-op? require at least one channel for alerter
		// if only discord panel without default channel, still add discord channel for user DMs
		if dc != nil {
			channels = append(channels, dc.AsChannel(logger))
		} else {
			return nil, fmt.Errorf("no notification channels enabled (configure telegram / feishu / discord)")
		}
	}

	if logger != nil {
		names := make([]string, 0, len(channels))
		for _, c := range channels {
			names = append(names, c.Name())
		}
		logger.Info("notify channels ready", "channels", names)
	}

	return &BuildResult{
		Multi:    notify.NewMulti(channels...),
		Telegram: tg,
		Feishu:   fs,
		Discord:  dc,
	}, nil
}
