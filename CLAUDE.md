# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Self-hosted multi-account Claude **subscription usage** dashboard. A single Go binary background-polls Anthropic's private `/api/oauth/usage` endpoint (read-only, consumes no inference) and shows each account's 5h/7d/Opus/Sonnet utilization + reset countdowns via a web dashboard (Gin + htmx + daisyUI + WebSocket) and a bubbletea TUI.

## Commands

```sh
just build              # static binary -> bin/claude-status (CGO_ENABLED=0)
just build_win          # cross-compile windows/amd64
just build_mac          # cross-compile darwin amd64 + arm64
just test               # go test -race ./...
just fmt                # gofmt -w . && go vet ./...
just run                # dev: serve --no-refresh (log to ./log/)
just tui                # TUI mode
just login <name>       # docker sandbox OAuth login -> accounts/<name>/.credentials.json
just vendor             # re-download web/static frontend assets (already committed)
```

Run a single test: `go test -race -run TestName ./internal/anthropic/` (the `anthropic` package holds nearly all the interesting tests; httptest-based, no network).

## Architecture

- **Two subcommands, one binary** (`main.go`): `tui` (default) and `serve`. Both run the **same backend** via `startBackend` (store + per-account pollers + Gin HTTP/WS). `serve` is headless; the TUI *host* runs the backend in-process and the screen attaches to it. Extra `tui` instances attach as **clients** over the WebSocket and auto-promote to host if the host dies.
- **Single-owner lock** (`internal/lock`): only one process per machine actually hits upstream, enforced by a file lock on the accounts dir. This is why refresh-token single-use safety holds. Lock is split by build tag: `lock_unix.go` (flock) / `lock_windows.go` (LockFileEx, mandatory — it deliberately locks a high byte offset so the JSON stays readable by `ReadInfo`).
- **Data flow**: `poller.poll()` → `store.SetUsage/SetError` → `store` broadcasts a cap-1 channel → two WS routes fan out from the same broadcast: `/ws` sends raw `view.Build(snaps)` JSON (consumed by the TUI's `ReadJSON`), `/ws/html` renders the same data through the `"usage"` `html/template` and pushes HTML that htmx's morphdom-swap extension OOB-morphs into `#usage`. Both share the subscribe/ping/read-drain loop via `serveWS` in `main.go`. `internal/view` is the shared wire model; `internal/format` does reset-time formatting.
- **Decoupled poll rates**: the background poller hits Anthropic per account at `--poll-interval` (hard floor `poller.MinInterval` = 180s, clamped); browsers/TUI only read the local `/ws`, never amplifying upstream load.
- **slog routing**: `serve` → stderr + `log/claude-status.log`; `tui` client → `io.Discard`; `tui` host → in-memory ring (`internal/logbuf`, shown in the TUI) + file.

## Critical, non-obvious constraints

- **The two upstream endpoints want OPPOSITE User-Agents** (`internal/anthropic/usage.go`). This is the single easiest thing to break:
  - **Usage** `api.anthropic.com/api/oauth/usage` → **must** send `User-Agent: claude-code/<ver>` (`UserAgent` const) + `anthropic-beta`. Without the claude-code UA it 429s.
  - **Token refresh** `platform.claude.com/v1/oauth/token` → **must** send `User-Agent: axios/<ver>` (`refreshUserAgent` const) and **no** `anthropic-beta`. A `claude-code/*` or `curl/*` UA gets 429'd *before grant validation*. The token endpoint host moved here from the now-dead `console.anthropic.com`.
- **OAuth refresh tokens are single-use / rotate on every refresh.** The dashboard must be the *sole* owner of each grant. Sharing an `accounts/<name>/.credentials.json` with any other claude process (incl. the `claude` CLI / sandbox) rotates the token out from under us → `invalid_grant`. The service only reads the file at startup and does not detect external rotation.
- **Refresh is globally serialized** through `internal/refresh` (the `Gate`: one refresh at a time, min gap `--refresh-min-interval`) plus a per-account 429 cooldown (`refreshNotBefore` in `creds.go`), to avoid stampeding/hammering the token endpoint.
- **Credentials are written atomically** (temp file + rename, 0600) preserving unknown JSON fields (`saveLocked` in `creds.go`).
- **`/` and `/ws/html` render via `html/template`** (`web/templates/*.html`, embedded and parsed once as `pageTmpl`). `/ws` stays raw JSON (`view.Data`) — the TUI (`internal/tui/source.go`) reads it directly with `ReadJSON`, so don't change its wire format without checking the TUI first.
- **Accounts are zero-config**: `accounts/<dirname>/.credentials.json`, where the directory name *is* the account name. `accounts/` and `bin/` are gitignored.
- Service binds `127.0.0.1` only; tokens never go to logs. Pure Go (`CGO_ENABLED=0`) cross-compiles to linux/windows/darwin × amd64/arm64.
