// Package enrich turns a client IP into a device name — from a static hosts file
// (router DHCP-lease names) or mDNS via avahi. Results are TTL-cached.
package enrich

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type cached struct {
	name string
	at   time.Time
}

type Enricher struct {
	ttl      time.Duration
	useAvahi bool
	hosts    map[string]string // static IP -> name overrides (LAN-independent)
	mu       sync.Mutex
	cache    map[string]cached
}

func New(ttl time.Duration, useAvahi bool, hosts map[string]string) *Enricher {
	if hosts == nil {
		hosts = map[string]string{}
	}
	return &Enricher{ttl: ttl, useAvahi: useAvahi, hosts: hosts, cache: make(map[string]cached)}
}

// LoadHosts parses an "IP name" file (# comments allowed). Lets the agent attach
// device names without on-LAN avahi — e.g. when it runs on the laptop pointing at
// a remote numa over the tunnel.
func LoadHosts(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			out[fields[0]] = fields[1]
		}
	}
	return out, sc.Err()
}

func (e *Enricher) Lookup(ip string) (name string) {
	e.mu.Lock()
	if c, ok := e.cache[ip]; ok && time.Since(c.at) < e.ttl {
		e.mu.Unlock()
		return c.name
	}
	e.mu.Unlock()

	if n, ok := e.hosts[ip]; ok {
		name = n
	} else if e.useAvahi {
		name = avahiResolve(ip)
	}

	e.mu.Lock()
	e.cache[ip] = cached{name: name, at: time.Now()}
	e.mu.Unlock()
	return name
}

func avahiResolve(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "avahi-resolve", "-a", ip).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out)) // "192.168.1.21\thostname.local"
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}
