set shell := ["bash", "-uc"]

default:
    @just --list

# 重新下載前端資產到 web/static/（已 commit，平常不用跑）
vendor:
    mkdir -p web/static
    curl -fsSL -o web/static/htmx.min.js "https://unpkg.com/htmx.org@2/dist/htmx.min.js"
    curl -fsSL -o web/static/daisyui.css "https://cdn.jsdelivr.net/npm/daisyui@5/daisyui.css"
    curl -fsSL -o web/static/ws.js "https://unpkg.com/htmx-ext-ws@2/dist/ws.js"
    curl -fsSL -o web/static/client-side-templates.js "https://unpkg.com/htmx-ext-client-side-templates@2/dist/client-side-templates.js"
    curl -fsSL -o web/static/mustache.min.js "https://unpkg.com/mustache@4/mustache.min.js"

# docker 沙箱登入一個帳號；憑證落在 accounts/<name>/.credentials.json
login name:
    mkdir -p "accounts/{{name}}"
    docker build -t claude-status-login -f docker/Dockerfile.login docker
    docker run -it --rm \
      --user "$(id -u):$(id -g)" \
      -e HOME=/tmp \
      -e CLAUDE_CONFIG_DIR=/data \
      -v "$(pwd)/accounts/{{name}}:/data" \
      claude-status-login
    chmod 600 "accounts/{{name}}/.credentials.json"

# 靜態建置單一執行檔 bin/claude-status
build:
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/claude-status .

# 交叉編譯 Windows 執行檔 → bin/claude-status.exe
build_win:
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/claude-status.exe .

# 交叉編譯 macOS 執行檔（Intel + Apple Silicon）→ bin/claude-status-darwin-{amd64,arm64}
build_mac:
    GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/claude-status-darwin-amd64 .
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/claude-status-darwin-arm64 .

# headless 後端開發模式（不 refresh token；log 寫到 ./log/claude-status.log）
run *flags:
    go run . serve --no-refresh {{flags}}

# TUI：第一個實例自動成為 host（log 面板顯示於帳號區下方），其餘當 client；host 掛掉時 client 自動升主
tui *flags:
    go run . tui {{flags}}

# 執行所有測試（含 race detector）
test:
    go test -race ./...

# 格式化 + 靜態檢查
fmt:
    gofmt -w . && go vet ./...
