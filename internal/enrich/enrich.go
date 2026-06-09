// Package enrich turns a client IP into a device name (mDNS via avahi) and a
// vendor (MAC OUI from the ARP table). Both sources are local-LAN only, so this
// must run on the Pi, not the laptop. Results are TTL-cached.
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
	name   string
	vendor string
	at     time.Time
}

type Enricher struct {
	ttl      time.Duration
	useAvahi bool
	mu       sync.Mutex
	cache    map[string]cached
}

func New(ttl time.Duration, useAvahi bool) *Enricher {
	return &Enricher{ttl: ttl, useAvahi: useAvahi, cache: make(map[string]cached)}
}

func (e *Enricher) Lookup(ip string) (name, vendor string) {
	e.mu.Lock()
	if c, ok := e.cache[ip]; ok && time.Since(c.at) < e.ttl {
		e.mu.Unlock()
		return c.name, c.vendor
	}
	e.mu.Unlock()

	if e.useAvahi {
		name = avahiResolve(ip)
	}
	vendor = vendorForMAC(macForIP(ip))

	e.mu.Lock()
	e.cache[ip] = cached{name: name, vendor: vendor, at: time.Now()}
	e.mu.Unlock()
	return name, vendor
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

func macForIP(ip string) string {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 4 && fields[0] == ip {
			return strings.ToLower(fields[3])
		}
	}
	return ""
}

// ouiVendors is a small curated map; extend as needed. Unknown OUIs fall back to
// the raw prefix so a device is still distinguishable.
var ouiVendors = map[string]string{
	"b8:27:eb": "Raspberry Pi",
	"dc:a6:32": "Raspberry Pi",
	"e4:5f:01": "Raspberry Pi",
	"28:cd:c1": "Raspberry Pi",
	"d8:3a:dd": "Raspberry Pi",
	"3c:22:fb": "Apple",
	"a4:83:e7": "Apple",
	"f0:18:98": "Apple",
	"ac:de:48": "Apple",
	"f4:d4:88": "Apple",
	"fc:fb:fb": "Cisco",
	"00:1a:11": "Google",
	"54:60:09": "Google",
	"d4:3d:7e": "Micro-Star (MSI)",
	"1c:69:7a": "ASUSTek",
	"50:eb:f6": "ASUSTek",
	"30:9c:23": "Micro-Star (MSI)",
	"7c:10:c9": "ASUSTek",
	"e8:9f:80": "Belkin",
	"24:4b:fe": "ASUSTek",
	"fc:34:97": "Espressif",
	"a0:20:a6": "Espressif",
	"24:6f:28": "Espressif",
}

func vendorForMAC(mac string) string {
	if len(mac) < 8 {
		return ""
	}
	oui := mac[:8] // "dc:a6:32"
	if v, ok := ouiVendors[oui]; ok {
		return v
	}
	return oui
}
