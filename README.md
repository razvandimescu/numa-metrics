# numa-metrics

Per-client DNS observability for the [numa](https://github.com/razvandimescu/numa)
resolver, designed to run on hardware too small to host a metrics stack: a
RAM-only agent on the Pi, durable storage on a machine you trust.

Running on a Pi Zero with a fragile SD card forces real discipline — no disk
writes, a hard memory cap, strict metric cardinality — so observing the resolver
can never destabilize it. The agent polls numa's `/query-log` over loopback,
aggregates per-client counters in RAM, and serves them for a remote collector to
**scrape** (`/metrics`) and **drain** (`/drain`) over an SSH tunnel.

```
pi-dns (this agent, RAM-only)              laptop (durable tier)
  ├ poll /query-log every 10s                ├ Prometheus / VictoriaMetrics
  ├ enrich IP -> device (hosts-file/avahi)   ├ SQLite (domain history)
  ├ /metrics  (client × path counters)       └ when awake: scrape /metrics
  └ in-RAM drain ring (NDJSON cursor)  ◀────    + drain /drain over an SSH tunnel
```

The **agent** (what runs on the Pi) is stdlib-only and dependency-free. The
optional **drain-consumer** (laptop-side) uses pure-Go `modernc.org/sqlite` — it
is never linked into the Pi binary.

## Pick your path

The **agent** is always the producer; the **`deploy/` pod** (drain-consumer +
Prometheus + Grafana) is always the durable/visualization tier. Two ways to run
the producer:

- **Real deployment — agent on the Pi (recommended).** A static ARMv6 binary
  under systemd: enrichment (avahi + hosts-file) works, and the Pi's in-RAM ring
  keeps filling while the laptop sleeps — drain on wake, no gaps. One-time `scp` +
  systemd install, then bring up the pod on the laptop over the tunnel.
- **Try it in ~5 min — agent as a host binary on the laptop.** Point `-numa-url`
  at a local or tunneled numa and run the pod alongside it. Per-client *IPs* come
  through; for device *names* use `-hosts-file` (avahi can't see a remote LAN).
  Caveat: if the laptop sleeps, numa's 1000-entry log ring rolls over and that
  window is lost — there's no Pi-side buffer in this model.

→ Install: [on the Pi](#install-on-the-pi) · Visualize: [the observability pod](#the-observability-pod-deploy)

## Scope & data ethics

**What this is.** A home-lab tool to understand *your own* network's DNS
behavior — which devices are noisy, what is failing to resolve, the broad shape
of traffic.

**What it is not.** Per-client DNS metadata is among the most revealing data a
network produces. This is not built for, and should not be pointed at, networks
whose users have not consented to being measured. The design enforces that
posture: endpoints bind to loopback only, device identities are off by default
(`-enrich=false`), and domain-level detail is deliberately kept out of always-on
metrics — it lives only in a drained store you control.

*This repository is private precisely because the design is dual-use; I would
rather discuss the boundaries than publish them unframed.*

## Why it's shaped this way

- **A clean primitive, not a polling hack.** numa's `/query-log` returns
  newest-first with no `since` filter; an early version inferred new queries by
  diffing snapshots against a fingerprint watermark — brittle and racy under
  bursts. The durable fix was upstream: a monotonic `seq` field contributed into
  numa itself ([numa#310](https://github.com/razvandimescu/numa/pull/310)), so the
  agent reads an exact, gap-free cursor. The integration surface is one
  well-defined field, not a workaround.
- **Ephemeral by design.** All state is in-process RAM; the systemd unit grants no
  writable paths, so the agent never touches the SD card. Losing the undrained
  buffer on reboot is an accepted cost — the laptop holds the durable archive.
- **Cannot destabilize the resolver it watches.** A hard memory cap (`MemoryMax` +
  `OOMPolicy=stop`) means a runaway agent is killed cleanly rather than dragging
  numa down with it (no SD-swap "swap-of-death").
- **Cardinality discipline.** Metrics are labeled `client` + `path` only; domains
  are *never* metric labels (that would explode cardinality). Per-domain detail
  lives in the rows you drain into SQLite on the laptop.

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

## The observability pod (`deploy/`)

`deploy/` is a self-contained compose stack — **drain-consumer** (→ SQLite),
**Prometheus** (scrapes `/metrics`), and **Grafana** with both datasources and two
dashboards auto-provisioned. The **agent is not in the pod**: it's the producer
(on the Pi, or a host binary for a quick local try) and the pod is the consumer
tier that scrapes and drains it over the tunnel.

Fastest path to a dashboard — agent as a host binary on the laptop:

```bash
make build && ./numa-metrics -numa-url=http://127.0.0.1:5380 &   # producer (host binary)
ssh -N -L 9353:127.0.0.1:9353 pi-dns-remote &                    # only if numa is remote
cd deploy && docker compose up -d --build                        # pod → http://localhost:3000
```

For the Pi deployment the agent already runs under systemd — skip the binary, just
start the tunnel and `docker compose up`.

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
| — | `NUMA_API_TOKEN` | — | numa API token; needed only when `NUMA_URL` is not loopback |
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

## Notes & limits

- **Gap detection.** The agent dedups by numa's `seq` (see [Why it's shaped this
  way](#why-its-shaped-this-way)) and notices when a burst evicts entries from
  numa's 1000-entry ring before it polls: it logs `gap — missed numa seqs …` and
  bumps `numa_metrics_poll_gap_total`. Raise `-limit` or lower `-interval` to avoid
  it. A numa restart (seq resets) is caught from the backwards jump and the window
  re-ingested. Requires numa with per-entry `seq`
  ([numa#310](https://github.com/razvandimescu/numa/pull/310)).
- **Bounded drain ring, by design.** If the laptop sleeps longer than the ring's
  depth, the oldest undrained rows are evicted — an accepted gap, not a bug. Size
  `-ring` for your expected offline window.
- **Keep the data on loopback.** Per [Scope & data ethics](#scope--data-ethics),
  endpoints stay on loopback and travel only through the SSH tunnel; any host that
  tunnel transits (e.g. a reverse-SSH relay fronting a NAT'd Pi) must forward bytes
  only, never store rows. `-enrich=false` collects counts without device identities.
