# claude-status

Self-hosted dashboard for watching your Claude subscription usage across **multiple accounts** — in the browser or the terminal.

[中文說明](README_tw.md)

## Screenshots

**Web dashboard** — every account, every rate-limit window, live over WebSocket:

![Web dashboard](docs/web.png)

**Terminal UI** — the same data in your terminal (`./claude-status tui`):

![Terminal UI](docs/tui.png)

> The `personal` / `work` names are just labels you pick when logging in each account.

## Why this exists

[Claude Code](https://claude.com/claude-code) stores a single OAuth login per session, so there's no convenient way to keep an eye on usage across several Claude subscriptions at once. `claude-status` manages each account's credentials independently and shows them all on one dashboard:

- **One isolated OAuth grant per account** — no single-use-token collisions between accounts
- **Live usage** for every rate-limit window (5h / 7d / Opus / Sonnet)
- **Web dashboard + optional terminal UI**
- **Read-only, zero inference cost** — it calls the *same* `GET /api/oauth/usage` endpoint Claude Code uses to draw your limits. It never sends prompts, never spends quota, and keeps your tokens on `127.0.0.1` only. Your login here is a separate grant that does not interfere with your day-to-day Claude CLI login.

## Requirements

Just **[Docker](https://docs.docker.com/get-docker/)** — used once per account to generate credentials in a throwaway container. You don't need Go, Node, or any build tools (not even this repo): download a prebuilt binary from the [Releases](../../releases) page.

## 1 · Create an account credential

One command runs Claude Code's login inside a disposable [`node` container](https://hub.docker.com/_/node) (a Docker Official Image). Only the resulting `.credentials.json` is written to your machine — nothing else persists.

```sh
# Log in an account — name it whatever you like (e.g. "personal")
docker run -it --rm \
  -e CLAUDE_CONFIG_DIR=/data \
  -v "$PWD/accounts/personal:/data" \
  node:22-slim \
  sh -c "npm install -g @anthropic-ai/claude-code && claude /login"
```

An interactive Claude Code session opens — follow the prompts (pick a theme), then a login link appears: open it in your browser, authorize, and paste the code back. The credential lands at `accounts/personal/.credentials.json`. Repeat with a different name (`work`, `research`, …) for each extra account.

> **Linux:** the container runs as root, so the new files are root-owned — run `sudo chown -R "$USER" accounts/` afterward. (Not needed with Docker Desktop on macOS/Windows.)
> **Windows:** run the command in WSL or Git Bash (`$PWD` is Unix shell syntax).

## 2 · Download & run

1. Grab the binary for your OS from the [Releases](../../releases) page — e.g. `…-linux-amd64`, `…-windows-amd64.exe`, `…-darwin-arm64` (Apple Silicon) or `…-darwin-amd64` (Intel Mac).
2. Put it in the folder that contains your `accounts/` directory. On macOS/Linux make it executable: `chmod +x claude-status-*`.
3. Start it:

```sh
./claude-status serve        # headless web dashboard
```

Open **http://127.0.0.1:8787** in your browser.

Prefer a terminal UI? Run `./claude-status` (or `./claude-status tui`) instead — it shows the dashboard in your terminal *and* serves the web UI. Press `R` to refresh, `Q` to quit.

## Good to know

- **Keep credentials private & unique.** Each `accounts/<name>/.credentials.json` is single-use — don't copy it to another machine or run two instances on the same account, or the OAuth grant breaks (`invalid_grant`). Back up the `accounts/` folder to preserve your logins.
- **Localhost only.** The server binds to `127.0.0.1` by default and never sends tokens over the network. For remote access, tunnel over SSH/VPN rather than exposing the port.
- **Handy flags:** `--poll-interval 10m` (how often to fetch usage, min 3m), `--listen 127.0.0.1:9999` (change the port), `--accounts-dir <dir>`.
