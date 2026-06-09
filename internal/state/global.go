package state

// GlobalStats mirrors numa's GET /stats response (the subset we export).
type GlobalStats struct {
	Version    string  `json:"version"`
	UptimeSecs float64 `json:"uptime_secs"`
	Queries    struct {
		Total      uint64 `json:"total"`
		Forwarded  uint64 `json:"forwarded"`
		Upstream   uint64 `json:"upstream"`
		Recursive  uint64 `json:"recursive"`
		Coalesced  uint64 `json:"coalesced"`
		Cached     uint64 `json:"cached"`
		Local      uint64 `json:"local"`
		Overridden uint64 `json:"overridden"`
		Blocked    uint64 `json:"blocked"`
		Errors     uint64 `json:"errors"`
	} `json:"queries"`
	Transport struct {
		Udp uint64 `json:"udp"`
		Tcp uint64 `json:"tcp"`
		Dot uint64 `json:"dot"`
		Doh uint64 `json:"doh"`
	} `json:"transport"`
	UpstreamTransport struct {
		Udp  uint64 `json:"udp"`
		Tcp  uint64 `json:"tcp"`
		Dot  uint64 `json:"dot"`
		Doh  uint64 `json:"doh"`
		Odoh uint64 `json:"odoh"`
	} `json:"upstream_transport"`
	Cache struct {
		Entries    uint64 `json:"entries"`
		MaxEntries uint64 `json:"max_entries"`
	} `json:"cache"`
	Overrides struct {
		Active uint64 `json:"active"`
	} `json:"overrides"`
	Blocking struct {
		Enabled       bool   `json:"enabled"`
		Paused        bool   `json:"paused"`
		DomainsLoaded uint64 `json:"domains_loaded"`
		AllowlistSize uint64 `json:"allowlist_size"`
	} `json:"blocking"`
	Lan struct {
		Enabled bool   `json:"enabled"`
		Peers   uint64 `json:"peers"`
	} `json:"lan"`
	Memory struct {
		CacheBytes          uint64 `json:"cache_bytes"`
		BlocklistBytes      uint64 `json:"blocklist_bytes"`
		QueryLogBytes       uint64 `json:"query_log_bytes"`
		SrttBytes           uint64 `json:"srtt_bytes"`
		OverridesBytes      uint64 `json:"overrides_bytes"`
		TotalEstimatedBytes uint64 `json:"total_estimated_bytes"`
		ProcessMemoryBytes  uint64 `json:"process_memory_bytes"`
	} `json:"memory"`
}

// SetGlobal records a fresh /stats snapshot and marks numa reachable.
func (s *State) SetGlobal(g GlobalStats) {
	s.mu.Lock()
	s.global = g
	s.haveGlobal = true
	s.globalUp = true
	s.mu.Unlock()
}

// MarkGlobalDown records that the last /stats fetch failed (numa unreachable).
func (s *State) MarkGlobalDown() {
	s.mu.Lock()
	s.globalUp = false
	s.mu.Unlock()
}
