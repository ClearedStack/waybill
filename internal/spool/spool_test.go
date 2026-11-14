package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ClearedStack/waybill/internal/fault"
)

func TestSpoolFIFOAndRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	// Plant an incomplete .tmp file to verify Open cleans it up.
	outbox := filepath.Join(dir, "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outbox, "0000000000000001.json.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outbox, "0000000000000001.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("stale .tmp file was not removed: %v", err)
	}

	n1, err := s.Enqueue(Entry{
		Action: ActionComplete,
		Lease:  "PA-0001.1.1",
		ItemID: "PA-0001",
		State:  "approved",
		Result: json.RawMessage(`{"slug":"sample-tool"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := s.Enqueue(Entry{
		Action:      ActionNeedsSubtool,
		Lease:       "PA-0002.1.1",
		ItemID:      "PA-0002",
		Fingerprint: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Re-open across a simulated restart and enqueue a third entry.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n3, err := s2.Enqueue(Entry{
		Action: ActionFail,
		Lease:  "PA-0003.1.1",
		ItemID: "PA-0003",
		Code:   fault.Parse,
	})
	if err != nil {
		t.Fatal(err)
	}

	staged, err := s2.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 3 {
		t.Fatalf("got %d staged entries, want 3", len(staged))
	}
	if staged[0].Name != n1 || staged[1].Name != n2 || staged[2].Name != n3 {
		t.Fatalf("FIFO order broken: %v vs %q, %q, %q", staged, n1, n2, n3)
	}

	if err := s2.Remove(n1); err != nil {
		t.Fatal(err)
	}
	// Removing twice is harmless.
	if err := s2.Remove(n1); err != nil {
		t.Fatal(err)
	}
	rem, err := s2.List()
	if err != nil || len(rem) != 2 || rem[0].Entry.ItemID != "PA-0002" {
		t.Fatalf("unexpected remaining entries: %+v, err=%v", rem, err)
	}
}

func TestSpoolValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := []Entry{
		{Action: ActionComplete, State: "approved"},
		{Action: ActionComplete, Lease: "PA-0001.1.1"},
		{Action: ActionComplete, Lease: "PA-0001.1.1", State: "approved", Result: json.RawMessage(`[1,2]`)},
		{Action: ActionFail, Lease: "PA-0001.1.1", Code: fault.NeedsSubtool},
		{Action: "invented", Lease: "PA-0001.1.1"},
		{
			Action: ActionComplete,
			Lease:  "PA-0001.1.1",
			State:  "approved",
			Result: json.RawMessage(`{"big":"` + strings.Repeat("x", MaxEntryBytes) + `"}`),
		},
	}
	for i, e := range bad {
		if _, err := s.Enqueue(e); err == nil {
			t.Errorf("case %d (%+v): expected validation error", i, e)
		}
	}
}
