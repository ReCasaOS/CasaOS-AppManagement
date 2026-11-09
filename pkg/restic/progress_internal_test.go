package restic

import "testing"

func TestAStatusLineBecomesStats(t *testing.T) {
	stats, ok := parseStatus([]byte(`{"message_type":"status","seconds_elapsed":10,"seconds_remaining":56,"percent_done":0.61,` +
		`"total_files":25488,"files_done":9812,"total_bytes":1926565931,"bytes_done":1182844747,"current_files":["/DATA/x.jpg"]}`))

	if !ok {
		t.Fatal("a status line is a status")
	}
	if stats.Bytes != 1182844747 || stats.TotalBytes != 1926565931 || stats.Files != 9812 || stats.TotalFiles != 25488 ||
		stats.Speed != 118284474 || stats.ETA == nil || *stats.ETA != 56 || stats.Current != "/DATA/x.jpg" {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestTotalsStayUnknownWhileResticScans(t *testing.T) {
	stats, ok := parseStatus([]byte(`{"message_type":"status","seconds_elapsed":3,"total_files":120,"files_done":5,"total_bytes":9000,"bytes_done":300}`))

	if !ok {
		t.Fatal("a status line is a status")
	}
	if stats.TotalBytes != 0 || stats.TotalFiles != 0 || stats.ETA != nil {
		t.Fatalf("totals are a guess while restic still scans: %+v", stats)
	}
	if stats.Bytes != 300 || stats.Files != 5 || stats.Speed != 100 {
		t.Fatalf("what is done is known: %+v", stats)
	}
}

func TestOtherLinesAreNotStatus(t *testing.T) {
	for _, line := range []string{`{"message_type":"summary","files_new":3}`, `not json`, ``, `{"message_type":"status"`} {
		if _, ok := parseStatus([]byte(line)); ok {
			t.Fatalf("%q is not a status", line)
		}
	}
}
