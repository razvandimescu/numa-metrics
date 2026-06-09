package state

import (
	"encoding/json"
	"strings"
	"testing"
)

// Real /stats payload from numa 0.20.0 (issue #285). Guards our json tags.
const sampleStats = `{
  "version": "0.20.0", "uptime_secs": 6608, "mode": "forward", "dnssec": false,
  "queries": {"total":5740,"forwarded":0,"upstream":582,"recursive":0,"coalesced":258,"cached":4280,"local":7,"overridden":0,"blocked":613,"errors":0},
  "transport": {"udp":5740,"tcp":0,"dot":0,"doh":0},
  "upstream_transport": {"udp":600,"tcp":0,"doh":0,"dot":0,"odoh":0},
  "cache": {"entries":767,"max_entries":100000},
  "overrides": {"active":0},
  "blocking": {"enabled":true,"paused":false,"domains_loaded":491477,"allowlist_size":0},
  "lan": {"enabled":false,"peers":0},
  "memory": {"cache_bytes":466938,"blocklist_bytes":44044012,"query_log_bytes":103973,"query_log_entries":1000,"srtt_bytes":153,"srtt_entries":3,"overrides_bytes":0,"total_estimated_bytes":44615076,"process_memory_bytes":86355968}
}`

func TestGlobalStatsDecodeAndRender(t *testing.T) {
	var g GlobalStats
	if err := json.Unmarshal([]byte(sampleStats), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g.Queries.Cached != 4280 || g.Blocking.DomainsLoaded != 491477 || g.Cache.MaxEntries != 100000 {
		t.Fatalf("json tags mismatch: cached=%d domains=%d max=%d",
			g.Queries.Cached, g.Blocking.DomainsLoaded, g.Cache.MaxEntries)
	}

	s := New(10)
	s.SetGlobal(g)
	var sb strings.Builder
	s.WriteMetrics(&sb)
	out := sb.String()

	for _, want := range []string{
		"numa_up 1",
		`numa_build_info{version="0.20.0"} 1`,
		`numa_queries_by_path_total{path="cached"} 4280`,
		`numa_queries_by_path_total{path="blocked"} 613`,
		`numa_blocking_domains_loaded 491477`,
		`numa_memory_bytes{kind="process"} 86355968`,
		"numa_cache_entries 767",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
