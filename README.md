# sub2api-monitor

不修改 [sub2api](https://github.com/Wei-Shaw/sub2api) 源码的独立 Telegram 监控程序。

通过 **Admin REST API 只读轮询** 获取运行状态，按规则触发 Telegram 告警。

## Sub2API 是什么

Sub2API 是一个 **AI API 网关平台**，用于分发和管理上游 AI 订阅配额：

| 能力 | 说明 |
|------|------|
| 多账号管理 | 支持 OAuth / API Key 等多类型上游账号（Claude、OpenAI、Gemini、Grok、Antigravity 等） |
| API Key 分发 | 为终端用户生成 Key，统一鉴权后转发上游 |
| 精确计费 | Token 级用量追踪与成本计算 |
| 智能调度 | 账号选择、粘性会话、分组隔离 |
| 并发 / 限速 | 用户级与账号级并发、RPM/Token 限制 |
| 内置支付 | EasyPay / 支付宝 / 微信 / Stripe 等 |
| 管理后台 | 账号、用户、用量、Ops 看板、告警规则、渠道探测等 |

技术栈：Go (Gin + Ent) + Vue3 + PostgreSQL + Redis。

### 与监控相关的内置能力（只读复用）

Sub2API **已有** 运维监控与邮件告警，但没有原生 Telegram 通道。外部监控可复用：

| 端点 | 用途 |
|------|------|
| `GET /health` | 进程存活（无需鉴权） |
| `GET /api/v1/admin/dashboard/stats` | 用户/账号/Token 汇总 |
| `GET /api/v1/admin/dashboard/realtime` | 实时指标 |
| `GET /api/v1/admin/ops/account-availability` | 账号可用率（按平台/分组） |
| `GET /api/v1/admin/ops/concurrency` | 并发占用 |
| `GET /api/v1/admin/ops/realtime-traffic` | QPS/TPS 窗口统计 |
| `GET /api/v1/admin/ops/alert-events` | 内置告警事件（可桥接到 TG） |
| `GET /api/v1/admin/ops/request-errors` | 客户端可见错误 |
| `GET /api/v1/admin/ops/upstream-errors` | 上游错误 |
| `GET /api/v1/admin/accounts` | 账号列表（error / rate_limit / overload） |
| `GET /api/v1/admin/channel-monitors` | 渠道主动探测任务 |

鉴权（二选一）：

- `x-api-key: <Admin API Key>`（推荐）
- `Authorization: Bearer <admin JWT>`

Admin API Key 在后台「设置 → Admin API Key」生成。

## 设计原则：零侵入

```
┌─────────────────┐     只读 HTTP      ┌──────────────────┐
│  sub2api-monitor│ ───────────────►  │  Sub2API Admin   │
│  (本项目)        │                   │  /api/v1/admin/* │
└────────┬────────┘                   └──────────────────┘
         │ sendMessage
         ▼
┌─────────────────┐
│  Telegram Bot   │
└─────────────────┘
```

- **不 fork、不 patch、不依赖 sub2api 内部包**
- 不写 sub2api 数据库；不订阅其 Redis
- 所有状态存在本进程本地（可选 SQLite）
- sub2api 升级不影响本项目（仅需兼容 Admin API 契约）

## 监控策略

### 探针（Collectors）

| 探针 | 频率建议 | 判定 |
|------|----------|------|
| `health` | 30s | `/health` 非 200 或超时 |
| `dashboard` | 60s | `error_accounts` / `overload_accounts` 超过阈值 |
| `accounts` | 60s | 新增 `status=error`、rate_limited、temp_unschedulable |
| `availability` | 60s | 某平台/分组可用账号数或比例过低 |
| `ops_alerts` | 30s | 轮询 `alert-events`，把内置告警桥接到 TG |
| `errors` | 60s | 窗口内 request/upstream 错误突增 |
| `traffic` | 60s | QPS 骤降（可选） |
| `account_usage` | 5m | **指定账号** 用量窗口 / 今日统计达到配置阈值 |

### 告警去重与抑制

- 同一 `fingerprint`（如 `account:error:42`）在 `cooldown` 内只推一次
- 状态恢复时发 **RESOLVED** 消息（可选）
- 支持静默时段（如夜间只保留 P0）

### 消息示例

```
🔴 [P1] 账号异常
实例: prod
账号: #42  claude-oauth-main
状态: error
原因: authentication_error: invalid token
时间: 2026-07-11 16:40:12 +08:00
```

## 快速开始

### 1. 创建 Telegram Bot

1. 找 [@BotFather](https://t.me/BotFather) 创建 bot，拿到 `BOT_TOKEN`
2. 把 bot 拉进群，或私聊 bot
3. 获取 `CHAT_ID`（可用 `https://api.telegram.org/bot<token>/getUpdates`）

### 2. 配置

```bash
cp config.example.yaml config.yaml
# 编辑 config.yaml 填入 base_url / admin_api_key / telegram
```

或使用环境变量（优先级高于文件）：

```bash
export SUB2API_BASE_URL=https://your-sub2api.example.com
export SUB2API_ADMIN_API_KEY=sk-admin-xxx
export TELEGRAM_BOT_TOKEN=123456:ABC...
export TELEGRAM_CHAT_ID=-1001234567890
```

### 3. 运行

```bash
# 本地
go run ./cmd/monitor -config config.yaml

# 或构建
make build
./bin/sub2api-monitor -config config.yaml
```

### 4. Docker

```bash
docker compose up -d
```

## Telegram 用户态面板

开启后，用户可在 **私聊 Bot** 中自助配置自己的监控，无需改服务器 YAML：

```yaml
telegram:
  bot_token: "..."
  chat_id: "管理员ID"        # 仍用于全局运维告警
  panel:
    enabled: true
    allow_user_ids: [123456789]       # 白名单（你的 Telegram 数字 ID）；或 open_registration/allow_all

    users_path: "./data/users.json"
    check_interval: 5m
    cooldown: 2h
```

### 用户操作

1. 私聊 Bot 发送 `/start`（会注册命令菜单）
2. **连接配置** → 设置 Base URL、Admin API Key → **测试连接**（health + dashboard）
3. **监控账号** → 手动输入 ID，或 **从列表选择**（分页拉取 Admin 账号）
4. **阈值** → 按窗口（5h / 7d / Gemini…）设置使用率百分比；可重置为系统默认
5. 保持「监控开启」；后台按 `check_interval` 拉用量，超阈值私聊提醒
6. **立即检查** 查看各窗口使用率、重置时间、今日 req/token/cost，并标记已超阈值窗口

常用命令：`/start` `/status` `/check` `/setbase` `/setkey` `/addaccount` `/delaccount` `/thresholds` `/id` `/help` `/cancel`

### 面板能力一览

| 功能 | 说明 |
|------|------|
| 主面板 | 监控开关、数据源 passive/active、连接与账号摘要、阈值摘要 |
| 连接配置 | Base URL / API Key / 测试连接 / 清除连接；密钥消息尽量自动删除 |
| 监控账号 | 添加、列表选择、启停、删除、详情、重命名 |
| 阈值 | 按窗口添加/修改/删除；写入或重置系统默认（来自 `checks.account_usage.default_thresholds`） |
| 立即检查 | 用量快照 + 今日统计；超阈值窗口标 ⚠️ |
| 权限 | `allow_user_ids` 白名单 / `allow_all` / `open_registration` / 回退默认 `chat_id` 所有者 |
| 后台轮询 | `UserUsageCollector` 按用户隔离告警；拉取失败会发 P3 提示 |

### 数据模型

```
data/users.json
└─ users[]
   ├─ telegram_user_id / chat_id
   ├─ base_url / admin_api_key   # 每用户独立连接
   ├─ enabled / source           # passive|active
   ├─ thresholds[]               # 用户级用量阈值（空=系统默认）
   └─ accounts[{id, name, thresholds?, enabled?}]
```

- 与全局 `checks.account_usage`（YAML 写死账号）可并存
- 面板用户告警只发给该用户 `chat_id`，互不干扰
- `users.json` 含密钥，已由 `data/` 目录 gitignore

### 架构

```
Telegram 用户 ──getUpdates──► panel.Bot
                                │ 读写
                                ▼
                           userstore (users.json)
                                │
                     UserUsageCollector 定时轮询
                                │ 每用户独立 Admin API
                                ▼
                           Sub2API instances
                                │
                     alerter ──► telegram.Client ──► 该用户私聊
```

## 多通道通知架构

告警与推送已从 Telegram 解耦，便于接入飞书等第三方：

```
collector ──Emit──► alerter.Engine
                       │ 格式化 plain/HTML/Markdown
                       ▼
                  notify.Multi (fan-out)
                 /              \
         telegram.Channel    feishu.Channel
         (Bot API)           (Webhook 卡片)
                 \              /
              后续: webhook / email / slack ...
```

| 包 | 职责 |
|----|------|
| `internal/notify` | `Channel` 接口、`Message`、`Multi` fan-out、格式化 |
| `internal/notify/factory` | 按配置装配通道 |
| `internal/telegram` | Telegram Bot + `AsChannel()` 适配 |
| `internal/notify/feishu` | 飞书自定义机器人 Webhook（签名可选） |

### 配置示例

```yaml
# 旧写法仍然可用
telegram:
  bot_token: "123456:ABC..."
  chat_id: "YOUR_CHAT_ID"

# 推荐：显式通道
notify:
  telegram:
    enabled: true
    # bot_token / chat_id 可省略，自动回退顶层 telegram
  feishu:
    enabled: true
    webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/xxx"
    # webhook_secret: "签名密钥"   # 机器人开启校验时填写
```

同时启用时，**同一条告警会 fan-out 到所有通道**。  
按通道路由可用 recipient 前缀：`telegram:123`、`feishu:oc_xxx`（飞书应用发 IM 预留）。

### 扩展新通道

1. 新建 `internal/notify/<name>`，实现：

```go
type Channel interface {
    Name() string
    Enabled() bool
    Send(ctx context.Context, msg notify.Message) error
}
```

2. 在 `internal/notify/factory/factory.go` 的 `BuildFromConfig` 注册  
3. 在 `config.NotifyConfig` 增加配置段  

`notify.Message` 同时带 `Text` / `HTML` / `Markdown`，通道按能力选择；`alerter` 负责去重、冷却、静默时段。

## 配置说明

见 [`config.example.yaml`](config.example.yaml)。

关键项：

- `sub2api.base_url` / `admin_api_key`
- `telegram.bot_token` / `chat_id`（可再配 `extra_chat_ids` 抄送）
- `checks.*.enabled` 与阈值
- `alert.cooldown` 去重窗口
- `checks.account_usage` 指定账号用量监控

## 指定账号用量提醒

不改 sub2api，轮询 Admin API：

- `GET /api/v1/admin/accounts/:id/usage?source=passive|active`
- `GET /api/v1/admin/accounts/:id/today-stats`

`utilization` 为 **0–100 百分比**。达到阈值后向该账号配置的 `chat_ids`（或默认 chat）推送；低于恢复线（默认阈值 −5）发 RESOLVED。

```yaml
checks:
  account_usage:
    enabled: true
    interval: 5m
    source: passive          # passive 不打上游；active 更准但更重
    cooldown: 2h
    default_thresholds:
      - window: five_hour    # 5h 窗口
        utilization_gte: 80
        severity: P2
      - window: seven_day
        utilization_gte: 90
        severity: P1
    accounts:
      - id: 42
        name: claude-main
        chat_ids: ["123456789"]        # 可推给特定用户/群

        thresholds:                # 可选，覆盖 default
          - window: five_hour
            utilization_gte: 70
            severity: P1
        today:                     # 可选：今日本地统计
          cost_gte: 20
          tokens_gte: 2000000
          severity: P2
```

支持的 `window`：

| 值 | 含义 |
|----|------|
| `five_hour` / `5h` | Claude/Codex 等 5 小时窗口 |
| `seven_day` / `7d` | 7 天窗口 |
| `seven_day_sonnet` / `seven_day_fable` | 细分 7d 窗口 |
| `gemini_shared_daily` / `gemini_pro_daily` / `gemini_flash_daily` | Gemini 日配额 |
| `antigravity:<model>` | Antigravity 单模型利用率 |
| `max` | 所有可用窗口的最高利用率 |

## Telegram 架构

```
alerter.Engine ──Notifier──► telegram.Client
                               ├─ 多 chat（default + extra + 事件级覆盖）
                               ├─ 长消息分片（≤4000 runes）
                               ├─ parse 失败自动降级纯文本
                               └─ 全局限速 + 429 retry_after
```

- 运维类探针默认发到 `telegram.chat_id`（+ `extra_chat_ids`）
- `account_usage` 可为每个账号指定 `chat_ids`，实现「用量到了提醒某个人」

## 项目结构

```
sub2api-monitor/
├── cmd/monitor/          # 入口
├── internal/
│   ├── config/           # 配置加载
│   ├── sub2api/          # Admin API 客户端
│   ├── telegram/         # 多会话 Bot 客户端
│   ├── collector/        # 各探针（含 account_usage）
│   ├── alerter/          # 规则、去重、格式化、路由 chat
│   └── state/            # 本地状态（内存/文件）
├── config.example.yaml
├── docker-compose.yml
├── Dockerfile
├── Makefile
└── README.md
```

## 与 sub2api 内置告警的关系

| | Sub2API 内置 Ops Alerts | 本项目 |
|--|-------------------------|--------|
| 通道 | 邮件 | Telegram（多 chat） |
| 部署 | 内嵌主进程 | 独立进程 |
| 修改主项目 | — | 否 |
| 指标 | success_rate、error_rate、账号可用等 | 同上 + health + 账号明细 + **指定账号用量** |
| 推荐用法 | 主站规则引擎 | 把 `alert-events` 桥到 TG，并补充 health/账号/用量 |

两者可并存：在 sub2api 后台配置规则 → 本程序轮询 `ops/alert-events` 转发到 Telegram。

## 安全建议

- 使用 **最小权限** 的 Admin API Key，仅部署在可信网络
- 不要把 `config.yaml` / `.env` 提交到 git
- Telegram chat 建议使用私有群，限制 bot 权限
- 生产环境建议 HTTPS + 固定出口 IP（若 sub2api 有防火墙）
- `account_usage.source=active` 会触发上游用量查询，请控制 `interval` 与账号数量

## License

MIT
