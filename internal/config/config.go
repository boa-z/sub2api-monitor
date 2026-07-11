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
	// Telegram is the legacy top-level Telegram config (still supported).
	// Prefer notify.telegram for new deployments; both are merged at runtime.
	Telegram TelegramConfig `yaml:"telegram"`
	// Notify holds pluggable outbound channels (telegram / feishu / future).
	Notify  NotifyConfig  `yaml:"notify"`
	Poll    PollConfig    `yaml:"poll"`
	Alert   AlertConfig   `yaml:"alert"`
	State   StateConfig   `yaml:"state"`
	Checks  ChecksConfig  `yaml:"checks"`
	Logging LoggingConfig `yaml:"logging"`
}

type Sub2APIConfig struct {
	BaseURL            string        `yaml:"base_url"`
	AdminAPIKey        string        `yaml:"admin_api_key"`
	JWT                string        `yaml:"jwt"`
	Timeout            time.Duration `yaml:"timeout"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
}

type TelegramConfig struct {
	BotToken            string        `yaml:"bot_token"`
	ChatID              string        `yaml:"chat_id"`
	ExtraChatIDs        []string      `yaml:"extra_chat_ids"`
	ParseMode           string        `yaml:"parse_mode"`
	DisableNotification bool          `yaml:"disable_notification"`
	APIBase             string        `yaml:"api_base"`
	SendStartupMessage  bool          `yaml:"send_startup_message"`
	MinSendInterval     time.Duration `yaml:"min_send_interval"`
	// Panel is the interactive user control panel (Telegram private chat).
	Panel PanelConfig `yaml:"panel"`
}

// ----- pluggable notify channels -----

// NotifyConfig groups outbound push channels. When the notify: block is present
// in YAML, HasExplicit is set so BuildFromConfig does not auto-enable legacy telegram.
type NotifyConfig struct {
	// set true by loader when the notify key exists (even if empty)
	explicit bool `yaml:"-"`

	Telegram NotifyTelegramConfig `yaml:"telegram"`
	Feishu   FeishuConfig         `yaml:"feishu"`
	// future: Webhook, Email, Slack, ...
}

func (n NotifyConfig) HasExplicit() bool { return n.explicit }

// NotifyTelegramConfig is the channel-scoped telegram settings under notify.telegram.
// Fields mirror TelegramConfig without the panel (panel stays top-level for UX).
type NotifyTelegramConfig struct {
	Enabled             bool          `yaml:"enabled"`
	BotToken            string        `yaml:"bot_token"`
	ChatID              string        `yaml:"chat_id"`
	ExtraChatIDs        []string      `yaml:"extra_chat_ids"`
	ParseMode           string        `yaml:"parse_mode"`
	DisableNotification bool          `yaml:"disable_notification"`
	APIBase             string        `yaml:"api_base"`
	MinSendInterval     time.Duration `yaml:"min_send_interval"`
}

// FeishuConfig configures Feishu/Lark outbound alerts.
type FeishuConfig struct {
	Enabled bool `yaml:"enabled"`
	// WebhookURL is a group custom-bot webhook (recommended for ops alerts).
	WebhookURL string `yaml:"webhook_url"`
	// WebhookSecret enables signed webhook requests when the bot has signature verification on.
	WebhookSecret string `yaml:"webhook_secret"`
	// App credentials reserved for future IM message API (user/chat targeted).
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	// DefaultReceiveIDs used when app messaging is implemented.
	DefaultReceiveIDs []string `yaml:"default_receive_ids"`
}

// PanelConfig controls the Telegram user-facing configuration UI.
type PanelConfig struct {
	Enabled bool `yaml:"enabled"`
	// AllowUserIDs restricts who can use the panel. Empty + OpenRegistration/AllowAll controls open access.
	AllowUserIDs []int64 `yaml:"allow_user_ids"`
	// AllowAll lets any Telegram user configure their own profile (still isolated).
	AllowAll bool `yaml:"allow_all"`
	// OpenRegistration is alias behavior: when allow list empty, allow anyone (default true if panel enabled and list empty — set explicitly).
	OpenRegistration bool `yaml:"open_registration"`
	// UsersPath is where per-user profiles are stored (JSON).
	UsersPath string `yaml:"users_path"`
	// CheckInterval for per-user usage polling.
	CheckInterval time.Duration `yaml:"check_interval"`
	// Cooldown for per-user usage alerts.
	Cooldown time.Duration `yaml:"cooldown"`
}

type PollConfig struct {
	Interval time.Duration `yaml:"interval"`
	Jitter   time.Duration `yaml:"jitter"`
}

type AlertConfig struct {
	Cooldown        time.Duration `yaml:"cooldown"`
	SendResolved    bool          `yaml:"send_resolved"`
	MaxMessageRunes int           `yaml:"max_message_runes"`
	QuietHours      *QuietHours   `yaml:"quiet_hours"`
}

type QuietHours struct {
	Start           string   `yaml:"start"`
	End             string   `yaml:"end"`
	AllowSeverities []string `yaml:"allow_severities"`
}

type StateConfig struct {
	Driver     string `yaml:"driver"`
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
	AccountUsage AccountUsageCheck `yaml:"account_usage"`
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
	Enabled                bool          `yaml:"enabled"`
	Interval               time.Duration `yaml:"interval"`
	WatchStatuses          []string      `yaml:"watch_statuses"`
	WatchRateLimited       bool          `yaml:"watch_rate_limited"`
	WatchOverload          bool          `yaml:"watch_overload"`
	WatchTempUnschedulable bool          `yaml:"watch_temp_unschedulable"`
	PageSize               int           `yaml:"page_size"`
}

type AvailabilityCheck struct {
	Enabled              bool          `yaml:"enabled"`
	Interval             time.Duration `yaml:"interval"`
	MinAvailableRatio    float64       `yaml:"min_available_ratio"`
	MinAvailableAccounts int           `yaml:"min_available_accounts"`
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

// ----- account usage quotas -----

// UsageThreshold fires when a usage window utilization reaches UtilizationGTE (0-100).
type UsageThreshold struct {
	// Window: five_hour|7d alias|seven_day|seven_day_sonnet|seven_day_fable|
	// gemini_shared_daily|gemini_pro_daily|gemini_flash_daily|antigravity:<model>|max
	Window string `yaml:"window"`
	// UtilizationGTE is percent 0-100+. Example: 80 means alert at >= 80% used.
	UtilizationGTE float64 `yaml:"utilization_gte"`
	// ResolveBelow is optional hysteresis; default = UtilizationGTE - 5 (min 0).
	ResolveBelow *float64 `yaml:"resolve_below"`
	Severity     string   `yaml:"severity"` // P0-P3, default P2
}

// TodayThreshold fires on local today-stats counters (cost/tokens/requests).
// Zero means disabled for that field.
type TodayThreshold struct {
	CostGTE     float64 `yaml:"cost_gte"`
	TokensGTE   int64   `yaml:"tokens_gte"`
	RequestsGTE int64   `yaml:"requests_gte"`
	Severity    string  `yaml:"severity"`
}

// AccountUsageTarget is a single monitored account.
type AccountUsageTarget struct {
	ID         int64            `yaml:"id"`
	Name       string           `yaml:"name"` // optional display label
	ChatIDs    []string         `yaml:"chat_ids"`
	Thresholds []UsageThreshold `yaml:"thresholds"` // empty → inherit defaults
	Today      *TodayThreshold  `yaml:"today"`      // nil → inherit default_today if set
	// Source override: passive|active
	Source string `yaml:"source"`
	// Disable usage windows / today independently
	DisableUsage bool `yaml:"disable_usage"`
	DisableToday bool `yaml:"disable_today"`
}

type AccountUsageCheck struct {
	Enabled           bool                `yaml:"enabled"`
	Interval          time.Duration       `yaml:"interval"`
	Source            string              `yaml:"source"` // passive|active
	ForceActive       bool                `yaml:"force_active"`
	Concurrency       int                 `yaml:"concurrency"`
	Cooldown           time.Duration       `yaml:"cooldown"`
	DefaultThresholds []UsageThreshold    `yaml:"default_thresholds"`
	DefaultToday      *TodayThreshold     `yaml:"default_today"`
	Accounts          []AccountUsageTarget `yaml:"accounts"`
}

// Load reads YAML config and overlays environment variables.
func Load(path string) (*Config, error) {
	cfg := defaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else {
		// Detect whether notify: key is present before full unmarshal.
		var probe map[string]any
		if err := yaml.Unmarshal(data, &probe); err == nil {
			if _, ok := probe["notify"]; ok {
				cfg.Notify.explicit = true
			}
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		// re-apply explicit flag (yaml may not preserve unexported via second decode)
		if probe != nil {
			if _, ok := probe["notify"]; ok {
				cfg.Notify.explicit = true
			}
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
			MinSendInterval:    50 * time.Millisecond,
				Panel: PanelConfig{
					Enabled:          false,
					OpenRegistration: true,
					UsersPath:        "./data/users.json",
					CheckInterval:    5 * time.Minute,
					Cooldown:         2 * time.Hour,
				},
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
				WatchStatuses:          []string{"error"},
				WatchRateLimited:       true,
				WatchOverload:          true,
				WatchTempUnschedulable: true,
				PageSize:               100,
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
			AccountUsage: AccountUsageCheck{
				Enabled:     false,
				Interval:    5 * time.Minute,
				Source:      "passive",
				Concurrency: 3,
				Cooldown:     2 * time.Hour,
				DefaultThresholds: []UsageThreshold{
					{Window: "five_hour", UtilizationGTE: 80, Severity: "P2"},
					{Window: "seven_day", UtilizationGTE: 90, Severity: "P1"},
				},
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
	if v := os.Getenv("FEISHU_WEBHOOK_URL"); v != "" {
		cfg.Notify.Feishu.WebhookURL = v
		cfg.Notify.Feishu.Enabled = true
	}
	if v := os.Getenv("FEISHU_WEBHOOK_SECRET"); v != "" {
		cfg.Notify.Feishu.WebhookSecret = v
	}
	if v := os.Getenv("INSTANCE"); v != "" {
		cfg.Instance = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
}

func (c *Config) Validate() error {
	// When panel is enabled, global sub2api can be optional (users bring their own).
	panelOn := c.Telegram.Panel.Enabled
	feishuOn := c.Notify.Feishu.Enabled && strings.TrimSpace(c.Notify.Feishu.WebhookURL) != ""
	telegramToken := strings.TrimSpace(c.Telegram.BotToken)
	if strings.TrimSpace(c.Notify.Telegram.BotToken) != "" {
		telegramToken = strings.TrimSpace(c.Notify.Telegram.BotToken)
	}
	telegramOn := telegramToken != ""

	if strings.TrimSpace(c.Sub2API.BaseURL) == "" && !panelOn {
		return fmt.Errorf("sub2api.base_url is required")
	}
	c.Sub2API.BaseURL = strings.TrimRight(c.Sub2API.BaseURL, "/")
	if c.Sub2API.AdminAPIKey == "" && c.Sub2API.JWT == "" && !panelOn {
		return fmt.Errorf("sub2api.admin_api_key or sub2api.jwt is required")
	}

	// Need at least one outbound channel.
	if !telegramOn && !feishuOn {
		return fmt.Errorf("at least one notify channel required (telegram.bot_token or notify.feishu.webhook_url)")
	}
	if panelOn && !telegramOn {
		return fmt.Errorf("telegram.bot_token is required when panel is enabled")
	}

	// default chat can be empty only if panel enabled, feishu-only ops, or per-target chats
	tgChat := c.Telegram.ChatID
	if c.Notify.Telegram.ChatID != "" {
		tgChat = c.Notify.Telegram.ChatID
	}
	extra := c.Telegram.ExtraChatIDs
	if len(c.Notify.Telegram.ExtraChatIDs) > 0 {
		extra = c.Notify.Telegram.ExtraChatIDs
	}
	if telegramOn && tgChat == "" && len(extra) == 0 && !panelOn && !feishuOn {
		if !c.Checks.AccountUsage.Enabled || len(c.Checks.AccountUsage.Accounts) == 0 {
			return fmt.Errorf("telegram.chat_id is required")
		}
		for _, a := range c.Checks.AccountUsage.Accounts {
			if len(a.ChatIDs) == 0 {
				return fmt.Errorf("telegram.chat_id is required (or set chat_ids on every account_usage target)")
			}
		}
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
	if c.Telegram.MinSendInterval <= 0 {
		c.Telegram.MinSendInterval = 50 * time.Millisecond
	}
	if c.Checks.Accounts.PageSize <= 0 {
		c.Checks.Accounts.PageSize = 100
	}
	if c.Checks.Health.FailThreshold <= 0 {
		c.Checks.Health.FailThreshold = 1
	}

	// panel defaults
	if panelOn {
		if c.Telegram.Panel.UsersPath == "" {
			c.Telegram.Panel.UsersPath = "./data/users.json"
		}
		if c.Telegram.Panel.CheckInterval <= 0 {
			c.Telegram.Panel.CheckInterval = 5 * time.Minute
		}
		if c.Telegram.Panel.Cooldown <= 0 {
			c.Telegram.Panel.Cooldown = 2 * time.Hour
		}
		// If neither allow list nor allow_all/open_registration, default open_registration=true
		if len(c.Telegram.Panel.AllowUserIDs) == 0 && !c.Telegram.Panel.AllowAll && !c.Telegram.Panel.OpenRegistration {
			c.Telegram.Panel.OpenRegistration = true
		}
	}

	// account usage validation
	au := &c.Checks.AccountUsage
	if au.Enabled {
		if len(au.Accounts) == 0 {
			return fmt.Errorf("checks.account_usage.accounts must not be empty when enabled")
		}
		if au.Interval <= 0 {
			au.Interval = 5 * time.Minute
		}
		if au.Concurrency <= 0 {
			au.Concurrency = 3
		}
		if au.Cooldown <= 0 {
			au.Cooldown = 2 * time.Hour
		}
		src := strings.ToLower(strings.TrimSpace(au.Source))
		if src == "" {
			src = "passive"
		}
		if src != "passive" && src != "active" {
			return fmt.Errorf("checks.account_usage.source must be passive or active")
		}
		au.Source = src
		for i := range au.Accounts {
			if au.Accounts[i].ID <= 0 {
				return fmt.Errorf("checks.account_usage.accounts[%d].id is required", i)
			}
			ths := au.Accounts[i].Thresholds
			if len(ths) == 0 {
				ths = au.DefaultThresholds
			}
			if !au.Accounts[i].DisableUsage {
				for j, th := range ths {
					if strings.TrimSpace(th.Window) == "" {
						return fmt.Errorf("account_usage account %d threshold[%d].window required", au.Accounts[i].ID, j)
					}
					if th.UtilizationGTE <= 0 || th.UtilizationGTE > 200 {
						return fmt.Errorf("account_usage account %d threshold[%d].utilization_gte must be in (0,200]", au.Accounts[i].ID, j)
					}
				}
			}
		}
	}
	return nil
}

// ResolveThresholds returns effective thresholds for a target.
func (t AccountUsageTarget) ResolveThresholds(defaults []UsageThreshold) []UsageThreshold {
	if len(t.Thresholds) > 0 {
		return t.Thresholds
	}
	return defaults
}

// ResolveToday returns effective today threshold (may be nil = disabled).
func (t AccountUsageTarget) ResolveToday(defaults *TodayThreshold) *TodayThreshold {
	if t.DisableToday {
		return nil
	}
	if t.Today != nil {
		return t.Today
	}
	return defaults
}
