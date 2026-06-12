package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-gin-claude-status/internal/store"
)

// NewRemoteProvider returns a SnapshotProvider backed by an already-running
// serve daemon. It polls GET <baseURL>/api/usage every `every` and never
// touches credentials — use this mode when a web daemon already owns the OAuth
// grants. The first fetch is synchronous so connection problems surface
// immediately; later failures keep the last good snapshots.
func NewRemoteProvider(ctx context.Context, baseURL string, every time.Duration) (SnapshotProvider, error) {
	url := strings.TrimRight(baseURL, "/") + "/api/usage"
	client := &http.Client{Timeout: 10 * time.Second}

	h := &holder{}
	snaps, err := fetch(ctx, client, url)
	if err != nil {
		return nil, fmt.Errorf("remote %s: %w", url, err)
	}
	h.set(snaps)

	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if snaps, err := fetch(ctx, client, url); err == nil {
					h.set(snaps)
				}
			}
		}
	}()

	return h.get, nil
}

type holder struct {
	mu    sync.RWMutex
	snaps []store.Snapshot
}

func (h *holder) get() []store.Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.snaps
}

func (h *holder) set(s []store.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snaps = s
}

func fetch(ctx context.Context, client *http.Client, url string) ([]store.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	var snaps []store.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snaps); err != nil {
		return nil, err
	}
	return snaps, nil
}
