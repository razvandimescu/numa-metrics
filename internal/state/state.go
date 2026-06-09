// Package state holds the in-RAM aggregate of numa query-log entries:
// per-client counters, last-seen, device info, and a bounded ring of raw rows
// for the laptop to drain. Nothing here touches disk.
package state

import (
	"net"
	"sync"
	"time"
)

// Entry mirrors numa's QueryLogResponse DTO (only the fields we consume).
type Entry struct {
	TimestampEpoch float64 `json:"timestamp_epoch"`
	Src            string  `json:"src"`
	Domain         string  `json:"domain"`
	QueryType      string  `json:"query_type"`
	Path           string  `json:"path"`
	Transport      string  `json:"transport"`
	Rescode        string  `json:"rescode"`
	LatencyMs      float64 `json:"latency_ms"`
	Dnssec         string  `json:"dnssec"`
}

// Row is an enriched Entry with a monotonic sequence number for cursor draining.
type Row struct {
	Seq uint64 `json:"seq"`
	Entry
	Name string `json:"name,omitempty"`
}

// Info is the per-client device metadata surfaced as a Prometheus _info metric.
type Info struct {
	Name string
}

type ckey struct {
	client string
	path   string
}

type State struct {
	mu       sync.Mutex
	counters map[ckey]uint64
	lastSeen map[string]float64
	info     map[string]Info

	buf     []Row
	cap     int
	head    int
	size    int
	nextSeq uint64

	// Watermark: fingerprint + epoch of the newest entry ingested last poll.
	lastFp    string
	lastEpoch float64

	pollOverflow uint64
	lastPollUnix int64

	global     GlobalStats
	haveGlobal bool
	globalUp   bool
}

func New(ringCap int) *State {
	if ringCap < 1 {
		ringCap = 1
	}
	return &State{
		counters: make(map[ckey]uint64),
		lastSeen: make(map[string]float64),
		info:     make(map[string]Info),
		buf:      make([]Row, ringCap),
		cap:      ringCap,
	}
}

// Watermark returns the newest fingerprint+epoch from the previous poll so the
// poller can stop walking the newest-first list once it reaches known data.
func (s *State) Watermark() (fp string, epoch float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastFp, s.lastEpoch
}

// Ingest folds a chronological (oldest-first) batch of new rows into the
// aggregates and the ring, assigning sequence numbers and advancing the
// watermark to newestFp/newestEpoch.
func (s *State) Ingest(rows []Row, newestFp string, newestEpoch float64, hitLimit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range rows {
		r := rows[i]
		s.nextSeq++ // 1-based: cursor after=0 returns everything
		r.Seq = s.nextSeq
		s.push(r)

		ip := hostOnly(r.Src)
		s.counters[ckey{ip, r.Path}]++
		if r.TimestampEpoch > s.lastSeen[ip] {
			s.lastSeen[ip] = r.TimestampEpoch
		}
		if r.Name != "" {
			s.info[ip] = Info{Name: r.Name}
		}
	}
	if len(rows) > 0 {
		s.lastFp = newestFp
		s.lastEpoch = newestEpoch
	}
	if hitLimit {
		s.pollOverflow++
	}
	s.lastPollUnix = time.Now().Unix()
}

// MarkPoll records a successful poll that produced no new rows.
func (s *State) MarkPoll() {
	s.mu.Lock()
	s.lastPollUnix = time.Now().Unix()
	s.mu.Unlock()
}

// RowsAfter returns ring rows with Seq > after, in ascending Seq order.
// max <= 0 means no cap.
func (s *State) RowsAfter(after uint64, max int) []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, 0)
	for i := 0; i < s.size; i++ {
		r := s.buf[(s.head+i)%s.cap]
		if r.Seq > after {
			out = append(out, r)
			if max > 0 && len(out) >= max {
				break
			}
		}
	}
	return out
}

func (s *State) push(r Row) {
	idx := (s.head + s.size) % s.cap
	if s.size < s.cap {
		s.buf[idx] = r
		s.size++
		return
	}
	s.buf[s.head] = r
	s.head = (s.head + 1) % s.cap
}

func hostOnly(src string) string {
	if h, _, err := net.SplitHostPort(src); err == nil {
		return h
	}
	return src
}
