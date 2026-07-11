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

### 预编译 Nightly（推荐快速试用）

GitHub Actions 会在 `main` 有代码变更时以及每日定时构建 **rolling nightly** 发布（标签 `nightly`，prerelease）：

- 页面：https://github.com/boa-z/sub2api-monitor/releases/tag/nightly
- 产物：Linux / macOS / Windows（amd64、arm64）压缩包 + `SHA256SUMS.txt`
- 二进制内嵌 `-version`：`nightly-YYYYMMDD-<sha>`

```bash
# 示例：Linux amd64
# 从 Releases 下载对应 tar.gz 后：
tar -xzf sub2api-monitor_nightly-*_linux_amd64.tar.gz
cp config.example.yaml config.yaml   # 仓库内示例；或从源码拷贝
# 编辑 config.yaml
./sub2api-monitor_nightly-*_linux_amd64 -config config.yaml
# 或
./sub2api-monitor_nightly-*_linux_amd64 -version
```

CI：推送 / PR 会跑 `gofmt` + `go vet` + `go test` + 构建冒烟；Nightly 发布使用仓库 `GITHUB_TOKEN` 更新 `nightly` tag 上的资产。

### 4. Docker

```bash
docker compose up -d
```

## Telegram 用户态面板

开启后，用户可在 **私聊 Bot** 中自助配置自己的监控，无需改服务器 YAML。支持**多用户**与 **管理员 / 普通用户** 权限区分。

```yaml
telegram:
  bot_token: "..."
  chat_id: "管理员ID"        # 全局运维告警默认 chat；admin_user_ids 为空时也作为 sole admin 回退
  panel:
    enabled: true
    admin_user_ids: [123456789]   # 管理员：运维视图 + 账号管理写操作；只读运维请用 profile.role=viewer
    # allow_user_ids: [987654321] # 可选：普通用户白名单；非空时仅列表内 + 管理员可用
    open_registration: true       # allow 列表为空时开放注册（用户间数据隔离）
    # allow_all: false
    users_path: "./data/users.json"
    check_interval: 5m
    cooldown: 2h
```

### 角色与权限

支持三级角色：**管理员 (admin)** / **只读运维 (viewer)** / **普通用户 (user)**。

| 能力 | 普通用户 | 只读运维 | 管理员 |
|------|----------|----------|--------|
| 连接配置（自己的 Base/Key） | ✅ | ✅ | ✅ |
| 导入全局 `sub2api` 连接（seed） | ❌ | ❌ | ✅ |
| 监控账号 / 阈值 / 立即检查 | ✅ | ✅ | ✅ |
| 运维视图（看板/可用性/告警/错误/并发/渠道/异常账号） | ❌ | ✅ 只读 | ✅ |
| 账号浏览 / 搜索 / 实例用户与分组（搜索+详情） | ❌ | ✅ | ✅ |
| 错误标记已解决 / 批量清错·恢复·开调度·清限速·一键修复 | ❌ | ❌ | ✅ |
| 账号写操作（调度/启停/清错/恢复/刷新/测试/临时停/重置额度） | ❌ | ❌ | ✅ |
| 面板用户角色（admin/viewer/user/清除覆盖） | ❌ | ❌ | ✅ |

判定优先级：

1. `data/users.json` 中 `profile.role` 为 `admin` / `viewer` / `user` 时**覆盖**配置  
2. 否则看 `telegram.panel.admin_user_ids`（仅授予 admin）  
3. 若管理员列表为空，则数字型 `telegram.chat_id` 回退为 sole admin  

主面板键盘按角色显示入口；只读运维可看运维视图但隐藏修复/调度/角色写按钮；写操作回调对非管理员拒绝。

### 多用户交付建议

1. **一个 Bot Token**：所有用户私聊同一 Bot；配置按 `telegram_user_id` 隔离在 `users.json`。
2. **开放注册 + 自备 Key（推荐租户模式）**  
   - `open_registration: true`  
   - 普通用户填写**自己实例**的 Admin API Key  
   - 管理员只管 Bot 进程与全局告警通道  
