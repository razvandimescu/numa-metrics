// numa-metrics: a tiny, RAM-only agent that polls a local numa resolver's
// query-log, aggregates per-client metrics, and serves them on loopback for a
// remote collector to scrape (/metrics) and drain (/drain) over an SSH tunnel.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/razvandimescu/numa-metrics/internal/enrich"
	"github.com/razvandimescu/numa-metrics/internal/poll"
	"github.com/razvandimescu/numa-metrics/internal/serve"
	"github.com/razvandimescu/numa-metrics/internal/state"
)

func main() {
	numaURL := flag.String("numa-url", env("NUMA_URL", "http://127.0.0.1:5380"), "base URL of the numa REST API")
	listen := flag.String("listen", env("LISTEN", "127.0.0.1:9353"), "loopback address for /metrics, /drain, /healthz")
	interval := flag.Duration("interval", envDur("INTERVAL", 10*time.Second), "poll interval")
	limit := flag.Int("limit", envInt("LIMIT", 1000), "query-log entries fetched per poll")
	ringCap := flag.Int("ring", envInt("RING", 20000), "in-RAM drain ring capacity (rows)")
	doEnrich := flag.Bool("enrich", envBool("ENRICH", true), "resolve client IP -> device name")
	enrichTTL := flag.Duration("enrich-ttl", envDur("ENRICH_TTL", 10*time.Minute), "enrichment cache TTL")
	avahi := flag.Bool("avahi", envBool("AVAHI", true), "use avahi-resolve for device names")
	doStats := flag.Bool("stats", envBool("STATS", true), "export resolver-wide gauges from /stats")
	statsInterval := flag.Duration("stats-interval", envDur("STATS_INTERVAL", 15*time.Second), "/stats poll interval")
	hostsFile := flag.String("hosts-file", env("HOSTS_FILE", ""), "static 'IP name' map for device names (works off-LAN)")
	flag.Parse()
	token := os.Getenv("NUMA_API_TOKEN") // env only: flags are visible in ps

	st := state.New(*ringCap)
	var en *enrich.Enricher
	if *doEnrich {
		var hosts map[string]string
		if *hostsFile != "" {
			if h, err := enrich.LoadHosts(*hostsFile); err != nil {
				log.Printf("hosts-file: %v (continuing without static names)", err)
			} else {
				hosts = h
				log.Printf("loaded %d static host names from %s", len(hosts), *hostsFile)
			}
		}
		en = enrich.New(*enrichTTL, *avahi, hosts)
	}
	p := poll.New(*numaURL, token, *limit, *interval, st, en)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go p.Run(ctx)
	if *doStats {
		go poll.NewStats(*numaURL, token, *statsInterval, st).Run(ctx)
	}

	session := strconv.FormatInt(time.Now().UnixNano(), 36)
	srv := &http.Server{Addr: *listen, Handler: serve.New(st, session)}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Printf("numa-metrics: polling %s every %s, serving %s (enrich=%v)", *numaURL, *interval, *listen, *doEnrich)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
