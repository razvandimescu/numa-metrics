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

func New(st *state.State) http.Handler {
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
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, row := range st.RowsAfter(after, max) {
			if err := enc.Encode(row); err != nil {
				return
			}
		}
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	return mux
}