3. **共享实例（运维模式）**  
   - 配置全局 `sub2api.base_url` + `admin_api_key`  
   - 仅 `admin_user_ids` 可「使用全局配置」导入 Key  
   - **不要**把共享 Admin Key 交给不可信用户（Admin API 可改调度/清错等）  
4. **混合**  
   - 白名单 `allow_user_ids` 限制谁能进面板  
   - 需要写权限的人进 `admin_user_ids` 或 `role: admin`；只需只读运维的设 `role: viewer`
5. **提权 / 降权**（运行时）  
   - **面板内（推荐）**：管理员 → 账号管理 → **面板用户** → 选择用户 → 设为管理员 / 只读运维 / 用户 / 清除覆盖  
   - 或编辑 `data/users.json` 将对应用户 `role` 设为 `admin` / `viewer` / `user` / 删除字段（继承配置）；`Update` 持久化后立即生效  
   - 或改配置 `admin_user_ids` 后重启进程  
6. **权限边界**  
   - 本 Bot 的「管理员」只控制 **监控面板** 功能入口  
   - 真正改 Sub2API 账号仍取决于用户填写的 **Admin API Key** 权限；勿把生产 Admin Key 分给不可信租户  

### 用户操作

1. 私聊 Bot 发送 `/start`（会注册命令菜单）
2. **连接配置** → 设置 Base URL、Admin API Key → **测试连接**（health + dashboard）
3. **监控账号** → 手动输入 ID，或 **从列表选择**（分页拉取 Admin 账号）
4. **阈值** → 按窗口（5h / 7d / Gemini…）设置使用率百分比；可重置为系统默认
5. 保持「监控开启」；后台按 `check_interval` 拉用量，超阈值私聊提醒
6. **立即检查** 查看各窗口使用率、重置时间、今日 req/token/cost，并标记已超阈值窗口

常用命令：`/start` `/status` `/check` `/setbase` `/setkey` `/addaccount` `/delaccount` `/thresholds` `/id` `/help` `/cancel`  
管理员：`/ops` 运维视图 · `/manage` 账号管理 · `/search` 搜索账号。

### 面板能力一览

| 功能 | 说明 |
|------|------|
| 连接 | Base URL / Admin API Key / 测试连接 / 清除；管理员可导入全局配置 |
| 监控账号 | 手动 ID / 列表选择 / 重命名 / 单账号启停 / 删除 |
| 阈值 | 多窗口百分比；自定义或系统默认；可删除单窗口 |
| 立即检查 | 拉 usage + today-stats + 状态；超阈值/异常账号直达实时或管理；管理员可跳运维 |
| **运维视图**（管理员） | 主面板运维快照 + 看板/异常快捷；看板 / 可用性 / 内置告警（触发·已恢复汇总，文案抽账号直达管理）/ 请求与上游错误（分标签+分页，解决后保留页码/批量/直达修复·实时·管理）/ 并发 / 渠道探测（启用·正常·异常筛选、详情、异常优先、7d 可用率）/ 异常账号（error·限速·停调度·汇总分标签+分页，管理/实时/修复/批量/一键监控）；运维菜单含健康摘要；看板一键跳转异常/限速/错误；可用性/并发/告警/渠道上下文跳转；账号管理返回来源记忆；批量操作带进度与失败账号直达管理；各视图可刷新 |
| **账号管理**（管理员） | 健康摘要 + **当前筛选徽章**；浏览（状态/平台/搜索/异常汇总）/ 批量清错·恢复·开调度·清限速·一键修复（均需确认；**优先当前浏览/异常 tab 范围**，按钮标签显示范围）/ 实例用户与分组（搜索/状态·平台筛选/详情只读） / **面板用户**（角色 admin/viewer/user/清除 + 开关监控/数据源）/ 单账号与**上下文优先**实时页（按异常/限速/停调度优先动作）/ 调度开关（二次确认）/ 启停状态 / 测试连通 / 清错误·限速 / 恢复·刷新 / 临时停调度（15m–24h）/ 重置额度 / 用量快照 / 加入监控 |
| 权限 | `admin_user_ids` + `allow_user_ids` / `allow_all` / `open_registration` / profile.role / 回退 `chat_id` |
| 后台轮询 | `UserUsageCollector` 按用户隔离告警；拉取失败会发 P3 提示 |

