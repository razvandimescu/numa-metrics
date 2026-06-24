package poll

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/razvandimescu/numa-metrics/internal/state"
)

// apiEntry mirrors what numa serves: entry fields plus the stamped seq.
type apiEntry struct {
	state.Entry
	Seq uint64 `json:"seq"`
}

func entry(seq uint64, dom string) apiEntry {
	return apiEntry{
		Entry: state.Entry{TimestampEpoch: float64(seq), Src: "192.168.1.21:5", Domain: dom, QueryType: "A", Path: "FORWARD"},
		Seq:   seq,
	}
}

// serveWindow returns a server reflecting whatever *window currently points at,
// plus the poller wired to it.
func serveWindow(window *[]apiEntry, st *state.State) (*httptest.Server, *Poller) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(*window)
	}))
	return srv, New(srv.URL, 1000, time.Hour, st, nil)
}

// numa returns entries newest-first; each poll the poller must ingest only
// those with seq above last poll's watermark.
func TestSeqDedup(t *testing.T) {
	var window []apiEntry
	st := state.New(100)
	srv, p := serveWindow(&window, st)
	defer srv.Close()

	window = []apiEntry{entry(3, "c.com"), entry(2, "b.com"), entry(1, "a.com")}
	p.once(context.Background())
	if got := len(st.RowsAfter(0, 0)); got != 3 {
		t.Fatalf("first poll: want 3 ingested, got %d", got)
	}

	// seqs 5,4 are new; 3,2,1 already seen and must be skipped.
	window = []apiEntry{entry(5, "e.com"), entry(4, "d.com"), entry(3, "c.com"), entry(2, "b.com"), entry(1, "a.com")}
	p.once(context.Background())

	rows := st.RowsAfter(0, 0)
	if len(rows) != 5 {
		t.Fatalf("second poll: want 5 total, got %d (dedup failed)", len(rows))
	}
	// Ingest order is chronological: a,b,c,d,e.
	for i, want := range []string{"a.com", "b.com", "c.com", "d.com", "e.com"} {
		if rows[i].Domain != want {
			t.Fatalf("row %d: want %s, got %s", i, want, rows[i].Domain)
		}
	}
}

// On a numa restart its seq resets, so the newest seq drops below our
// watermark; the poller must notice and re-ingest rather than stall.
func TestSeqRestartReingests(t *testing.T) {
	var window []apiEntry
	st := state.New(100)
	srv, p := serveWindow(&window, st)
	defer srv.Close()

	window = []apiEntry{entry(9, "b.com"), entry(8, "a.com")}
	p.once(context.Background())
	if got := len(st.RowsAfter(0, 0)); got != 2 {
		t.Fatalf("pre-restart: want 2, got %d", got)
	}

	// numa restarted: seq counter back to small values, brand-new entries.
	window = []apiEntry{entry(2, "d.com"), entry(1, "c.com")}
	p.once(context.Background())

	rows := st.RowsAfter(0, 0)
	if len(rows) != 4 {
		t.Fatalf("post-restart: want 4 (re-ingested), got %d (silent stall?)", len(rows))
	}
	if rows[2].Domain != "c.com" || rows[3].Domain != "d.com" {
		t.Fatalf("post-restart rows wrong: %s, %s", rows[2].Domain, rows[3].Domain)
	}
}

// A burst larger than numa's ring between polls evicts entries we never saw;
// the poller must detect the seq gap and bump the overflow counter.
func TestSeqGapDetected(t *testing.T) {
	var window []apiEntry
	st := state.New(100)
	srv, p := serveWindow(&window, st)
	defer srv.Close()

	window = []apiEntry{entry(2, "b.com"), entry(1, "a.com")}
	p.once(context.Background())

	// Next poll jumps to seqs 9,8 — seqs 3..7 rolled off (a gap).
	window = []apiEntry{entry(9, "g.com"), entry(8, "f.com")}
	p.once(context.Background())

	if got := len(st.RowsAfter(0, 0)); got != 4 {
		t.Fatalf("want 4 ingested across the gap, got %d", got)
	}
	var sb strings.Builder
	st.WriteMetrics(&sb)
	if !strings.Contains(sb.String(), "numa_metrics_poll_gap_total 1") {
		t.Fatalf("gap not counted:\n%s", sb.String())
	}
}
