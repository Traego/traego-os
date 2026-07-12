// Command hub runs the Traego multi-controller registry: the hosted (or
// self-hosted) index controllers enroll into. docs/hosted-hub.md and
// docs/hub-first-setup.md are the design.
//
// Config via env:
//
//	TRAEGO_HUB_ADDR       listen address (default :9443)
//	TRAEGO_HUB_ORG_TOKEN  org token controllers enroll with (required)
//	TRAEGO_HUB_DB         bbolt registry file (default hub.db)
//	TRAEGO_HUB_SEEN       online->offline threshold, Go duration (default 90s)
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/traego/traego/internal/discovery"
	"github.com/traego/traego/internal/hub"
)

func main() {
	addr := env("TRAEGO_HUB_ADDR", ":9443")
	// Cloud Run (and friends) dictate the port via $PORT.
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	seen, err := time.ParseDuration(env("TRAEGO_HUB_SEEN", "90s"))
	if err != nil {
		log.Fatalf("invalid TRAEGO_HUB_SEEN: %v", err)
	}

	st, err := hub.OpenBolt(env("TRAEGO_HUB_DB", "hub.db"))
	if err != nil {
		log.Fatalf("hub store: %v", err)
	}
	defer st.Close()

	cfg := hub.Config{
		OrgToken:    must("TRAEGO_HUB_ORG_TOKEN"),
		Store:       st,
		SeenTimeout: seen,
		Logf:        log.Printf,
		PublicURL:   os.Getenv("TRAEGO_HUB_PUBLIC_URL"), // "" = derive from request Host
	}
	// mDNS discovery only exists on a LAN. A hosted hub (Cloud Run sets
	// K_SERVICE) has no multicast; its wizard provisions by direct address —
	// or, later, through an on-LAN agent doing the browsing.
	if os.Getenv("K_SERVICE") == "" && env("TRAEGO_HUB_DISCOVERY", "on") != "off" {
		cfg.Discover = func(ctx context.Context) ([]hub.DiscoveredController, error) {
			found, err := discovery.Browse(ctx, 2*time.Second)
			if err != nil {
				return nil, err
			}
			out := make([]hub.DiscoveredController, 0, len(found))
			for _, f := range found {
				out = append(out, hub.DiscoveredController{Name: f.Name, Addr: f.Addr, TXT: f.TXT})
			}
			return out, nil
		}
	}
	srv := hub.New(cfg)

	// One binary serves the API and the traego.ai landing pages: /api/* and
	// /healthz go to the API, everything else to the embedded site.
	apiHandler := srv.Handler()
	root := http.NewServeMux()
	root.Handle("/api/", apiHandler)
	root.Handle("/healthz", apiHandler)
	root.Handle("/", hub.WebHandler())

	httpSrv := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("hub listening on %s (plain HTTP — front with TLS in production)", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Print("hub shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("%s is required", k)
	}
	return v
}
