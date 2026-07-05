// Package discovery is the LAN rendezvous for hub-first setup
// (docs/hub-first-setup.md): an unclaimed controller beacons itself over
// mDNS/DNS-SD, and a hub browses for beacons to offer them in its wizard.
// Discovery is a convenience — provisioning also works by direct address.
package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// Service is the DNS-SD service type for controllers awaiting setup.
const Service = "_traego._tcp"

// Beacon announces this controller as unclaimed until Stop.
type Beacon struct{ srv *zeroconf.Server }

// Announce starts the setup-pending beacon. name is the display name (usually
// the hostname), port the controller's API port, specs free-form TXT extras.
func Announce(name string, port int, txt map[string]string) (*Beacon, error) {
	records := []string{"state=unclaimed"}
	for k, v := range txt {
		records = append(records, k+"="+v)
	}
	name = CleanName(name)
	host, _ := os.Hostname()
	srv, err := zeroconf.Register(name, Service, "local.", port, records, nil)
	if err != nil {
		return nil, fmt.Errorf("mdns register (%s on %s): %w", name, host, err)
	}
	return &Beacon{srv: srv}, nil
}

// CleanName turns a machine hostname into a valid DNS-SD instance label:
// macOS hostnames carry a ".local" suffix (and sometimes a trailing dot) that
// must not appear inside the instance name — the service domain provides it.
func CleanName(host string) string {
	host = strings.TrimSuffix(host, ".")
	host = strings.TrimSuffix(host, ".local")
	if host == "" {
		host = "traego-controller"
	}
	return host
}

// Stop withdraws the beacon (call once claimed).
func (b *Beacon) Stop() {
	if b != nil && b.srv != nil {
		b.srv.Shutdown()
	}
}

// Found is one discovered unclaimed controller.
type Found struct {
	Name string            `json:"name"`
	Addr string            `json:"addr"` // host:port, dialable from the hub
	TXT  map[string]string `json:"txt"`
}

// Browse scans the LAN for unclaimed controllers for the given window.
func Browse(ctx context.Context, window time.Duration) ([]Found, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, err
	}
	entries := make(chan *zeroconf.ServiceEntry, 16)
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	if err := resolver.Browse(ctx, Service, "local.", entries); err != nil {
		return nil, err
	}
	var out []Found
	for e := range entries {
		f := Found{Name: e.Instance, TXT: map[string]string{}}
		for _, t := range e.Text {
			if k, v, ok := cut(t); ok {
				f.TXT[k] = v
			}
		}
		if f.TXT["state"] != "unclaimed" {
			continue
		}
		var ip net.IP
		if len(e.AddrIPv4) > 0 {
			ip = e.AddrIPv4[0]
		} else if len(e.AddrIPv6) > 0 {
			ip = e.AddrIPv6[0]
		}
		if ip == nil {
			continue
		}
		f.Addr = net.JoinHostPort(ip.String(), fmt.Sprint(e.Port))
		out = append(out, f)
	}
	return out, nil
}

func cut(s string) (k, v string, ok bool) {
	for i := range s {
		if s[i] == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}
