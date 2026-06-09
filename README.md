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
  ├ enrich IP -> device (hosts-file / avahi)  ├ SQLite (domain history)        (reverse SSH;
  ├ /metrics  (client × path counters)     └ when awake: scrape /metrics      stores nothing)
  └ in-RAM drain ring (NDJSON cursor)  ◀──── + drain /drain over the tunnel
```

The **agent** (what runs on the Pi) is stdlib-only and dependency-free. The
optional **drain-consumer** (laptop-side) uses pure-Go `modernc.org/sqlite` — it
is never linked into the Pi binary.

## Why it's shaped this way

- **No numa code changes.** numa's `/query-log` + `/stats` are the only sources.
- **No SD writes.** All state is in-process RAM; the systemd unit grants no
  writable paths. A reboot/power-loss drops the undrained buffer — acceptable for
  metadata, and the laptop holds the durable archive.
- **Memory-capped** (`MemoryMax` + `OOMPolicy=stop` in the unit) so it can never
  trigger the SD-swap "swap-of-death" — a runaway is killed cleanly, numa untouched.
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
make pi   # numa-metrics-armv6
scp numa-metrics-armv6 packaging/numa-metrics.service devices.txt pi-dns-remote:/tmp/
# then on the Pi (sudo):
sudo install -m755 /tmp/numa-metrics-armv6 /usr/local/bin/numa-metrics
sudo install -m644 /tmp/numa-metrics.service /etc/systemd/system/numa-metrics.service
sudo mkdir -p /etc/numa-metrics && sudo install -m644 /tmp/devices.txt /etc/numa-metrics/devices.txt
sudo systemctl daemon-reload && sudo systemctl enable --now numa-metrics
```

`devices.txt` (router-lease names) is optional — the unit's `HOSTS_FILE` points at
`/etc/numa-metrics/devices.txt` but a missing file is non-fatal. On the Pi,
avahi also resolves mDNS names automatically.

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
numa_client_info{client,name}                  gauge (=1; join key for dashboards)
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

## Per-domain history (SQLite, from `/drain`)

Per-*domain*, per-client detail is **deliberately not** in Prometheus (domain as a
label = cardinality blow-up). Instead the **drain-consumer** pulls `/drain`
incrementally into SQLite — the durable, high-cardinality store you run
ad-hoc SQL and Grafana tables over. It resumes after sleep via a persisted cursor
and survives agent restarts via the `X-Numa-Metrics-Session` header (seq rewind).

```sql
-- top domains for a device
SELECT domain, count(*) q FROM queries WHERE client='192.168.1.21' GROUP BY domain ORDER BY q DESC;
-- what got blocked, per device
SELECT client, domain, count(*) FROM queries WHERE path='BLOCKED' GROUP BY client, domain;
```

## Visualize: Grafana stack (`deploy/`)

`deploy/` is a self-contained compose stack: **drain-consumer** (→ SQLite),
**Prometheus** (scrapes `/metrics`), and **Grafana** with both datasources
auto-provisioned plus two dashboards.

```bash
ssh -N -L 9353:127.0.0.1:9353 pi-dns-remote &   # tunnel the agent to the host
cd deploy && docker compose up -d --build        # Grafana → http://localhost:3000
```

- **Numa — Overview & Per-Client** (Prometheus): cache-hit %, block %, queries/sec
  by path, top clients, memory.
- **Numa — Per-Client Domains** (SQLite): `$client` selector → top domains, top
  blocked domains, queries/min.

The consumer reaches the agent at `host.docker.internal:9353`; edit
`deploy/prometheus.yml` / the `AGENT_URL` env if it's elsewhere.

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
| `-enrich` | `ENRICH` | `true` | IP → device name |
| `-enrich-ttl` | `ENRICH_TTL` | `10m` | enrichment cache TTL |
| `-avahi` | `AVAHI` | `true` | use `avahi-resolve` for names |
| `-stats` | `STATS` | `true` | export resolver-wide gauges from `/stats` |
| `-stats-interval` | `STATS_INTERVAL` | `15s` | `/stats` poll interval |
| `-hosts-file` | `HOSTS_FILE` | _(none)_ | static `IP name` map for device names (works off-LAN, e.g. when the agent runs on the laptop pointing at a remote numa) |

## Two deployment models

- **Agent on the Pi (recommended for a real deployment):** enrichment (avahi + hosts-file)
  works, and the Pi's in-RAM ring keeps filling while the laptop sleeps — drain on
  wake, no gaps. Needs a one-time `scp` + systemd install.
- **Agent on the laptop, pointed at a remote numa over the tunnel (quick/easy):**
  set `-numa-url` to the tunneled numa API. Per-client *IPs* still come through;
  for device *names* use `-hosts-file` (avahi can't see the remote LAN). Caveat:
  if the laptop sleeps, numa's 1000-entry log ring rolls over and you lose that
  window — there's no Pi-side buffer in this model.

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
