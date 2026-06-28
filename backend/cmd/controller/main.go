// Command controller runs the Traego control-plane API.
//
// Config via env:
//
//	TRAEGO_ADDR        listen address (default :8443)
//	TRAEGO_ADMIN_KEY   bearer token for operator endpoints (required)
//	TRAEGO_JOIN_TOKEN  token nodes present to announce (required)
//	TRAEGO_HB_TIMEOUT  online->offline timeout, Go duration (default 30s)
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/traego/traego/internal/api"
	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/inference"
	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/ollama"
	"github.com/traego/traego/internal/store"
	"github.com/traego/traego/internal/web"
)

func main() {
	addr := env("TRAEGO_ADDR", ":8443")

	// `controller healthcheck` probes /healthz and exits 0/1. Used by the
	// container HEALTHCHECK since the distroless image has no shell or curl.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		resp, err := http.Get("http://127.0.0.1" + addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	hbTimeout, err := time.ParseDuration(env("TRAEGO_HB_TIMEOUT", "30s"))
	if err != nil {
		log.Fatalf("invalid TRAEGO_HB_TIMEOUT: %v", err)
	}

	// Internal CA backs the mTLS data plane: it signs the controller's own
	// server cert and every adopted node's client cert.
	authority, err := ca.New()
	if err != nil {
		log.Fatalf("ca: %v", err)
	}
	// Sample this host's CPU/memory on an interval; the API serves the latest.
	collector := metrics.NewCollector()
	var sysCache atomic.Value
	sysCache.Store(collector.Sample())

	// Local AI: the controller manages an Ollama backend (a GPU container on
	// Linux, or bare-metal Ollama on macOS) at TRAEGO_OLLAMA_URL.
	infer := inference.NewManager(ollama.New(env("TRAEGO_OLLAMA_URL", "http://localhost:11434")))

	// Persistent store: state survives restart. TRAEGO_DB is the bbolt file.
	st, err := store.OpenBolt(env("TRAEGO_DB", "traego.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	srv, err := api.New(api.Config{
		Store:            st,
		AdminKey:         must("TRAEGO_ADMIN_KEY"),
		JoinToken:        must("TRAEGO_JOIN_TOKEN"),
		HeartbeatTimeout: hbTimeout,
		CA:               authority,
		Inference:        infer,
		HomeSite:         store.Site{ID: env("TRAEGO_SITE_ID", "local"), Name: env("TRAEGO_SITE_NAME", "Local site")},
		System: func() metrics.Sample {
			if v := sysCache.Load(); v != nil {
				return v.(metrics.Sample)
			}
			return metrics.Sample{}
		},
	})
	if err != nil {
		log.Fatalf("controller: %v", err)
	}

	// reap nodes that stop heartbeating
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(hbTimeout / 2)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := srv.ReapOffline(); n > 0 {
					log.Printf("reaped %d offline node(s)", n)
				}
			}
		}
	}()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sysCache.Store(collector.Sample())
				srv.RecordActivity()
			}
		}
	}()

	// One binary serves the API and the built-in dashboard: /api/* and
	// /healthz go to the API handler; everything else serves the embedded UI.
	apiHandler := srv.Handler()
	root := http.NewServeMux()
	root.Handle("/api/", apiHandler)
	root.Handle("/healthz", apiHandler)
	root.Handle("/", web.SPAHandler(web.DistFS()))

	httpSrv := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("controller API + UI listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// mTLS data plane: a separate listener that requires a CA-signed client
	// certificate. Nodes heartbeat here once they have their identity.
	secureAddr := env("TRAEGO_SECURE_ADDR", ":8444")
	hosts := strings.Split(env("TRAEGO_SECURE_HOSTS", "traego-controller,localhost,127.0.0.1"), ",")
	serverCert, err := authority.ServerCertificate(hosts)
	if err != nil {
		log.Fatalf("server cert: %v", err)
	}
	secureSrv := &http.Server{
		Addr:              secureAddr,
		Handler:           srv.SecureHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientCAs:    authority.Pool(),
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
	}
	go func() {
		log.Printf("controller mTLS data plane listening on %s", secureAddr)
		if err := secureSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("mTLS listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = secureSrv.Shutdown(shutCtx)
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
