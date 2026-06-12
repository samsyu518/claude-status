// claude-status: self-hosted multi-account Claude subscription usage
// dashboard. `serve` (default) runs the web UI; `tui` is reserved for the
// future bubbletea client (see docs/TUI-HANDOFF.md).
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
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
	"go-gin-claude-status/internal/poller"
	"go-gin-claude-status/internal/store"
)

//go:embed web/templates
var templatesFS embed.FS

//go:embed web/static
var staticFS embed.FS

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		runServe(args)
	case "tui":
		fmt.Fprintln(os.Stderr, "tui is not implemented yet — see docs/TUI-HANDOFF.md")
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (available: serve, tui)\n", cmd)
		os.Exit(2)
	}
}

func runServe(args []string) {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fl.String("listen", "127.0.0.1:8787", "address to bind (keep it on loopback)")
	accountsDir := fl.String("accounts-dir", "accounts", "directory with one subdirectory per account, each holding "+anthropic.CredentialsFile)
	interval := fl.Duration("poll-interval", 5*time.Minute, "upstream poll interval per account (min 180s)")
	noRefresh := fl.Bool("no-refresh", false, "never refresh tokens (for dev/testing with borrowed credentials)")
	fl.Parse(args)

	if *interval < poller.MinInterval {
		log.Printf("poll-interval %s too low, clamping to %s", *interval, poller.MinInterval)
		*interval = poller.MinInterval
	}

	client := anthropic.NewClient()
	accounts, err := discoverAccounts(*accountsDir, client, *noRefresh)
	if err != nil {
		log.Fatal(err)
	}

	names := make([]string, len(accounts))
	for i, acc := range accounts {
		names[i] = acc.Name
	}
	st := store.New(names)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, acc := range accounts {
		p := &poller.Poller{Account: acc, Client: client, Store: st, Interval: *interval}
		go p.Run(ctx)
	}

	srv := &http.Server{Addr: *listen, Handler: newRouter(st)}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	log.Printf("serving %d account(s) on http://%s (poll interval %s)", len(accounts), *listen, *interval)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
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
			log.Printf("skipping %s: no %s", filepath.Join(dir, e.Name()), anthropic.CredentialsFile)
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
