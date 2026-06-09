# numa-metrics

A tiny, RAM-only agent that turns a local [numa](https://github.com/razvandimescu/numa)
resolver's query log into per-client DNS metadata — **without modifying numa and
without writing to the Pi's SD card.**

It runs on the resolver host (e.g. a hardened Pi Zero), polls numa's `/query-log`
REST endpoint over loopback, aggregates per-client counters in memory, and serves
them on loopback for a remote collector to **scrape** (`/metrics`) and **drain**
(`/drain`) over an SSH tunnel.

```
pi-dns (this agent, RAM-only)            laptop (durable tier)         relay
  ├ poll /query-log every 10s              ├ Prometheus/VictoriaMetrics  └ transit only
  ├ enrich IP -> device (avahi / ARP-OUI)  ├ SQLite (domain history)        (reverse SSH;
  ├ /metrics  (client × path counters)     └ when awake: scrape /metrics      stores nothing)
  └ in-RAM drain ring (NDJSON cursor)  ◀──── + drain /drain over the tunnel
```

## Why it's shaped this way

- **No numa code changes.** numa's `/query-log` is the only data source.
- **No SD writes.** All state is in-process RAM; the systemd unit grants no
  writable paths. A reboot/power-loss drops the undrained buffer — acceptable for
  metadata, and the laptop holds the durable archive.
- **Memory-capped** (`MemoryMax=32M`, `OOMPolicy=stop`) so it can never trigger
  the SD-swap "swap-of-death" — a runaway is killed cleanly, numa untouched.
- **Cardinality discipline.** Metrics are labeled `client` + `path` only; domains
  are *never* metric labels (that would explode cardinality). Per-domain detail
  lives in the raw rows you drain into SQLite on the laptop.

## Build

```bash
make build      # host binary
make pi         # static ARMv6 binary (Pi Zero v1) -> numa-metrics-armv6
make test
```

ARMv6 cross-build needs no Docker/musl — `CGO_ENABLED=0 GOARCH=arm GOARM=6` and
the standard library only.

## Install on the Pi

```bash
scp numa-metrics-armv6 pi-dns-remote:/tmp/numa-metrics
ssh pi-dns-remote 'sudo install -m755 /tmp/numa-metrics /usr/local/bin/numa-metrics'
scp packaging/numa-metrics.service pi-dns-remote:/tmp/
ssh pi-dns-remote 'sudo install -m644 /tmp/numa-metrics.service /etc/systemd/system/ \
  && sudo systemctl daemon-reload && sudo systemctl enable --now numa-metrics'
```

## Endpoints (loopback only)

| Path | Purpose |
|------|---------|
| `GET /metrics` | Prometheus text — per-client counters **and** resolver-wide gauges (see below). |
| `GET /drain?after=<seq>&max=<n>` | NDJSON of buffered rows with `seq > after`, ascending. Track the max `seq` you receive and pass it back as `after` next time. |
| `GET /healthz` | Liveness. |

## Metrics exposed

**Per-client** (from `/query-log`, labeled `client` + `path` only — never domain):

```
numa_client_queries_total{client,path}        counter
numa_client_last_seen_timestamp_seconds{client} gauge
numa_client_info{client,name,vendor}           gauge (=1; join key for dashboards)
```

**Resolver-wide** (from `/stats`, disable with `-stats=false`):

```
numa_up                                        gauge (1 if last /stats scrape ok)
numa_build_info{version}                       gauge
numa_uptime_seconds                            gauge
numa_queries_by_path_total{path}               counter  (cached/upstream/blocked/...)
numa_transport_queries_total{transport}        counter
numa_cache_entries, numa_cache_max_entries     gauge
numa_blocking_enabled, numa_blocking_domains_loaded gauge
numa_overrides_active, numa_lan_peers          gauge
numa_memory_bytes{kind}                        gauge
```

Cache-hit-rate (the ratio #285's user computed by hand) is then just PromQL:
`100 * rate(numa_queries_by_path_total{path="cached"}[5m]) / rate(numa_queries_by_path_total{path=~"cached|upstream|local"}[5m])`.

## Visualize: Grafana stack (`deploy/`)

A ready Prometheus + Grafana compose stack lives in `deploy/`. Run it on the
laptop; it scrapes the agent over the SSH tunnel and ships a provisioned
dashboard (overview + per-client panels).

```bash
ssh -N -L 9353:127.0.0.1:9353 pi-dns-remote &   # tunnel the agent to the host
cd deploy && docker compose up -d                # Grafana → http://localhost:3000
```

Grafana auto-loads the Prometheus datasource and the **Numa — Overview & Per-Client**
dashboard. Edit `deploy/prometheus.yml` if the agent isn't on `:9353`.

## Consuming it from the laptop (over the tunnel)

Reach the loopback endpoints through the existing reverse-SSH alias:

```bash
# Prometheus / VictoriaMetrics scrape target (via an SSH -L forward, or ProxyJump):
ssh -N -L 9353:127.0.0.1:9353 pi-dns-remote &
#   then scrape http://127.0.0.1:9353/metrics

# Drain raw rows into the laptop's SQLite (cursor-based, resumes after sleep):
curl -s "http://127.0.0.1:9353/drain?after=${CURSOR}" | sqlite-import ...
```

## Config (flags or env)

| Flag | Env | Default | |
|------|-----|---------|--|
| `-numa-url` | `NUMA_URL` | `http://127.0.0.1:5380` | numa REST base URL |
| `-listen` | `LISTEN` | `127.0.0.1:9353` | loopback bind |
| `-interval` | `INTERVAL` | `10s` | poll interval |
| `-limit` | `LIMIT` | `1000` | entries fetched per poll |
| `-ring` | `RING` | `20000` | in-RAM drain ring size (rows) |
| `-enrich` | `ENRICH` | `true` | IP → device name/vendor |
| `-enrich-ttl` | `ENRICH_TTL` | `10m` | enrichment cache TTL |
| `-avahi` | `AVAHI` | `true` | use `avahi-resolve` for names |
| `-stats` | `STATS` | `true` | export resolver-wide gauges from `/stats` |
| `-stats-interval` | `STATS_INTERVAL` | `15s` | `/stats` poll interval |

## Notes & limits

- numa's `/query-log` has **no `since` filter** and returns newest-first, so the
  agent over-fetches the newest `limit` and dedups against a watermark. If a poll
  logs `page full … all new`, raise `-limit` or lower `-interval` — the query rate
  briefly exceeded one page and entries may have rolled off.
- The drain ring is bounded; if the laptop sleeps longer than the ring's depth,
  the oldest undrained rows are evicted (a gap, by design). Size `-ring` for your
  expected offline window.
- **Privacy:** per-client query data is sensitive. Keep endpoints on loopback,
  carry them only through the tunnel, and never park the domain-level rows on the
  public relay. Set `-enrich=false` to collect counts without device identities.
