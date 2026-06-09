package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/razvandimescu/numa-metrics/internal/state"
)

// StatsPoller fetches numa's GET /stats (resolver-wide aggregates) on an
// interval and stores the snapshot for export as global gauges.
type StatsPoller struct {
	url      string
	interval time.Duration
	state    *state.State
	client   *http.Client
}

func NewStats(baseURL string, interval time.Duration, st *state.State) *StatsPoller {
	return &StatsPoller{
		url:      baseURL + "/stats",
		interval: interval,
		state:    st,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

func (p *StatsPoller) Run(ctx context.Context) {
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

func (p *StatsPoller) once(ctx context.Context) {
	g, err := p.fetch(ctx)
	if err != nil {
		log.Printf("stats: %v", err)
		p.state.MarkGlobalDown()
		return
	}
	p.state.SetGlobal(g)
}

func (p *StatsPoller) fetch(ctx context.Context) (state.GlobalStats, error) {
	var g state.GlobalStats
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return g, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return g, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return g, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return g, err
	}
	return g, nil
}
