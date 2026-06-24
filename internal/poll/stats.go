package poll

import (
	"context"
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
	g, err := getJSON[state.GlobalStats](ctx, p.client, p.url)
	if err != nil {
		log.Printf("stats: %v", err)
		p.state.MarkGlobalDown()
		return
	}
	p.state.SetGlobal(g)
}
