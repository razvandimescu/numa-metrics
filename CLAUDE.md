# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A RAM-only agent that polls a local [numa](https://github.com/razvandimescu/numa)
DNS resolver's REST API and re-exports it as per-client Prometheus metrics plus a
drainable raw-row feed — **without modifying numa and without writing to the Pi's
SD card.** It targets a hardened Pi Zero v1 (ARMv6) running numa, with a laptop as
the durable/visualization tier reached over a reverse-SSH tunnel.

The README is unusually complete (architecture diagram, all flags, deployment
models, PromQL examples) — read it before making non-trivial changes.

## Commands

```bash
make build      # host binary -> ./numa-metrics
make pi         # static ARMv6 binary -> ./numa-metrics-armv6 (CGO off, no Docker/musl)
make consumer   # laptop-side drain-consumer -> ./drain-consumer
make test       # go test ./...
make vet
go test ./internal/state -run TestIngest   # single test
```

CI (`.github/workflows/ci.yml`, Go 1.26) gates on: `gofmt -l` must be empty,
`go vet`, `go test`, `go build`, and the ARMv6 cross-build. Keep `gofmt` clean.

## Architecture

Two independent binaries from one module. The split is deliberate and load-bearing:

- **Agent** (`main.go` + `internal/`): **stdlib-only, zero dependencies.** Runs on
  the Pi. Never import anything that pulls in a third-party package, or the ARMv6
  binary stops being dependency-free.
- **drain-consumer** (`cmd/drain-consumer/`): laptop-side, uses pure-Go
  `modernc.org/sqlite`. The *only* place that dependency may be linked. Never
  reference it from `internal/` or `main.go`.

### Agent data flow

```
poll.Poller   --GET /query-log--> dedup vs watermark --> state.Ingest
poll.StatsPoller --GET /stats--> state.SetGlobal
state.State (in-RAM aggregates + bounded ring)
serve.New --> /metrics (Prometheus text) | /drain (NDJSON cursor) | /healthz
```

- `internal/state` — the single source of truth, mutex-guarded. Holds per-client
  counters (`client`+`path`), last-seen, device info, **and a bounded ring buffer**
  of raw enriched rows (`Row`) carrying monotonic `Seq` numbers for cursor draining.
  `state.go` = aggregation + ring; `metrics.go` = Prometheus text rendering;
  `global.go` = the `/stats` DTO and snapshot setters.
- `internal/poll` — numa returns `/query-log` **newest-first** and stamps each entry
  with a monotonic **`seq`**, so the poller over-fetches the newest `limit` and keeps
  entries with `seq > lastSeq` (exact integer dedup), then ingests them chronologically.
  It detects two edge cases from the seq values: a **gap** (oldest fetched `seq > lastSeq+1`
  → entries rolled off numa's ring; bumps `numa_metrics_poll_overflow_total`) and a
  **numa restart** (newest `seq < lastSeq` → seq reset; re-ingest the window). The numa
  `seq` is decoded only here (poll's `logEntry`), never stored in `state.Row` — which has
  its *own* drain `Seq`. (Producer side: numa#310.)
- `internal/enrich` — IP → device name via static hosts file and/or `avahi-resolve`
  (shelled out), TTL-cached. `nil` enricher disables it entirely.
- `internal/serve` — loopback HTTP. The `session` token (process-start nanos, base36)
  is returned in the `X-Numa-Metrics-Session` header so a draining consumer can detect
  an agent restart (seq resets to 0) and rewind its cursor.

### drain-consumer

Pulls `/drain?after=<cursor>` incrementally into SQLite (`queries` table + a single-row
`cursor` table). Resumes after laptop sleep via the persisted cursor; survives agent
restarts via session-change detection (rewind to 0). Dedup is `INSERT OR IGNORE` on
`(session, seq)`. This is the **high-cardinality, per-domain** store — the thing that
must NOT live in Prometheus.

## Invariants — do not violate

- **Cardinality discipline.** Prometheus metric labels are `client` + `path` ONLY.
  **Domains never become metric labels** (that explodes cardinality). Per-domain detail
  belongs exclusively in the drained SQLite store. This is the core design constraint.
- **No SD writes / no numa changes.** The agent keeps all state in RAM and only reads
  numa's `/query-log` + `/stats`. The systemd unit (`packaging/numa-metrics.service`)
  grants no writable paths, runs `DynamicUser` + `ProtectSystem=strict`, and caps memory
  (`MemoryMax=64M`, `OOMPolicy=stop`) so a runaway ring is killed cleanly rather than
  triggering SD-swap "swap-of-death". Don't add code that needs to write to disk.
- **Loopback only.** Endpoints bind `127.0.0.1` and are reached solely through the SSH
  tunnel; per-client query data is sensitive. `-enrich=false` collects counts without
  device identities.

## Config

All flags have env equivalents (see README table / `main.go`). Defaults:
`-numa-url=http://127.0.0.1:5380`, `-listen=127.0.0.1:9353`, `-interval=10s`,
`-limit=1000`, `-ring=20000`. `-hosts-file` lets the agent attach device names off-LAN
(e.g. running on the laptop pointed at a remote numa, where avahi can't see the LAN).

## Deploy stack

`deploy/` is a self-contained compose stack (drain-consumer + Prometheus + Grafana with
both datasources and two dashboards auto-provisioned). The consumer reaches the agent at
`host.docker.internal:9353`; start the tunnel first.
