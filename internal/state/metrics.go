package state

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type counter struct {
	client string
	path   string
	value  uint64
}

// WriteMetrics renders the aggregate as Prometheus text exposition. Labels are
// kept low-cardinality on purpose (client + path); domains never become labels.
func (s *State) WriteMetrics(w io.Writer) {
	s.mu.Lock()
	counters := make([]counter, 0, len(s.counters))
	for k, v := range s.counters {
		counters = append(counters, counter{k.client, k.path, v})
	}
	lastSeen := make(map[string]float64, len(s.lastSeen))
	for k, v := range s.lastSeen {
		lastSeen[k] = v
	}
	info := make(map[string]Info, len(s.info))
	for k, v := range s.info {
		info[k] = v
	}
	overflow := s.pollOverflow
	ringSize := s.size
	lastPoll := s.lastPollUnix
	global := s.global
	haveGlobal := s.haveGlobal
	globalUp := s.globalUp
	s.mu.Unlock()

	if haveGlobal {
		writeGlobal(w, global, globalUp)
	}

	sort.Slice(counters, func(i, j int) bool {
		if counters[i].client != counters[j].client {
			return counters[i].client < counters[j].client
		}
		return counters[i].path < counters[j].path
	})

	fmt.Fprintln(w, "# HELP numa_client_queries_total DNS queries per client and resolution path.")
	fmt.Fprintln(w, "# TYPE numa_client_queries_total counter")
	for _, c := range counters {
		fmt.Fprintf(w, "numa_client_queries_total{client=%q,path=%q} %d\n",
			esc(c.client), esc(c.path), c.value)
	}

	fmt.Fprintln(w, "# HELP numa_client_last_seen_timestamp_seconds Unix time of the client's most recent query.")
	fmt.Fprintln(w, "# TYPE numa_client_last_seen_timestamp_seconds gauge")
	for _, ip := range sortedKeys(lastSeen) {
		fmt.Fprintf(w, "numa_client_last_seen_timestamp_seconds{client=%q} %g\n", esc(ip), lastSeen[ip])
	}

	fmt.Fprintln(w, "# HELP numa_client_info Per-client device name (join key for dashboards).")
	fmt.Fprintln(w, "# TYPE numa_client_info gauge")
	for _, ip := range sortedInfoKeys(info) {
		fmt.Fprintf(w, "numa_client_info{client=%q,name=%q} 1\n", esc(ip), esc(info[ip].Name))
	}

	fmt.Fprintln(w, "# HELP numa_metrics_poll_overflow_total Polls that detected a seq gap (entries rolled off numa's ring before we fetched them).")
	fmt.Fprintln(w, "# TYPE numa_metrics_poll_overflow_total counter")
	fmt.Fprintf(w, "numa_metrics_poll_overflow_total %d\n", overflow)

	fmt.Fprintln(w, "# HELP numa_metrics_ring_rows Current rows buffered in the drain ring.")
	fmt.Fprintln(w, "# TYPE numa_metrics_ring_rows gauge")
	fmt.Fprintf(w, "numa_metrics_ring_rows %d\n", ringSize)

	fmt.Fprintln(w, "# HELP numa_metrics_last_poll_timestamp_seconds Unix time of the last successful poll.")
	fmt.Fprintln(w, "# TYPE numa_metrics_last_poll_timestamp_seconds gauge")
	fmt.Fprintf(w, "numa_metrics_last_poll_timestamp_seconds %d\n", lastPoll)
}

