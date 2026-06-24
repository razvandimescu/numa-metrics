package state

import (
	"strings"
	"testing"
)

func TestWriteMetricsShape(t *testing.T) {
	s := New(10)
	s.Ingest([]Row{
		{Entry: Entry{TimestampEpoch: 100, Src: "192.168.1.21:5", Domain: "reddit.com", QueryType: "A", Path: "BLOCKED"}, Name: "kid-pc.local"},
		{Entry: Entry{TimestampEpoch: 101, Src: "192.168.1.21:5", Domain: "good.com", QueryType: "A", Path: "FORWARD"}},
	}, 101, false)

	var sb strings.Builder
	s.WriteMetrics(&sb)
	out := sb.String()

	for _, want := range []string{
		`numa_client_queries_total{client="192.168.1.21",path="BLOCKED"} 1`,
		`numa_client_queries_total{client="192.168.1.21",path="FORWARD"} 1`,
		`numa_client_info{client="192.168.1.21",name="kid-pc.local"} 1`,
		"# TYPE numa_client_queries_total counter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing line:\n  %s\n--- got ---\n%s", want, out)
		}
	}
}
