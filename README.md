<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/rex-beacon-lockup-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/rex-beacon-lockup-light.svg">
    <img alt="rex-beacon" src="assets/rex-beacon-lockup-light.svg" width="520">
  </picture>
</p>

<p align="center">
  <strong>OSC 7501 Program Status emitter, Rex event daemon, Feishu/Lark rich card notifier, and Mac/iOS Command Palette inbox for <a href="https://www.superlogical.com/rex/docs">Rex</a>.</strong>
</p>

`rex-beacon` turns Rex terminal activity—coding agents (`antigravity`, `pi`, `claude`, `codex`), long-running builds, and listening dev servers—into:
- **Structured OSC 7501 status indicators** (`working`, `blocked`, `done`, `error`) inside Rex tabs & sessions
- **Feishu / Lark Interactive Cards** (with Session/Tab/Pane metadata, working directory, elapsed duration, and terminal output tail) + native **macOS notifications**
- **Cross-device Mac & iPhone Rex Command Palette Actions** (`Beacon: Attention Inbox…`, `Beacon: Reply to Blocked Pane…`, `Beacon: Peek Pane Output…`, `Beacon: Run Command & Notify…`)

---

## Features

1. **OSC 7501 Emitter & Command Wrapper**
   - `rex-beacon status` — spec-compliant OSC 7501 emitter (`state`, `kind`, `app`, `id`, `title`, `progress`, `msg` with UTF-8 base64 & control-char sanitization).
   - `rex-beacon wrap -- <cmd...>` — wraps any command (`make`, `cargo test`, `npm run build`), emitting `working` on start and `done`/`error` with elapsed time on completion.
   - `rex-beacon hook claude` / `rex-beacon claude install` — maps Claude Code lifecycle hooks (`UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `Notification`, `Stop`, `StopFailure`, `SessionEnd`) to OSC 7501.
   - `rex-beacon shell-init zsh` — provides `beacon_status` and `bw` (`beacon wrap`) shell helpers.

2. **Rex Event Daemon (`rex-beacon watch` / `rex-beacon daemon`)**
   - Streams `rex events --all --json` with automatic reconnect & backoff.
   - Tracks `working → done/error` duration per `(block_id, record_id)`, deduplicates repeated bursts with cooldown, and enriches alerts with `session.view`, `process` (`cwd`, foreground command), and `format` terminal tail lines.
   - Optional triggers for newly opened TCP ports (`notify_on_ports`), terminal bells (`notify_on_bell`), and non-zero process exits (`notify_on_non_zero_exit`).

3. **Multi-Platform Delivery Backends + Two-Way Chat Control**
   - **Feishu / Lark**:
     - `feishu_app`: Direct Feishu / Lark OpenAPI (`tenant_access_token` + `im/v1/messages` Schema 2.0 card with native `table` rendering) **plus two-way WebSocket long-connection control** (no public domain required): click `允许 (y)` / `选 1` / `拒绝 (n)` / `Ctrl+C` / `最新输出` buttons on cards, quote-reply any notification card to send text directly into the terminal pane, or message `/inbox`, `/peek`, `/reply <id> <text>`.
     - `webhook`: Feishu / Lark custom group bot webhook with optional HMAC-SHA256 signing.
     - `lark_cli`: Delegates to a locally authenticated `lark-cli` binary.
   - **Telegram (`telegram`)**: Bot API `sendMessage` with HTML formatting **plus `getUpdates` long-polling two-way control** (inline reply buttons, quoted replies, `/inbox`, `/peek`, `/reply`).
   - **DingTalk (`dingtalk`)**: Custom robot webhook (`markdown`, optional `SEC...` HMAC-SHA256 URL signing).
   - **WeCom / 企业微信 (`wecom`)**: Group bot webhook (`markdown` with semantic status colors).
   - **Discord (`discord`)**: Channel webhook with color-coded Rich Embeds & metadata fields.
   - **Slack (`slack`)**: Incoming webhook with colored Attachment + Block Kit sections.
   - **Generic JSON Webhook (`custom_webhook`)**: Posts structured JSON + optional custom headers and `X-Rex-Beacon-Signature` HMAC-SHA256 header.
   - **macOS (`macos`)**: Native macOS notification center alerts with configurable sound.

4. **Rex Mac & iOS Lua Plugin (`rex-beacon rex install`)**
   Installs `~/.config/rex/plugins/rex-beacon/init.lua` and wires it into `~/.config/rex/init.lua` without disturbing existing plugins (such as `leaf`):
   - **Beacon: Attention Inbox…** — Prioritized picker (`blocked > error > done > working`) across all sessions; jumps directly to the session & pane on Mac or iPhone.
   - **Beacon: Reply to Blocked Pane…** — Quick replies (`y`, `1`, `Enter`, `n`, `Ctrl+C`, or custom text) sent directly to a blocked pane without switching windows—great from Rex for iOS.
   - **Beacon: Peek Pane Output…** — Opens a floating layer showing the tail of any status-reporting pane.
   - **Beacon: Run Command & Notify…** — Prompts for a command and runs it wrapped in a new split.
   - **Beacon: Send Test Alert** / **Beacon: Daemon Control…**

---

## Install & Build

```sh
git clone https://github.com/ziludev/rex-beacon.git
cd rex-beacon
go build -o ~/.local/bin/rex-beacon ./cmd/rex-beacon
```

---

## Quick Start

### 1. Configure Notifications (`~/.config/rex-beacon/config.json`)

Using Feishu OpenAPI App (or export `FEISHU_APP_ID`, `FEISHU_APP_SECRET`, `FEISHU_RECEIVE_ID`):

```sh
rex-beacon init \
  --app-id "$FEISHU_APP_ID" \
  --app-secret "$FEISHU_APP_SECRET" \
  --receive-id "ou_xxxxxxxxxxxxxxxx" \
  --receive-id-type open_id
```

Or using a Feishu Custom Bot Webhook:

```sh
rex-beacon init --webhook-url "https://open.feishu.cn/open-apis/bot/v2/hook/..." --webhook-secret "..."
```

Or using local `lark-cli`:

```sh
rex-beacon init --lark-cli-chat "oc_xxxxxxxxxxxxxxxx"
```

### 2. Send a Test Card

```sh
rex-beacon notify --state blocked --kind permission --msg "Approve deploy to staging?"
```

### 3. Install the Rex Mac/iOS Lua Plugin

```sh
rex-beacon rex install
```

Open Command Palette (`Cmd+K` on Mac, or palette on iPhone) and search for **Beacon**.

### 4. Start the Background Event Daemon

```sh
rex-beacon daemon start
rex-beacon daemon status
rex-beacon daemon logs
```

### 5. Optional: Claude Code Hooks & Shell Helpers

```sh
# Install Claude Code OSC 7501 hooks into ~/.claude/settings.json
rex-beacon claude install

# Add to ~/.zshrc
eval "$(rex-beacon shell-init zsh)"
```

---

## Configuration Reference (`~/.config/rex-beacon/config.json`)

```json
{
  "all": true,
  "feishu_app": {
    "enabled": true,
    "brand": "feishu",
    "app_id": "cli_...",
    "app_secret": "...",
    "receive_id": "ou_...",
    "receive_id_type": "open_id",
    "interactive": true
  },
  "webhook": {
    "enabled": false,
    "url": "",
    "secret": ""
  },
  "lark_cli": {
    "enabled": false,
    "binary": "lark-cli",
    "as": "bot",
    "chat_id": "",
    "user_id": ""
  },
  "dingtalk": {
    "enabled": false,
    "url": "",
    "secret": ""
  },
  "wecom": {
    "enabled": false,
    "url": ""
  },
  "discord": {
    "enabled": false,
    "url": "",
    "username": "rex-beacon"
  },
  "slack": {
    "enabled": false,
    "url": ""
  },
  "telegram": {
    "enabled": false,
    "bot_token": "",
    "chat_id": "",
    "interactive": true
  },
  "custom_webhook": {
    "enabled": false,
    "url": "",
    "secret": "",
    "headers": {}
  },
  "macos": {
    "enabled": true,
    "sound": "Glass"
  },
  "rules": {
    "notify_states": ["blocked", "error", "done"],
    "min_working_duration_sec": 15,
    "cooldown_sec": 30,
    "tail_lines": 45,
    "notify_on_ports": false,
    "notify_on_bell": false,
    "notify_on_non_zero_exit": true,
    "skip_if_client_focused_now": false,
    "when_at_computer": "skip_all",
    "user_idle_threshold_sec": 60
  }
}
```

Environment overrides: `REX_BEACON_CONFIG`, `REX_BEACON_REX_BIN`, `FEISHU_APP_ID`, `FEISHU_APP_SECRET`, `FEISHU_RECEIVE_ID`, `FEISHU_RECEIVE_ID_TYPE`, `FEISHU_BRAND`, `FEISHU_WEBHOOK_URL`, `FEISHU_WEBHOOK_SECRET`, `DINGTALK_WEBHOOK_URL`, `DINGTALK_WEBHOOK_SECRET`, `WECOM_WEBHOOK_URL`, `DISCORD_WEBHOOK_URL`, `SLACK_WEBHOOK_URL`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `REX_BEACON_WEBHOOK_URL`, `REX_BEACON_WEBHOOK_SECRET`, `REX_BEACON_MACOS_ENABLED`.
