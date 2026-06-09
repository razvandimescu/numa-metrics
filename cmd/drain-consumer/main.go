// drain-consumer pulls raw rows from a numa-metrics agent's /drain endpoint and
// folds them into a local SQLite database — the durable, high-cardinality store
// (per-domain, per-client) that does NOT belong in Prometheus. Runs on the
// laptop; reaches the agent over the SSH tunnel. Cursor + session let it resume
// after sleep and survive agent restarts (seq resets) without dup or loss.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type row struct {
	Seq            uint64  `json:"seq"`
	TimestampEpoch float64 `json:"timestamp_epoch"`
	Src            string  `json:"src"`
	Domain         string  `json:"domain"`
	QueryType      string  `json:"query_type"`
	Path           string  `json:"path"`
	Transport      string  `json:"transport"`
	Rescode        string  `json:"rescode"`
	LatencyMs      float64 `json:"latency_ms"`
	Name           string  `json:"name"`
}

const schema = `
CREATE TABLE IF NOT EXISTS queries (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  session   TEXT NOT NULL,
  seq       INTEGER NOT NULL,
  ts        REAL NOT NULL,
  client    TEXT NOT NULL,
  name      TEXT,
  domain    TEXT NOT NULL,
  qtype     TEXT,
  path      TEXT,
  transport TEXT,
  rescode   TEXT,
  latency_ms REAL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_dedup ON queries(session, seq);
CREATE INDEX IF NOT EXISTS idx_client_domain ON queries(client, domain);
CREATE INDEX IF NOT EXISTS idx_ts ON queries(ts);
CREATE TABLE IF NOT EXISTS cursor (id INTEGER PRIMARY KEY CHECK (id = 1), session TEXT, seq INTEGER);
`

func main() {
	agentURL := flag.String("agent-url", env("AGENT_URL", "http://127.0.0.1:9353"), "numa-metrics agent base URL")
	dbPath := flag.String("db", env("DB", "numa_metrics.db"), "SQLite database path")
	interval := flag.Duration("interval", envDur("INTERVAL", 30*time.Second), "drain interval")
	retentionDays := flag.Int("retention-days", envInt("RETENTION_DAYS", 30), "drop domain rows older than N days (0 = keep forever)")
	batch := flag.Int("batch", envInt("BATCH", 5000), "max rows per drain request")
	flag.Parse()

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", *dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("schema: %v", err)
	}

	c := &consumer{db: db, url: *agentURL, batch: *batch, client: &http.Client{Timeout: 15 * time.Second}}
	c.session, c.cursor = c.loadCursor()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("drain-consumer: %s -> %s every %s (retention %dd)", *agentURL, *dbPath, *interval, *retentionDays)
	t := time.NewTicker(*interval)
	defer t.Stop()
	c.tick(ctx, *retentionDays)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx, *retentionDays)
		}
	}
}

type consumer struct {
	db      *sql.DB
	url     string
	batch   int
	client  *http.Client
	session string
	cursor  uint64
}

func (c *consumer) tick(ctx context.Context, retentionDays int) {
	if err := c.drain(ctx); err != nil {
		log.Printf("drain: %v", err)
	}
	if retentionDays > 0 {
		cutoff := float64(time.Now().Unix() - int64(retentionDays)*86400)
		if _, err := c.db.ExecContext(ctx, "DELETE FROM queries WHERE ts < ?", cutoff); err != nil {
			log.Printf("retention: %v", err)
		}
	}
}

func (c *consumer) drain(ctx context.Context) error {
	u := fmt.Sprintf("%s/drain?after=%d&max=%d", c.url, c.cursor, c.batch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	// Session detection: adopt on cold start; rewind on change (agent restarted,
	// seq counter reset) so we don't stall on a now-stale cursor.
	if sess := resp.Header.Get("X-Numa-Metrics-Session"); sess != "" {
		switch {
		case c.session == "":
			c.session = sess
		case sess != c.session:
			log.Printf("agent session changed (%q -> %q): rewinding cursor", c.session, sess)
			c.session = sess
			c.cursor = 0
			c.saveCursor()
			return nil // next tick pulls from 0 against the new session
		}
	}

	dec := json.NewDecoder(resp.Body)
	rows := make([]row, 0, c.batch)
	for {
		var r row
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return nil
	}
	return c.insert(ctx, rows)
}

func (c *consumer) insert(ctx context.Context, rows []row) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO queries
		(session, seq, ts, client, name, domain, qtype, path, transport, rescode, latency_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	maxSeq := c.cursor
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, c.session, r.Seq, r.TimestampEpoch, hostOnly(r.Src),
			r.Name, r.Domain, r.QueryType, r.Path, r.Transport, r.Rescode, r.LatencyMs); err != nil {
			tx.Rollback()
			return err
		}
		if r.Seq > maxSeq {
			maxSeq = r.Seq
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cursor(id, session, seq) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET session=excluded.session, seq=excluded.seq`,
		c.session, maxSeq); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.cursor = maxSeq
	log.Printf("ingested %d rows (cursor=%d)", len(rows), c.cursor)
	return nil
}

func (c *consumer) loadCursor() (session string, seq uint64) {
	row := c.db.QueryRow("SELECT session, seq FROM cursor WHERE id = 1")
	var s sql.NullString
	var n sql.NullInt64
	if err := row.Scan(&s, &n); err != nil {
		return "", 0
	}
	return s.String, uint64(n.Int64)
}

func (c *consumer) saveCursor() {
	c.db.Exec(`INSERT INTO cursor(id, session, seq) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET session=excluded.session, seq=excluded.seq`, c.session, c.cursor)
}

func hostOnly(src string) string {
	if h, _, err := net.SplitHostPort(src); err == nil {
		return h
	}
	return src
}

func env(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}

func envDur(k string, d time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok {
		if x, err := time.ParseDuration(v); err == nil {
			return x
		}
	}
	return d
}
