// claude-status: self-hosted multi-account Claude subscription usage
// dashboard. The default command, `tui`, opens the terminal UI; the first
// instance hosts the backend (the sole token refresher) and the rest attach to
// it as clients. `serve` runs the same backend headless (web UI only).
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"go-gin-claude-status/internal/anthropic"
	"go-gin-claude-status/internal/lock"
	"go-gin-claude-status/internal/logbuf"
	"go-gin-claude-status/internal/poller"
	"go-gin-claude-status/internal/store"
	"go-gin-claude-status/internal/tui"
)

//go:embed web/templates
var templatesFS embed.FS

//go:embed web/static
var staticFS embed.FS

func main() {
	args := os.Args[1:]
	cmd := "tui"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		runServe(args)
	case "tui":
		runTUI(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (available: serve, tui)\n", cmd)
		os.Exit(2)
	}
}

// activateLogging redirects the global slog default (and the bridged log
// package) to extra and a persistent append-only file under logDir. On any
// filesystem error it falls back to writing only to extra and logs a warning.
func activateLogging(extra io.Writer, logDir string) {
	w := extra
	if err := os.MkdirAll(logDir, 0o700); err == nil {
		f, err := os.OpenFile(filepath.Join(logDir, "claude-status.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			w = io.MultiWriter(extra, f)
		} else {
			slog.Warn("could not open log file, logging to output only", "err", err)
		}
	} else {
		slog.Warn("could not create log dir, logging to output only", "err", err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// startBackend wires the store, per-account pollers and HTTP API onto an
// already-bound listener, serving until ctx is cancelled. It returns the store
// so a TUI host can read snapshots in-process. Shared by `serve` (headless) and
// the TUI host path; the caller owns the single-owner lock.
func startBackend(ctx context.Context, ln net.Listener, accountsDir string, interval time.Duration, noRefresh bool) (*store.Store, error) {
	if interval < poller.MinInterval {
		interval = poller.MinInterval
	}
	client := anthropic.NewClient()
	accounts, err := discoverAccounts(accountsDir, client, noRefresh)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(accounts))
	for i, acc := range accounts {
		names[i] = acc.Name
	}
	st := store.New(names)
	for _, acc := range accounts {
		p := &poller.Poller{Account: acc, Client: client, Store: st, Interval: interval}
		go p.Run(ctx)
	}
	srv := &http.Server{Handler: newRouter(st)}
	go func() {
		<-ctx.Done()
		srv.Close() // immediate close frees the port fast so failover can take over
	}()
	go srv.Serve(ln)
	return st, nil
}

func runServe(args []string) {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fl.String("listen", "127.0.0.1:8787", "address to bind (keep it on loopback)")
	accountsDir := fl.String("accounts-dir", "accounts", "directory with one subdirectory per account, each holding "+anthropic.CredentialsFile)
	interval := fl.Duration("poll-interval", 5*time.Minute, "upstream poll interval per account (min 180s)")
	noRefresh := fl.Bool("no-refresh", false, "never refresh tokens (for dev/testing with borrowed credentials)")
	logDir := fl.String("log-dir", "log", "directory for the server log file")
	fl.Parse(args)

	lockPath, err := lock.PathFor(*accountsDir)
	if err != nil {
		log.Fatal(err)
	}
	lk, ok, err := lock.Acquire(lockPath)
	if err != nil {
		log.Fatal(err)
	}
	if !ok {
		info, _ := lock.ReadInfo(lockPath)
		log.Fatalf("a backend is already running at %s (pid %d)", info.Addr, info.PID)
	}
	defer lk.Release()
	activateLogging(os.Stderr, *logDir)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := startBackend(ctx, ln, *accountsDir, *interval, *noRefresh); err != nil {
		log.Fatal(err)
	}
	if err := lk.WriteInfo(lock.Info{Addr: *listen, PID: os.Getpid()}); err != nil {
		log.Fatal(err)
	}
	slog.Info(fmt.Sprintf("serving on http://%s (poll interval %s)", *listen, max(*interval, poller.MinInterval)))
	<-ctx.Done()
}

func runTUI(args []string) {
	fl := flag.NewFlagSet("tui", flag.ExitOnError)
	listen := fl.String("listen", "127.0.0.1:8787", "shared backend address: bind it to host, or attach to whoever already holds it")
	remote := fl.String("remote", "", "force pure client mode against this base URL (e.g. http://127.0.0.1:8787); never hosts, never touches credentials")
	accountsDir := fl.String("accounts-dir", "accounts", "directory with one subdirectory per account, each holding "+anthropic.CredentialsFile)
	interval := fl.Duration("poll-interval", 5*time.Minute, "upstream poll interval per account (min 180s)")
	noRefresh := fl.Bool("no-refresh", false, "never refresh tokens (for dev/testing with borrowed credentials)")
	logDir := fl.String("log-dir", "log", "directory for the server log file")
	fl.Parse(args)

	ring := logbuf.New(200)

	// Alt-screen mode owns the terminal; route slog to Discard until this
	// process becomes host. Fetch errors still reach the user via snapshots.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var cfg tui.Config
	if *remote != "" {
		cfg = tui.Config{URL: strings.TrimRight(*remote, "/") + "/api/usage"}
	} else {
		lockPath, err := lock.PathFor(*accountsDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		logDirStr, accDir, iv, nr := *logDir, *accountsDir, *interval, *noRefresh
		cfg = tui.Config{
			BindAddr: *listen,
			URL:      "http://" + *listen + "/api/usage",
			LockPath: lockPath,
			Host: func(ctx context.Context, ln net.Listener) (func() []store.Snapshot, error) {
				activateLogging(ring, logDirStr)
				st, err := startBackend(ctx, ln, accDir, iv, nr)
				if err != nil {
					return nil, err
				}
				return st.Snapshots, nil
			},
		}
	}

	b, err := tui.Connect(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := tui.Run(ctx, b, ring); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func discoverAccounts(dir string, client *anthropic.Client, noRefresh bool) ([]*anthropic.Account, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("accounts dir: %w (run `just login <name>` to create one)", err)
	}
	var accounts []*anthropic.Account
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		credPath := filepath.Join(dir, e.Name(), anthropic.CredentialsFile)
		if _, err := os.Stat(credPath); err != nil {
			slog.Warn(fmt.Sprintf("skipping %s: no %s", filepath.Join(dir, e.Name()), anthropic.CredentialsFile))
			continue
		}
		acc, err := anthropic.LoadAccount(e.Name(), credPath, client, noRefresh)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, acc)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no accounts found (expected %s/<name>/%s) — run `just login <name>` first", dir, anthropic.CredentialsFile)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	return accounts, nil
}

func newRouter(st *store.Store) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetHTMLTemplate(template.Must(template.ParseFS(templatesFS, "web/templates/*.html")))

	staticSub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		panic(err)
	}
	r.StaticFS("/static", http.FS(staticSub))

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", buildView(st.Snapshots()))
	})
	r.GET("/partials/usage", func(c *gin.Context) {
		c.HTML(http.StatusOK, "usage.html", buildView(st.Snapshots()))
	})
	r.GET("/api/usage", func(c *gin.Context) {
		c.JSON(http.StatusOK, st.Snapshots())
	})
	r.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return r
}
