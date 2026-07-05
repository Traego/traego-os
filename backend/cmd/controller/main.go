// Command controller runs the Traego control-plane API.
//
// Config via env:
//
//	TRAEGO_ADDR        listen address (default :8443)
//	TRAEGO_ADMIN_KEY   bearer token for operator endpoints (required)
//	TRAEGO_JOIN_TOKEN  token nodes present to announce (required)
//	TRAEGO_HB_TIMEOUT  online->offline timeout, Go duration (default 30s)
//	TRAEGO_TLS         "on" serves the API+UI over TLS with a cert minted by the
//	                   controller's own CA (default off: plaintext HTTP, the
//	                   UniFi/Home Assistant-style LAN default — no browser cert
//	                   warning on first run). Node identity is unaffected: the
//	                   :8444 data plane is always mTLS and joins pin the CA via
//	                   TRAEGO_CA_FINGERPRINT either way.
//	TRAEGO_TLS_CERT    path to an operator-provided certificate chain (e.g. from
//	TRAEGO_TLS_KEY     Let's Encrypt via DNS-01) — providing both implies TLS
//	                   for the API+UI listener using that pair
//	TRAEGO_CORS_ORIGIN origin allowed to call the API cross-origin (default
//	                   none: the embedded UI is same-origin; set to
//	                   http://localhost:5173 for Vite dev)
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/traego/traego/internal/api"
	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/discovery"
	"github.com/traego/traego/internal/hub"
	"github.com/traego/traego/internal/hublink"
	"github.com/traego/traego/internal/inference"
	"github.com/traego/traego/internal/provision"
	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/ollama"
	"github.com/traego/traego/internal/store"
	"github.com/traego/traego/internal/web"
)

