package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Instance string         `yaml:"instance"`
	Sub2API  Sub2APIConfig  `yaml:"sub2api"`
	Telegram TelegramConfig `yaml:"telegram"`
	Poll     PollConfig     `yaml:"poll"`
	Alert    AlertConfig    `yaml:"alert"`
	State    StateConfig    `yaml:"state"`
	Checks   ChecksConfig   `yaml:"checks"`
	Logging  LoggingConfig  `yaml:"logging"`
}

type Sub2APIConfig struct {
	BaseURL            string        `yaml:"base_url"`
	AdminAPIKey        string        `yaml:"admin_api_key"`
	JWT                string        `yaml:"jwt"`
	Timeout            time.Duration `yaml:"timeout"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
}

type TelegramConfig struct {
	BotToken             string `yaml:"bot_token"`
	ChatID               string `yaml:"chat_id"`
	ParseMode            string `yaml:"parse_mode"`
	DisableNotification  bool   `yaml:"disable_notification"`
	APIBase              string `yaml:"api_base"`
	SendStartupMessage   bool   `yaml:"send_startup_message"`
}

type PollConfig struct {
	Interval time.Duration `yaml:"interval"`
	Jitter   time.Duration `yaml:"jitter"`
}

type AlertConfig struct {
	Cooldown         time.Duration `yaml:"cooldown"`
	SendResolved     bool          `yaml:"send_resolved"`
	MaxMessageRunes  int           `yaml:"max_message_runes"`
	QuietHours       *QuietHours   `yaml:"quiet_hours"`
}

type QuietHours struct {
	Start           string   `yaml:"start"` // HH:MM
	End             string   `yaml:"end"`
	AllowSeverities []string `yaml:"allow_severities"`
}

type StateConfig struct {
	Driver     string `yaml:"driver"` // memory|sqlite
	SQLitePath string `yaml:"sqlite_path"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type ChecksConfig struct {
	Health       HealthCheck       `yaml:"health"`
	Dashboard    DashboardCheck    `yaml:"dashboard"`
	Accounts     AccountsCheck     `yaml:"accounts"`
	Availability AvailabilityCheck `yaml:"availability"`
	OpsAlerts    OpsAlertsCheck    `yaml:"ops_alerts"`
	Errors       ErrorsCheck       `yaml:"errors"`
	Traffic      TrafficCheck      `yaml:"traffic"`
}

type HealthCheck struct {
	Enabled       bool          `yaml:"enabled"`
	Interval      time.Duration `yaml:"interval"`
	FailThreshold int           `yaml:"fail_threshold"`
}

type DashboardCheck struct {
	Enabled              bool          `yaml:"enabled"`
	Interval             time.Duration `yaml:"interval"`
	MaxErrorAccounts     int           `yaml:"max_error_accounts"`
	MaxOverloadAccounts  int           `yaml:"max_overload_accounts"`
	MaxRatelimitAccounts int           `yaml:"max_ratelimit_accounts"`
}

type AccountsCheck struct {
	Enabled                 bool          `yaml:"enabled"`
	Interval                time.Duration `yaml:"interval"`
	WatchStatuses           []string      `yaml:"watch_statuses"`
	WatchRateLimited        bool          `yaml:"watch_rate_limited"`
	WatchOverload           bool          `yaml:"watch_overload"`
	WatchTempUnschedulable  bool          `yaml:"watch_temp_unschedulable"`
	PageSize                int           `yaml:"page_size"`
}

type AvailabilityCheck struct {
	Enabled             bool          `yaml:"enabled"`
	Interval            time.Duration `yaml:"interval"`
	MinAvailableRatio   float64       `yaml:"min_available_ratio"`
	MinAvailableAccounts int          `yaml:"min_available_accounts"`
}

type OpsAlertsCheck struct {
	Enabled    bool          `yaml:"enabled"`
	Interval   time.Duration `yaml:"interval"`
	Severities []string      `yaml:"severities"`
	Statuses   []string      `yaml:"statuses"`
}

type ErrorsCheck struct {
	Enabled           bool          `yaml:"enabled"`
	Interval          time.Duration `yaml:"interval"`
	Window            time.Duration `yaml:"window"`
	MaxRequestErrors  int           `yaml:"max_request_errors"`
	MaxUpstreamErrors int           `yaml:"max_upstream_errors"`
}

type TrafficCheck struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
	Window   string        `yaml:"window"`
	MinQPS   float64       `yaml:"min_qps"`
}

