# claude-status

**一眼看完所有 Claude 訂閱帳號的用量。**

用訂閱方案（OAuth）登入 Claude Code 時，`/status` 屬於 inference-only scope，**看不到剩餘用量與重置時間** — 只能自己上 claude.ai → Settings → Usage 一個帳號一個帳號慢慢查。`claude-status` 是自架的儀表板，把每個帳號、每個速率限制視窗的即時用量，集中顯示在瀏覽器或終端機。

[English](README.md)

## 畫面預覽

**網頁儀表板** — 所有帳號、每個速率限制視窗，透過 WebSocket 即時更新：

![網頁儀表板](docs/web.png)

**終端 UI** — 同樣的資料顯示在終端機（`./claude-status tui`）：

![終端 UI](docs/tui.png)

> `personal`／`work` 只是你登入各帳號時自取的標籤。

## 為何需要此工具

我在同一台機器上用多個 Claude 訂閱帳號（一個私人帳號、一個工作用帳號）—— 做法是每個帳號各產生一個長期 OAuth token，再用 shell alias 切換：

```sh
claude setup-token             # 印出長期 OAuth token，每個帳號跑一次
mkdir -p ~/.claude-tokens && chmod 700 ~/.claude-tokens
printf %s '貼上私人帳號 token' > ~/.claude-tokens/personal
printf %s '貼上工作用帳號 token' > ~/.claude-tokens/work
chmod 600 ~/.claude-tokens/*

# 決定每次 `claude` 用哪個帳號
alias personal='CLAUDE_CODE_OAUTH_TOKEN="$(cat ~/.claude-tokens/personal)" claude'
alias work='CLAUDE_CODE_OAUTH_TOKEN="$(cat ~/.claude-tokens/work)" claude'
```

> **實測坑：** 若 `~/.claude/.credentials.json` 存在（互動式 `/login` 留下的），它會**蓋過** `CLAUDE_CODE_OAUTH_TOKEN`，讓所有 alias 都被綁到那一個帳號 —— 與官方文件標示的優先序相反。解法：`mv ~/.claude/.credentials.json{,.bak}`，之後別再於 `~/.claude` 跑 `/login`。*（實測行為，可能隨 Claude Code 版本變動。）*

問題就出在這裡：用這種方式（訂閱 / OAuth）登入後，**`/status` 屬於 inference-only scope，看不到剩餘用量與重置時間**。內建唯一能查的方式只有 claude.ai → Settings → Usage，而且一次只能看一個帳號。

`claude-status` 就是為了解決這件事。它獨立管理每個帳號的憑證，把它們全部顯示在同一個儀表板：

- **每個帳號一個獨立的 OAuth grant** — 帳號之間不會發生 single-use token 碰撞
- **即時用量**，涵蓋每個速率限制視窗（5h / 7d / Opus / Sonnet）
- **網頁儀表板 + 可選的終端 UI**
- **唯讀、零推論額度消耗** — 它呼叫的是 Claude Code 用來顯示你額度的*同一個* `GET /api/oauth/usage` 端點。不發送任何 prompt、不消耗額度，token 只留在 `127.0.0.1`。

> 上面的 alias 只是 CLI 切帳號用的。dashboard 取得憑證的方式不同 —— 每個帳號各跑一次拋棄式 `/login`（見下方步驟 1），而且是**各自獨立的** OAuth grant，兩者互不干擾。

## 系統需求

只需要 **[Docker](https://docs.docker.com/get-docker/)** — 每個帳號用一次，在拋棄式容器裡產生憑證。你**不需要**安裝 Go、Node 或任何建置工具（連這個 repo 都不用）：直接到 [Releases](../../releases) 頁面下載預先編好的執行檔即可。

## 1 · 產生帳號憑證

一條指令就在拋棄式的 [`node` 容器](https://hub.docker.com/_/node)（Docker 官方映像）裡跑 Claude Code 登入，只有產生的 `.credentials.json` 會寫到你的機器上，其他都不留。

```sh
# 登入一個帳號 — 名稱自取（例如 "personal"）
docker run -it --rm \
  -e CLAUDE_CONFIG_DIR=/data \
  -v "$PWD/accounts/personal:/data" \
  node:22-slim \
  sh -c "npm install -g @anthropic-ai/claude-code && claude /login"
```

會開啟互動式 Claude Code — 跟著提示走（選一個 theme），接著會出現登入連結：用瀏覽器開啟、授權、再把驗證碼貼回來。憑證會產生在 `accounts/personal/.credentials.json`。要新增帳號就換個名稱（`work`、`research`…）重跑一次。

> **Linux：** 容器以 root 執行，新檔案會是 root 擁有 — 跑完後執行 `sudo chown -R "$USER" accounts/`。（macOS/Windows 的 Docker Desktop 不需要。）
> **Windows：** 請在 WSL 或 Git Bash 執行（`$PWD` 是 Unix shell 語法）。

## 2 · 下載並執行

1. 到 [Releases](../../releases) 頁面下載對應你作業系統的執行檔 — 例如 `…-linux-amd64`、`…-windows-amd64.exe`、`…-darwin-arm64`（Apple Silicon）或 `…-darwin-amd64`（Intel Mac）。
2. 放到包含 `accounts/` 目錄的資料夾裡。macOS/Linux 請賦予執行權限：`chmod +x claude-status-*`。
3. 啟動：

```sh
./claude-status serve        # 無介面的網頁儀表板
```

在瀏覽器開啟 **http://127.0.0.1:8787**。

想用終端 UI？改執行 `./claude-status`（或 `./claude-status tui`）— 它會在終端機顯示儀表板，*同時*也提供網頁 UI。按 `R` 刷新、`Q` 結束。

## 注意事項

- **憑證請保密且不可共用。** 每個 `accounts/<name>/.credentials.json` 都是 single-use — 不要複製到別台機器、也不要對同一帳號同時跑兩個實例，否則 OAuth grant 會壞掉（`invalid_grant`）。備份 `accounts/` 資料夾即可保留你的登入。
- **只綁本機。** 服務預設綁定 `127.0.0.1`，不會把 token 送上網路。需要遠端存取請用 SSH/VPN tunnel，不要直接對外開 port。
- **常用旗標：** `--poll-interval 10m`（多久抓一次用量，最少 3m）、`--listen 127.0.0.1:9999`（換 port）、`--accounts-dir <dir>`。
