// Package poll fetches numa's newest query-log entries on an interval and folds
// the unseen ones into state. numa has no `since` filter, so we over-fetch the
// newest `limit` and dedup against a watermark.
package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/razvandimescu/numa-metrics/internal/enrich"
	"github.com/razvandimescu/numa-metrics/internal/state"
)

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
	entries, err := p.fetch(ctx)
	if err != nil {
		log.Printf("poll: %v", err)
		return
	}

	lastFp, lastEpoch := p.state.Watermark()
	fresh := make([]state.Entry, 0, len(entries)) // newest-first
	for _, e := range entries {
		if fingerprint(e) == lastFp || e.TimestampEpoch < lastEpoch {
			break
		}
		fresh = append(fresh, e)
	}
	if len(fresh) == 0 {
		p.state.MarkPoll()
		return
	}

	rows := make([]state.Row, 0, len(fresh)) // chronological
	for i := len(fresh) - 1; i >= 0; i-- {
		e := fresh[i]
		r := state.Row{Entry: e}
		if p.enricher != nil {
			if ip := hostOnly(e.Src); ip != "" {
				r.Name = p.enricher.Lookup(ip)
			}
		}
		rows = append(rows, r)
	}

	newest := fresh[0]
	hitLimit := lastFp != "" && len(entries) == p.limit && len(fresh) == len(entries)
	if hitLimit {
		log.Printf("poll: page full (%d) and all new — raise -limit or lower -interval to avoid gaps", p.limit)
	}
	p.state.Ingest(rows, fingerprint(newest), newest.TimestampEpoch, hitLimit)
}

func (p *Poller) fetch(ctx context.Context) ([]state.Entry, error) {
	u := fmt.Sprintf("%s/query-log?limit=%d", p.baseURL, p.limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out []state.Entry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func fingerprint(e state.Entry) string {
	return e.Src + "|" + e.Domain + "|" + e.QueryType + "|" +
		strconv.FormatFloat(e.TimestampEpoch, 'f', 6, 64)
}

func hostOnly(src string) string {
	if h, _, err := net.SplitHostPort(src); err == nil {
		return h
	}
	return src
}