运维视图只读；账号管理通过用户自己的 Admin API 调用 Sub2API 管理接口，不会改本监控程序配置以外的服务器 YAML。

### 数据模型

```
data/users.json
└─ users[]
   ├─ telegram_user_id / chat_id
   ├─ role                       # 可选 admin|viewer|user，覆盖配置级角色
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


## Discord Bot 面板

支持 **Discord** 作为通知通道 + 交互面板（与 Telegram 面板能力对齐，共享 `users.json` 多用户隔离与管理员权限模型）。

```yaml
discord:
  bot_token: "你的 Bot Token"
  default_channel_id: "全局告警频道ID"   # 可选
  guild_id: "开发服务器ID"               # 可选，加速 slash 同步
  panel:
    enabled: true
    admin_user_ids: [123456789012345678] # Discord 用户 snowflake
    open_registration: true
    # users_path 默认复用 telegram.panel.users_path
```

### 准备 Bot

1. [Discord Developer Portal](https://discord.com/developers/applications) 创建 Application → Bot → Reset Token  
2. OAuth2 → URL Generator：scopes 勾选 `bot` + `applications.commands`；权限至少 Send Messages / Use Slash Commands / Embed Links  
3. 用生成的 URL 邀请进服务器；面板也可在 **私信** 使用 slash 命令  
4. 复制自己的用户 ID（开发者模式 → 右键用户 → 复制 ID）写入 `admin_user_ids`

### 斜杠命令

| 命令 | 说明 |
|------|------|
| `/panel` `/status` | 主面板（按钮导航） |
| `/check` | 立即检查用量 |
| `/setbase` `/setkey` | 配置连接 |
| `/addaccount` | 添加监控账号 |
| `/ops` `/manage` | 管理员：运维 / 账号管理 |
| `/help` | 帮助 |

按钮交互：连接、监控账号、阈值；管理员可用运维（看板/可用性/告警/错误分标签分页+平台模型/并发/渠道/异常账号分标签分页+管理/实时/修复）、账号浏览（状态·平台·停调度·限速·异常汇总 + 下拉选择）、单账号管理与**上下文优先实时页**、批量清错·恢复·开调度·清限速·一键修复（确认后执行，优先当前筛选范围）等。  
普通用户键盘隐藏运维/管理入口；只读运维（`role: viewer`）可见运维只读视图；管理员由 `discord.panel.admin_user_ids` 或 `role: admin` 判定（无 Telegram 式 chat_id 回退，需显式配置）。

### 告警路由

- Discord 用户监控告警发往 **用户 DM**（`discord:<user_id>`）  
- 全局 ops 告警可发到 `default_channel_id`  
- 与 Telegram / 飞书可同时启用（`notify` 多通道 fan-out）

### 与 Telegram 并存

- 同一进程可同时开 `telegram.panel` + `discord.panel`  
- 默认共享 `./data/users.json`；用 `platform` 字段区分来源  
- **注意**：Telegram 与 Discord 的数字 ID 空间不同，一般不会冲突；勿手工复用 ID

## 多通道通知架构

告警与推送已从 Telegram 解耦，便于接入飞书等第三方：

```
collector ──Emit──► alerter.Engine
                       │ 格式化 plain/HTML/Markdown
                       ▼
                  notify.Multi (fan-out)
              /        |         \
     telegram.Channel  feishu   discord.Channel
     (Bot API)       (Webhook)  (Bot REST + DM)
              \        |         /
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
│   ├── discord/          # Discord REST + Gateway
│   ├── panel/            # Telegram 交互面板
│   │   └── discordpanel/ # Discord 交互面板
│   ├── userstore/        # 多用户配置（users.json）
│   ├── notify/           # 多通道通知（TG/飞书/Discord）
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
