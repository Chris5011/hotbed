package engine

import (
	"strings"
	"testing"
)

// TestParseQemuSnapshotList checks against the REAL `qemu-img snapshot -l`
// layout (headers to skip, numeric-leading data rows, dates joined into Info).
func TestParseQemuSnapshotList(t *testing.T) {
	out := "Snapshot list:\n" +
		"ID        TAG                  VM SIZE                DATE       VM CLOCK\n" +
		"1         manual-20250101-1200       0      2025-01-01 12:00:00   00:00:00.000\n" +
		"2         pre-upgrade                0      2025-03-02 09:41:00   00:00:00.000 (current)\n" +
		"\n"
	rows := parseQemuSnapshotList(out)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Tag != "manual-20250101-1200" {
		t.Errorf("row0 tag = %q", rows[0].Tag)
	}
	if rows[0].Info == "" || !strings.Contains(rows[0].Info, "2025-01-01 12:00:00") {
		t.Errorf("row0 info = %q (want date kept)", rows[0].Info)
	}
	if rows[1].Tag != "pre-upgrade" {
		t.Errorf("row1 tag = %q", rows[1].Tag)
	}
	if !strings.Contains(rows[1].Info, "current") {
		t.Errorf("row1 info = %q (want '(current)' marker kept)", rows[1].Info)
	}
	// empty list — fresh disk: headers only, zero rows.
	if r := parseQemuSnapshotList("Snapshot list:\nID  TAG  VM SIZE  DATE  VM CLOCK\n"); len(r) != 0 {
		t.Errorf("expected 0 rows for empty list, got %+v", r)
	}
}
