package poll

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/razvandimescu/numa-metrics/internal/state"
)

// numa returns entries newest-first. Each poll the server returns the current
// window; the poller must ingest only entries newer than last poll's watermark.
func TestWatermarkDedup(t *testing.T) {
	var window []state.Entry // newest-first
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(window)
	}))
	defer srv.Close()

	st := state.New(100)
	p := New(srv.URL, 1000, time.Hour, st, nil)

	e := func(epoch float64, dom string) state.Entry {
		return state.Entry{TimestampEpoch: epoch, Src: "192.168.1.21:5", Domain: dom, QueryType: "A", Path: "FORWARD"}
	}

	window = []state.Entry{e(3, "c.com"), e(2, "b.com"), e(1, "a.com")}
	p.once(context.Background())
	if got := len(st.RowsAfter(0, 0)); got != 3 {
		t.Fatalf("first poll: want 3 ingested, got %d", got)
	}

	// Two new entries (5,4) prepended; 3,2,1 already seen and must be skipped.
	window = []state.Entry{e(5, "e.com"), e(4, "d.com"), e(3, "c.com"), e(2, "b.com"), e(1, "a.com")}
	p.once(context.Background())

	rows := st.RowsAfter(0, 0)
	if len(rows) != 5 {
		t.Fatalf("second poll: want 5 total, got %d (dedup failed)", len(rows))
	}
	// Ingest order is chronological: a,b,c,d,e.
	wantDomains := []string{"a.com", "b.com", "c.com", "d.com", "e.com"}
	for i, want := range wantDomains {
		if rows[i].Domain != want {
			t.Fatalf("row %d: want %s, got %s", i, want, rows[i].Domain)
		}
	}
}
