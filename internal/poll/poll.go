// Package poll fetches numa's newest query-log entries on an interval and folds
// the unseen ones into state. numa returns newest-first and stamps each entry
// with a monotonic seq, so we keep entries with seq above our watermark.
package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/razvandimescu/numa-metrics/internal/enrich"
	"github.com/razvandimescu/numa-metrics/internal/state"
)

// logEntry is a query-log row as numa returns it: the entry fields plus the
// monotonic seq numa stamps at insert (used only here, for dedup).
type logEntry struct {
	state.Entry
	Seq uint64 `json:"seq"`
}

type Poller struct {
	baseURL  string
	limit    int
	interval time.Duration
	state    *state.State
	enricher *enrich.Enricher // nil disables enrichment
	client   *http.Client
}

func New(baseURL string, limit int, interval time.Duration, st *state.State, en *enrich.Enricher) *Poller {
	return &Poller{
		baseURL:  baseURL,
		limit:    limit,
		interval: interval,
		state:    st,
		enricher: en,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	p.once(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.once(ctx)
		}
	}
}

func (p *Poller) once(ctx context.Context) {
	entries, err := p.fetch(ctx) // newest-first, each carrying numa's seq
	if err != nil {
		log.Printf("poll: %v", err)
		return
	}
	if len(entries) == 0 {
		p.state.MarkPoll()
		return
	}

	lastSeq := p.state.LastSeq()
	newest := entries[0].Seq

	// numa restart: its seq counter resets to 0, so the newest seq drops below
	// our watermark. Re-ingest the whole window from scratch — numa's own
	// counters reset too, so this is the correct fresh start.
	if newest < lastSeq {
		log.Printf("poll: numa seq went backwards (%d < %d) — numa restarted, re-ingesting", newest, lastSeq)
		lastSeq = 0
	}

	// Entries are newest-first and contiguous, so the fresh ones are the prefix
	// with seq > lastSeq; count them, then walk that prefix oldest-first.
	n := 0
	for n < len(entries) && entries[n].Seq > lastSeq {
		n++
	}
	if n == 0 {
		p.state.MarkPoll()
		return
	}

	// Gap: the oldest fresh entry is more than one seq past our watermark, so
	// entries in between rolled off numa's ring before we fetched them.
	oldestFresh := entries[n-1].Seq
	gap := lastSeq > 0 && oldestFresh > lastSeq+1
	if gap {
		log.Printf("poll: gap — missed numa seqs %d..%d (raise -limit or lower -interval)", lastSeq+1, oldestFresh-1)
	}

	rows := make([]state.Row, 0, n) // chronological
	for i := n - 1; i >= 0; i-- {
		e := entries[i]
		r := state.Row{Entry: e.Entry}
		if p.enricher != nil {
			if ip := state.HostIP(e.Src); ip != "" {
				r.Name = p.enricher.Lookup(ip)
			}
		}
		rows = append(rows, r)
	}
	p.state.Ingest(rows, newest, gap)
}

func (p *Poller) fetch(ctx context.Context) ([]logEntry, error) {
	return getJSON[[]logEntry](ctx, p.client, fmt.Sprintf("%s/query-log?limit=%d", p.baseURL, p.limit))
}

// getJSON does a context-scoped GET and decodes a single JSON value from the body.
func getJSON[T any](ctx context.Context, client *http.Client, url string) (T, error) {
	var out T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return out, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}
