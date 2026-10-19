package ledger

import (
	"path/filepath"
	"strings"
	"testing"
)

func sampleRef(session string) EditRef {
	return EditRef{
		SessionID: session,
		ToolUseID: "toolu_01",
		Tool:      "Edit",
		File:      "config.go",
		Hunk:      "@@ -12,3 +12,5 @@",
	}
}

func TestNewIDDeterministic(t *testing.T) {
	ref := sampleRef("s1")
	a := NewID(ref, "config.go 存在")
	b := NewID(EditRef{SessionID: "s1", ToolUseID: "toolu_01", Tool: "Edit", File: "config.go", Hunk: "@@ -12,3 +12,5 @@"}, "config.go 存在")
	if a != b {
		t.Fatalf("same edit + statement produced different IDs: %s vs %s", a, b)
	}
	if NewID(ref, "另一条假设") == a {
		t.Fatal("different statements must produce different IDs")
	}
	if NewID(sampleRef("s2"), "config.go 存在") == a {
		t.Fatal("different sessions must produce different IDs")
	}
}

func TestAppendAndReadAll(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), ".preledger", "ledger.jsonl"))

	a := Assumption{ID: NewID(sampleRef("s1"), "premise"), EditRef: sampleRef("s1"), Statement: "premise", Basis: "quote", Status: StatusOpen}
	if err := store.Append(a); err != nil {
		t.Fatalf("append: %v", err)
	}
	b := a
	b.ID = NewID(sampleRef("s1"), "second")
	b.Statement = "second"
	if err := store.Append(b); err != nil {
		t.Fatalf("append second: %v", err)
	}

	got, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(got) != 2 || got[0].Statement != "premise" || got[1].Statement != "second" {
		t.Fatalf("unexpected records: %+v", got)
	}
}

func TestReadAllMissingFile(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	got, err := store.ReadAll()
	if err != nil || got != nil {
		t.Fatalf("missing ledger should read as empty, got %v, %v", got, err)
	}
}

func TestCurrentFoldsToLatestPerID(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	id := NewID(sampleRef("s1"), "premise")
	if err := store.Append(Assumption{ID: id, EditRef: sampleRef("s1"), Statement: "premise", Status: StatusOpen}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Append(Assumption{ID: id, EditRef: sampleRef("s1"), Statement: "premise", Status: StatusRefuted}); err != nil {
		t.Fatalf("append update: %v", err)
	}
	other := Assumption{ID: NewID(sampleRef("s1"), "other"), EditRef: sampleRef("s1"), Statement: "other", Status: StatusOpen}
	if err := store.Append(other); err != nil {
		t.Fatalf("append other: %v", err)
	}

	current, err := store.Current()
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if len(current) != 2 {
		t.Fatalf("want 2 folded assumptions, got %d", len(current))
	}
	if current[0].Status != StatusRefuted {
		t.Fatalf("latest record per ID must win, got status %q", current[0].Status)
	}
	if current[0].ID != id || current[1].ID != other.ID {
		t.Fatalf("first-appearance order not preserved: %+v", current)
	}
}

func TestSetStatusAppendsRecord(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	id := NewID(sampleRef("s1"), "premise")
	if err := store.Append(Assumption{ID: id, EditRef: sampleRef("s1"), Statement: "premise", Status: StatusOpen}); err != nil {
		t.Fatalf("append: %v", err)
	}

	if err := store.SetStatus(id, StatusConfirmed); err != nil {
		t.Fatalf("set status: %v", err)
	}
	all, _ := store.ReadAll()
	if len(all) != 2 {
		t.Fatalf("status change must append a record, file has %d", len(all))
	}
	if all[0].Status != StatusOpen || all[1].Status != StatusConfirmed {
		t.Fatalf("original record must stay untouched: %+v", all)
	}

	if err := store.SetStatus(id, "bogus"); err == nil {
		t.Fatal("unknown status must error")
	}
	if err := store.SetStatus("missing", StatusOpen); err == nil {
		t.Fatal("unknown id must error")
	}
}

func TestAppendRejectsEmptyID(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	err := store.Append(Assumption{Statement: "no id"})
	if err == nil || !strings.Contains(err.Error(), "id is empty") {
		t.Fatalf("empty id must error, got %v", err)
	}
}