func main() {
	addr := env("TRAEGO_ADDR", ":8443")
	certFile, keyFile := os.Getenv("TRAEGO_TLS_CERT"), os.Getenv("TRAEGO_TLS_KEY")
	ownCert := certFile != "" && keyFile != ""
	// Plaintext by default: a first-run homelab UI shouldn't open with a cert
	// warning. An operator-provided cert pair implies TLS.
	tlsOn := env("TRAEGO_TLS", "off") == "on" || ownCert

	// `controller healthcheck` probes /healthz and exits 0/1. Used by the
	// container HEALTHCHECK since the distroless image has no shell or curl.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		scheme := "https"
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
			// liveness probe of our own loopback, not an authenticated channel
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}}
		if !tlsOn {
			scheme = "http"
		}
		resp, err := client.Get(scheme + "://127.0.0.1" + addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	hbTimeout, err := time.ParseDuration(env("TRAEGO_HB_TIMEOUT", "30s"))
	if err != nil {
		log.Fatalf("invalid TRAEGO_HB_TIMEOUT: %v", err)
	}

	// Sample this host's CPU/memory on an interval; the API serves the latest.
	collector := metrics.NewCollector()
	var sysCache atomic.Value
	sysCache.Store(collector.Sample())

	// Persistent store: state survives restart. TRAEGO_DB is the bbolt file.
	st, err := store.OpenBolt(env("TRAEGO_DB", "traego.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	// Local AI: the controller manages an Ollama backend at TRAEGO_OLLAMA_URL.
	// When that URL is this host, the controller owns the process too: install
	// starts `ollama serve`, uninstall/shutdown stops it. A remote URL is
	// treated as an externally managed backend.
	ollamaURL := env("TRAEGO_OLLAMA_URL", "http://localhost:11434")
	olClient := ollama.New(ollamaURL)
	infer := inference.NewManager(olClient)
	var olSup *ollama.Supervisor
	if ollama.Manageable(ollamaURL) {
		olSup = ollama.NewSupervisor(olClient, log.Printf)
		infer.WithBackend(olSup)
	}
	// The AI module's durable state (installed, reservation, enabled models)
	// lives in the store; reload it so an installed module — and its managed
	// backend — survive controller restarts.
	infer.OnChange(func(s inference.State) {
		b, err := json.Marshal(s)
		if err == nil {
			if err := st.PutMeta(metaAIState, b); err != nil {
				log.Printf("ai state: persist: %v", err)
			}
		}
	})
	if raw, err := st.GetMeta(metaAIState); err == nil {
		var s inference.State
		if json.Unmarshal(raw, &s) == nil {
			infer.LoadState(s)
			if s.Installed {
				log.Printf("ai module: restored (installed, %.0f GB reserved, %d model(s) enabled)", s.VRAMGB, len(s.Enabled))
			}
		}
	}

	// Internal CA: signs the controller's own server certs and every adopted
	// node's client cert. The root persists in the store so node certificates
	// (and the fingerprint operators pin) survive controller restarts.
	authority, err := loadOrCreateCA(st)
	if err != nil {
		log.Fatalf("ca: %v", err)
	}
	log.Printf("CA fingerprint (sha256): %s — pin this on joining nodes via TRAEGO_CA_FINGERPRINT", authority.Fingerprint())

	srv, err := api.New(api.Config{
		Store:            st,
		AdminKey:         must("TRAEGO_ADMIN_KEY"),
		JoinToken:        must("TRAEGO_JOIN_TOKEN"),
		HeartbeatTimeout: hbTimeout,
		CORSOrigin:       os.Getenv("TRAEGO_CORS_ORIGIN"),
		Logf:             log.Printf,
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
	// revive a managed AI backend that stops answering while installed
	go infer.Keep(ctx, 10*time.Second)

	// Outbound hub enrollment (docs/hosted-hub.md): push our site index up so
	// the org's hub can list this controller. Identity persists in the store.
	hostname, _ := os.Hostname()
	startHubLink := func(hubURL, orgToken, displayName string) {
		id := ""
		if raw, err := st.GetMeta(metaControllerID); err == nil {
			id = string(raw)
		}
		if displayName == "" {
			displayName = env("TRAEGO_CONTROLLER_NAME", hostname)
		}
		link := hublink.New(hublink.Config{
			HubURL:   hubURL,
			OrgToken: orgToken,
			ID:       id,
			Name:     displayName,
			Interval: 30 * time.Second,
			Logf:     log.Printf,
			OnID: func(id string) {
				if err := st.PutMeta(metaControllerID, []byte(id)); err != nil {
					log.Printf("hub: persist controller id: %v", err)
				}
			},
			Summary: func(context.Context) []hub.SiteSummary {
				nodes, err := st.List()
				if err != nil {
					return nil
				}
				sites, _ := st.ListSites()
				bySite := map[string]*hub.SiteSummary{}
				out := []hub.SiteSummary{}
				for _, site := range sites {
					s := &hub.SiteSummary{ID: site.ID, Name: site.Name}
					bySite[site.ID] = s
				}
				for _, n := range nodes {
					s := bySite[n.Site]
					if s == nil {
						continue
					}
					s.Machines++
					if n.State == store.StateOnline {
						s.Online++
					}
				}
				// the controller host itself counts in its home site
				if s := bySite[env("TRAEGO_SITE_ID", "local")]; s != nil {
					s.Machines++
					s.Online++
				}
				for _, site := range sites {
					out = append(out, *bySite[site.ID])
				}
				return out
			},
		})
		go link.Run(ctx)
	}

	// Three ways into an org (docs/hub-first-setup.md): env config (dev), a
	// persisted claim from a previous provisioning, or — while unclaimed — a
	// setup-pending beacon plus a one-shot claim-code-gated provision endpoint
	// a hub's wizard can call.
	var provHandler http.Handler
	if hubURL := os.Getenv("TRAEGO_HUB_URL"); hubURL != "" {
		startHubLink(hubURL, must("TRAEGO_HUB_TOKEN"), "")
	} else if raw, err := st.GetMeta(metaClaim); err == nil {
		var cl provision.Claim
		if err := json.Unmarshal(raw, &cl); err == nil {
			log.Printf("hub: claimed by %s (since %s)", cl.HubURL, cl.ClaimedAt.Format(time.RFC3339))
			startHubLink(cl.HubURL, cl.OrgToken, cl.Name)
		}
	} else {
		code := provision.NewClaimCode()
		log.Printf("SETUP: unclaimed controller — claim code: %s (a hub's setup wizard uses this to adopt it)", code)
		port := 8443
		if _, p, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
		beacon, berr := discovery.Announce(env("TRAEGO_CONTROLLER_NAME", hostname), port, map[string]string{"cores": strconv.Itoa(collector.Sample().Cores)})
		if berr != nil {
			log.Printf("setup beacon: %v (discovery disabled; provisioning by direct address still works)", berr)
		}
		provHandler = provision.New(provision.Config{
			ClaimCode: code,
			Claimed: func() bool {
				_, err := st.GetMeta(metaClaim)
				return err == nil
			},
			Apply: func(cl provision.Claim) error {
				b, err := json.Marshal(cl)
				if err != nil {
					return err
				}
				if err := st.PutMeta(metaClaim, b); err != nil {
					return err
				}
				beacon.Stop()
				startHubLink(cl.HubURL, cl.OrgToken, cl.Name)
				return nil
			},
			Logf: log.Printf,
		})
	}
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
	if provHandler != nil {
		// more specific than /api/, so it wins while the controller is unclaimed
		root.Handle("/api/v1/provision", provHandler)
	}

	hosts := strings.Split(env("TRAEGO_SECURE_HOSTS", "traego-controller,localhost,127.0.0.1"), ",")
	serverCert, err := authority.ServerCertificate(hosts)
	if err != nil {
		log.Fatalf("server cert: %v", err)
	}

	httpSrv := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 5 * time.Second}
	if tlsOn {
		uiCert := serverCert
		if ownCert {
			uiCert, err = tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				log.Fatalf("TRAEGO_TLS_CERT/KEY: %v", err)
			}
		}
		httpSrv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{uiCert}, MinVersion: tls.VersionTLS12}
	}
	go func() {
		if tlsOn {
			src := "self-signed by the controller CA — browsers will warn once"
			if ownCert {
				src = "operator-provided certificate"
			}
			log.Printf("controller API + UI listening on %s (TLS, %s)", addr, src)
			if err := httpSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("listen: %v", err)
			}
			return
		}
		log.Printf("controller API + UI listening on %s (plain HTTP — LAN default: admin key and join secrets transit unencrypted on this listener; the :8444 node data plane stays mTLS)", addr)
		log.Print("to serve the UI over TLS: TRAEGO_TLS=on (self-signed), or TRAEGO_TLS_CERT/TRAEGO_TLS_KEY for a real cert (e.g. Let's Encrypt DNS-01)")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// mTLS data plane: a separate listener that requires a CA-signed client
	// certificate. Nodes heartbeat here once they have their identity.
	secureAddr := env("TRAEGO_SECURE_ADDR", ":8444")
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
	if olSup != nil {
		olSup.Stop() // only kills an ollama the controller itself started
	}
}

// Store meta keys for the persisted CA root, AI module state, and hub identity.
const (
	metaCACert       = "ca.cert"
	metaCAKey        = "ca.key"
	metaAIState      = "ai.state"
	metaControllerID = "controller.id"
	metaClaim        = "claim.config"
)

// loadOrCreateCA returns the CA persisted in the store, or creates one and
// persists it on first boot. Node certs stay valid across controller restarts.
func loadOrCreateCA(st store.Store) (*ca.CA, error) {
	certPEM, errCert := st.GetMeta(metaCACert)
	keyPEM, errKey := st.GetMeta(metaCAKey)
	if errCert == nil && errKey == nil {
		return ca.Load(certPEM, keyPEM)
	}
	authority, err := ca.New()
	if err != nil {
		return nil, err
	}
	kp, err := authority.KeyPEM()
	if err != nil {
		return nil, err
	}
	if err := st.PutMeta(metaCACert, authority.CertPEM()); err != nil {
		return nil, err
	}
	if err := st.PutMeta(metaCAKey, kp); err != nil {
		return nil, err
	}
	return authority, nil
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
