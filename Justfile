set shell := ["bash", "-uc"]

default:
    @just --list

# 重新下載前端資產到 web/static/（已 commit，平常不用跑）
vendor:
    mkdir -p web/static
    curl -fsSL -o web/static/htmx.min.js "https://unpkg.com/htmx.org@2/dist/htmx.min.js"
    curl -fsSL -o web/static/daisyui.css "https://cdn.jsdelivr.net/npm/daisyui@5/daisyui.css"

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

# 開發模式（絕不 refresh token，可安全借用既有憑證測試）
run *flags:
    go run . serve --no-refresh {{flags}}

# TUI（standalone 會 refresh token；web daemon 已在跑時請改用 --remote <url>）
tui *flags:
    go run . tui {{flags}}

test:
    go test ./...

fmt:
    gofmt -w . && go vet ./...
