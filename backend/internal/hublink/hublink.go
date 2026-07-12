// Package hublink is the controller's outbound connection to a hub: enroll
// once, then push periodic site/health summaries. Outbound-only, so it works
// behind NAT with zero inbound rules (docs/hosted-hub.md). The hub being down
// never affects local operation — reports just resume when it returns.
package hublink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/traego/traego/internal/hub"
)

// Config wires a Link.
type Config struct {
	HubURL   string // e.g. https://hub.traego.ai or http://localhost:9443
	OrgToken string
	ID       string // persisted controller identity ("" on first enroll)
	Name     string
	Version  string
	Interval time.Duration                            // report cadence (default 30s)
	Summary  func(ctx context.Context) []hub.SiteSummary // live site index
	OnID     func(id string)                          // persistence hook for a hub-assigned ID
	Logf     func(string, ...any)
	Client   *http.Client
}

// Link maintains the controller→hub relationship.
type Link struct {
	cfg Config
	id  string
}

// New builds a Link (call Run to start it).
func New(cfg Config) *Link {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Link{cfg: cfg, id: cfg.ID}
}

// Run enrolls and then reports on the configured cadence until ctx ends.
// Every failure is retried on the next tick — the hub is never load-bearing.
func (l *Link) Run(ctx context.Context) {
	t := time.NewTicker(l.cfg.Interval)
	defer t.Stop()
	l.tick(ctx) // immediately, not after the first interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.tick(ctx)
		}
	}
}

func (l *Link) tick(ctx context.Context) {
	if l.id == "" {
		if err := l.enroll(ctx); err != nil {
			l.cfg.Logf("hub: enroll: %v (will retry)", err)
			return
		}
	}
	if err := l.report(ctx); err != nil {
		l.cfg.Logf("hub: report: %v (will retry)", err)
		// A hub that lost its registry (or a new hub) won't know our ID:
		// re-enroll with it so identity is preserved, not reminted.
		if err := l.enroll(ctx); err == nil {
			_ = l.report(ctx)
		}
	}
}

func (l *Link) enroll(ctx context.Context) error {
	var resp struct {
		ID string `json:"id"`
	}
	err := l.post(ctx, "/api/v1/enroll", map[string]string{
		"id": l.id, "name": l.cfg.Name, "version": l.cfg.Version,
	}, &resp)
	if err != nil {
		return err
	}
	if resp.ID != l.id {
		l.id = resp.ID
		if l.cfg.OnID != nil {
			l.cfg.OnID(l.id)
		}
	}
	l.cfg.Logf("hub: enrolled with %s as %s", l.cfg.HubURL, l.id)
	return nil
}

func (l *Link) report(ctx context.Context) error {
	var sites []hub.SiteSummary
	if l.cfg.Summary != nil {
		sites = l.cfg.Summary(ctx)
	}
	return l.post(ctx, "/api/v1/controllers/"+l.id+"/report", map[string]any{"sites": sites}, nil)
}

func (l *Link) post(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.HubURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+l.cfg.OrgToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
