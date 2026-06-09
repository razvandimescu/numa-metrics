// Package serve exposes /metrics (Prometheus text), /drain (NDJSON cursor pull),
// and /healthz. Bind to loopback only — reachable solely through the SSH tunnel.
package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/razvandimescu/numa-metrics/internal/state"
)

// New builds the loopback router. session identifies this agent process so a
// draining consumer can detect a restart (seq resets to 0) and rewind safely.
func New(st *state.State, session string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		st.WriteMetrics(w)
	})

	mux.HandleFunc("/drain", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		max := 0
		if m := r.URL.Query().Get("max"); m != "" {
			if n, err := strconv.Atoi(m); err == nil {
				max = n
			}
		}
		w.Header().Set("X-Numa-Metrics-Session", session)
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, row := range st.RowsAfter(after, max) {
			if err := enc.Encode(row); err != nil {
				return
			}
		}
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok session=%s\n", session)
	})

	return mux
}