// writeGlobal renders the numa-wide gauges/counters from a /stats snapshot.
func writeGlobal(w io.Writer, g GlobalStats, up bool) {
	fmt.Fprintln(w, "# HELP numa_up Whether the last /stats scrape succeeded.")
	fmt.Fprintln(w, "# TYPE numa_up gauge")
	fmt.Fprintf(w, "numa_up %d\n", b2i(up))

	fmt.Fprintln(w, "# HELP numa_build_info numa version, as a label.")
	fmt.Fprintln(w, "# TYPE numa_build_info gauge")
	fmt.Fprintf(w, "numa_build_info{version=%q} 1\n", esc(g.Version))

	fmt.Fprintln(w, "# HELP numa_uptime_seconds Resolver uptime.")
	fmt.Fprintln(w, "# TYPE numa_uptime_seconds gauge")
	fmt.Fprintf(w, "numa_uptime_seconds %g\n", g.UptimeSecs)

	fmt.Fprintln(w, "# HELP numa_queries_by_path_total Resolver-wide queries by resolution path.")
	fmt.Fprintln(w, "# TYPE numa_queries_by_path_total counter")
	for _, kv := range []struct {
		path string
		v    uint64
	}{
		{"cached", g.Queries.Cached}, {"upstream", g.Queries.Upstream},
		{"forwarded", g.Queries.Forwarded}, {"recursive", g.Queries.Recursive},
		{"coalesced", g.Queries.Coalesced}, {"local", g.Queries.Local},
		{"overridden", g.Queries.Overridden}, {"blocked", g.Queries.Blocked},
		{"errors", g.Queries.Errors},
	} {
		fmt.Fprintf(w, "numa_queries_by_path_total{path=%q} %d\n", kv.path, kv.v)
	}

	fmt.Fprintln(w, "# HELP numa_transport_queries_total Client-facing queries by transport.")
	fmt.Fprintln(w, "# TYPE numa_transport_queries_total counter")
	for _, kv := range []struct {
		t string
		v uint64
	}{{"udp", g.Transport.Udp}, {"tcp", g.Transport.Tcp}, {"dot", g.Transport.Dot}, {"doh", g.Transport.Doh}} {
		fmt.Fprintf(w, "numa_transport_queries_total{transport=%q} %d\n", kv.t, kv.v)
	}

	fmt.Fprintln(w, "# HELP numa_cache_entries Cache entries currently held.")
	fmt.Fprintln(w, "# TYPE numa_cache_entries gauge")
	fmt.Fprintf(w, "numa_cache_entries %d\n", g.Cache.Entries)
	fmt.Fprintln(w, "# HELP numa_cache_max_entries Configured cache capacity.")
	fmt.Fprintln(w, "# TYPE numa_cache_max_entries gauge")
	fmt.Fprintf(w, "numa_cache_max_entries %d\n", g.Cache.MaxEntries)

	fmt.Fprintln(w, "# HELP numa_blocking_enabled Whether blocking is active.")
	fmt.Fprintln(w, "# TYPE numa_blocking_enabled gauge")
	fmt.Fprintf(w, "numa_blocking_enabled %d\n", b2i(g.Blocking.Enabled && !g.Blocking.Paused))
	fmt.Fprintln(w, "# HELP numa_blocking_domains_loaded Blocklist domain count.")
	fmt.Fprintln(w, "# TYPE numa_blocking_domains_loaded gauge")
	fmt.Fprintf(w, "numa_blocking_domains_loaded %d\n", g.Blocking.DomainsLoaded)

	fmt.Fprintln(w, "# HELP numa_overrides_active Active ephemeral overrides.")
	fmt.Fprintln(w, "# TYPE numa_overrides_active gauge")
	fmt.Fprintf(w, "numa_overrides_active %d\n", g.Overrides.Active)

	fmt.Fprintln(w, "# HELP numa_lan_peers Discovered LAN peers.")
	fmt.Fprintln(w, "# TYPE numa_lan_peers gauge")
	fmt.Fprintf(w, "numa_lan_peers %d\n", g.Lan.Peers)

	fmt.Fprintln(w, "# HELP numa_memory_bytes numa memory usage by area.")
	fmt.Fprintln(w, "# TYPE numa_memory_bytes gauge")
	for _, kv := range []struct {
		kind string
		v    uint64
	}{
		{"cache", g.Memory.CacheBytes}, {"blocklist", g.Memory.BlocklistBytes},
		{"query_log", g.Memory.QueryLogBytes}, {"srtt", g.Memory.SrttBytes},
		{"overrides", g.Memory.OverridesBytes}, {"total_estimated", g.Memory.TotalEstimatedBytes},
		{"process", g.Memory.ProcessMemoryBytes},
	} {
		fmt.Fprintf(w, "numa_memory_bytes{kind=%q} %d\n", kv.kind, kv.v)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedInfoKeys(m map[string]Info) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
