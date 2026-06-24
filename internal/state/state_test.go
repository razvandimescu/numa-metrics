package state

import "testing"

func row(seq float64, src, domain, path string) Row {
	return Row{Entry: Entry{TimestampEpoch: seq, Src: src, Domain: domain, QueryType: "A", Path: path}}
}

func TestRingEvictsOldestKeepsSeqOrder(t *testing.T) {
	s := New(3)
	for i := 0; i < 5; i++ {
		s.Ingest([]Row{row(float64(i), "192.168.1.21:5", "x.com", "FORWARD")}, uint64(i+1), false)
	}
	got := s.RowsAfter(0, 0)
	if len(got) != 3 {
		t.Fatalf("want 3 rows after eviction, got %d", len(got))
	}
	if got[0].Seq != 3 || got[2].Seq != 5 {
		t.Fatalf("want seqs 3..5 in order, got %d..%d", got[0].Seq, got[2].Seq)
	}
}

func TestRowsAfterCursor(t *testing.T) {
	s := New(10)
	for i := 0; i < 4; i++ {
		s.Ingest([]Row{row(float64(i), "10.0.0.1:1", "y.com", "CACHED")}, uint64(i+1), false)
	}
	got := s.RowsAfter(2, 0) // seqs are 1..4; want > 2
	if len(got) != 2 || got[0].Seq != 3 {
		t.Fatalf("cursor drain wrong: got %d rows starting seq %d", len(got), seqOf(got))
	}
}

func TestCountersAndBlocked(t *testing.T) {
	s := New(10)
	s.Ingest([]Row{
		row(1, "192.168.1.21:1", "reddit.com", "BLOCKED"),
		row(2, "192.168.1.21:1", "good.com", "FORWARD"),
		row(3, "192.168.1.21:1", "ads.com", "BLOCKED"),
	}, 3, false)

	if s.counters[ckey{"192.168.1.21", "BLOCKED"}] != 2 {
		t.Fatalf("want 2 blocked for .21, got %d", s.counters[ckey{"192.168.1.21", "BLOCKED"}])
	}
	if s.counters[ckey{"192.168.1.21", "FORWARD"}] != 1 {
		t.Fatalf("want 1 forward for .21")
	}
	if s.lastSeen["192.168.1.21"] != 3 {
		t.Fatalf("last-seen should track max epoch")
	}
}

func seqOf(rows []Row) uint64 {
	if len(rows) == 0 {
		return 0
	}
	return rows[0].Seq
}
