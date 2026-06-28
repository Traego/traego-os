// Command traegod is the Traego node agent. It announces this machine to the
// controller, waits to be adopted, then heartbeats until stopped.
//
// Config via env:
//
//	TRAEGO_CONTROLLER   controller base URL, e.g. http://controller:8443 (required)
//	TRAEGO_JOIN_TOKEN   join token (required)
//	TRAEGO_NODE_NAME    display name (default: hostname)
//	TRAEGO_NODE_CLASS   persistent | ephemeral (default persistent)
//	TRAEGO_HB_INTERVAL  heartbeat interval, Go duration (default 10s)
//	TRAEGO_MEMORY_GB    declared RAM (optional)
//	TRAEGO_GPU_VRAM_GB  declared GPU VRAM (optional)
package main

import (
	"context"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/traego/traego/internal/agent"
	"github.com/traego/traego/internal/store"
)

func main() {
	name := os.Getenv("TRAEGO_NODE_NAME")
	if name == "" {
		name, _ = os.Hostname()
	}
	hb, err := time.ParseDuration(env("TRAEGO_HB_INTERVAL", "10s"))
	if err != nil {
		log.Fatalf("invalid TRAEGO_HB_INTERVAL: %v", err)
	}

	controller := must("TRAEGO_CONTROLLER")
	// mTLS is the default: a node gets a CA-signed cert after adoption and
	// heartbeats over mutual TLS. Set TRAEGO_SECURE=false only to opt out.
	secure := os.Getenv("TRAEGO_SECURE") != "false"
	secureURL := env("TRAEGO_SECURE_URL", defaultSecureURL(controller))

	a := agent.New(agent.Config{
		ControllerURL:     controller,
		JoinToken:         must("TRAEGO_JOIN_TOKEN"),
		Name:              name,
		Site:              os.Getenv("TRAEGO_SITE"), // empty -> controller's home site
		Class:             store.Class(env("TRAEGO_NODE_CLASS", string(store.ClassPersistent))),
		Specs:             agent.DetectSpecs(),
		HeartbeatInterval: hb,
		Secure:            secure,
		SecureURL:         secureURL,
		Logf:              log.Printf,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("traegod: %v", err)
	}
}

// defaultSecureURL derives the mTLS data-plane URL from the controller URL:
// http://host:8443 -> https://host:8444.
func defaultSecureURL(controller string) string {
	u, err := url.Parse(controller)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return "https://" + u.Hostname() + ":8444"
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