// Load reads YAML config and overlays environment variables.
func Load(path string) (*Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		// allow env-only boot when file missing
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	applyEnv(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaultConfig() *Config {
	return &Config{
		Instance: "default",
		Sub2API: Sub2APIConfig{
			Timeout: 15 * time.Second,
		},
		Telegram: TelegramConfig{
			ParseMode:          "HTML",
			APIBase:            "https://api.telegram.org",
			SendStartupMessage: true,
		},
		Poll: PollConfig{
			Interval: 30 * time.Second,
			Jitter:   3 * time.Second,
		},
		Alert: AlertConfig{
			Cooldown:        10 * time.Minute,
			SendResolved:    true,
			MaxMessageRunes: 3500,
		},
		State: StateConfig{
			Driver:     "memory",
			SQLitePath: "./data/state.db",
		},
		Checks: ChecksConfig{
			Health: HealthCheck{
				Enabled: true, Interval: 30 * time.Second, FailThreshold: 2,
			},
			Dashboard: DashboardCheck{
				Enabled: true, Interval: 60 * time.Second,
				MaxErrorAccounts: 0, MaxOverloadAccounts: 5, MaxRatelimitAccounts: 20,
			},
			Accounts: AccountsCheck{
				Enabled: true, Interval: 60 * time.Second,
				WatchStatuses: []string{"error"},
				WatchRateLimited: true, WatchOverload: true, WatchTempUnschedulable: true,
				PageSize: 100,
			},
			Availability: AvailabilityCheck{
				Enabled: true, Interval: 60 * time.Second,
				MinAvailableRatio: 0.3, MinAvailableAccounts: 1,
			},
			OpsAlerts: OpsAlertsCheck{
				Enabled: true, Interval: 30 * time.Second,
				Severities: []string{"P0", "P1", "P2"},
			},
			Errors: ErrorsCheck{
				Enabled: false, Interval: 60 * time.Second, Window: 5 * time.Minute,
				MaxRequestErrors: 50, MaxUpstreamErrors: 50,
			},
			Traffic: TrafficCheck{
				Enabled: false, Interval: 60 * time.Second, Window: "5min", MinQPS: 0.1,
			},
		},
		Logging: LoggingConfig{Level: "info", Format: "text"},
	}
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SUB2API_BASE_URL"); v != "" {
		cfg.Sub2API.BaseURL = v
	}
	if v := os.Getenv("SUB2API_ADMIN_API_KEY"); v != "" {
		cfg.Sub2API.AdminAPIKey = v
	}
	if v := os.Getenv("SUB2API_JWT"); v != "" {
		cfg.Sub2API.JWT = v
	}
	if v := os.Getenv("TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Telegram.BotToken = v
	}
	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		cfg.Telegram.ChatID = v
	}
	if v := os.Getenv("INSTANCE"); v != "" {
		cfg.Instance = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Sub2API.BaseURL) == "" {
		return fmt.Errorf("sub2api.base_url is required")
	}
	c.Sub2API.BaseURL = strings.TrimRight(c.Sub2API.BaseURL, "/")
	if c.Sub2API.AdminAPIKey == "" && c.Sub2API.JWT == "" {
		return fmt.Errorf("sub2api.admin_api_key or sub2api.jwt is required")
	}
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("telegram.bot_token is required")
	}
	if c.Telegram.ChatID == "" {
		return fmt.Errorf("telegram.chat_id is required")
	}
	if c.Sub2API.Timeout <= 0 {
		c.Sub2API.Timeout = 15 * time.Second
	}
	if c.Poll.Interval <= 0 {
		c.Poll.Interval = 30 * time.Second
	}
	if c.Alert.Cooldown <= 0 {
		c.Alert.Cooldown = 10 * time.Minute
	}
	if c.Alert.MaxMessageRunes <= 0 {
		c.Alert.MaxMessageRunes = 3500
	}
	if c.Telegram.APIBase == "" {
		c.Telegram.APIBase = "https://api.telegram.org"
	}
	if c.Telegram.ParseMode == "" {
		c.Telegram.ParseMode = "HTML"
	}
	if c.Checks.Accounts.PageSize <= 0 {
		c.Checks.Accounts.PageSize = 100
	}
	if c.Checks.Health.FailThreshold <= 0 {
		c.Checks.Health.FailThreshold = 1
	}
	return nil
}
