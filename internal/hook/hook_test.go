package hook

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuperMarioYL/preledger/internal/ledger"
)

func editPayload(session, transcript, toolUseID string) Payload {
	input, _ := json.Marshal(map[string]any{
		"file_path":  "/repo/internal/config/config.go",
		"old_string": "func Load() (Config, error) {",
		"new_string": "func Load() (Config, error) {\n\treturn Config{}, nil",
	})
	return Payload{
		SessionID:      session,
		TranscriptPath: transcript,
		HookEventName:  EventPostToolUse,
		ToolUseID:      toolUseID,
		ToolName:       ToolEdit,
		ToolInput:      input,
	}
}

func TestCaptureEditEntry(t *testing.T) {
	entries, err := Capture(editPayload("s1", "/root/proj/claude/s1.jsonl", "toolu_01"), "/repo")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}
	a := entries[0]
	if a.EditRef.SessionID != "s1" || a.EditRef.ToolUseID != "toolu_01" || a.EditRef.Tool != ToolEdit {
		t.Fatalf("edit ref lost payload identity: %+v", a.EditRef)
	}
	if a.EditRef.File != filepath.Join("internal", "config", "config.go") {
		t.Fatalf("file should be repo-relative, got %q", a.EditRef.File)
	}
	if a.Basis != "func Load() (Config, error) {" {
		t.Fatalf("basis must carry the verbatim old_string, got %q", a.Basis)
	}
	if a.Status != ledger.StatusOpen || a.ID == "" {
		t.Fatalf("entry must be open with an ID: %+v", a)
	}
	want := "internal/config/config.go 的现有内容中待替换片段存在且唯一：func Load() (Config, error) {"
	if a.Statement != want {
		t.Fatalf("statement %q, want %q", a.Statement, want)
	}
}

func TestCaptureReplaceAllStatement(t *testing.T) {
	p := editPayload("s1", "", "toolu_01")
	p.ToolInput, _ = json.Marshal(map[string]any{
		"file_path":   "/repo/main.go",
		"old_string":  "old",
		"new_string":  "new",
		"replace_all": true,
	})
	entries, err := Capture(p, "/repo")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !strings.Contains(entries[0].Statement, "包含待替换片段") {
		t.Fatalf("replace_all statement wrong: %q", entries[0].Statement)
	}
	if entries[0].Statement != "main.go 的现有内容包含待替换片段：old" {
		t.Fatalf("unexpected statement: %q", entries[0].Statement)
	}
}

func TestCaptureWriteEntry(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"file_path": "/repo/README.md", "content": "# Demo"})
	p := Payload{SessionID: "s1", ToolName: ToolWrite, ToolInput: input}
	entries, err := Capture(p, "/repo")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	a := entries[0]
	if a.Statement != "README.md 将被整体重写，旧内容全部废弃" {
		t.Fatalf("unexpected write statement: %q", a.Statement)
	}
	if a.Basis != "# Demo" {
		t.Fatalf("basis must carry the verbatim content, got %q", a.Basis)
	}
}

func TestCaptureIgnoresNonEditPayloads(t *testing.T) {
	if entries, err := Capture(Payload{ToolName: "Bash"}, "/repo"); err != nil || entries != nil {
		t.Fatalf("Bash must capture nothing, got %v, %v", entries, err)
	}
	if entries, err := Capture(Payload{HookEventName: "PreToolUse", ToolName: ToolEdit}, "/repo"); err != nil || entries != nil {
		t.Fatalf("PreToolUse must capture nothing, got %v, %v", entries, err)
	}
	// The hook event name is an unverified payload field: absent means proceed.
	if entries, err := Capture(editPayload("s1", "", "toolu_01"), "/repo"); err != nil || len(entries) != 1 {
		t.Fatalf("missing event name must still capture, got %v, %v", entries, err)
	}
}

func TestCaptureSessionIDFallsBackToTranscriptStem(t *testing.T) {
	p := editPayload("", "/root/proj/claude/85a01839-e080-4d1e-8301-69e9c172bd9f.jsonl", "t1")
	entries, err := Capture(p, "/repo")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got := entries[0].EditRef.SessionID; got != "85a01839-e080-4d1e-8301-69e9c172bd9f" {
		t.Fatalf("session id fallback to transcript stem failed: %q", got)
	}
}

func TestCaptureHunkFromToolResponse(t *testing.T) {
	p := editPayload("s1", "", "toolu_01")
	resp, _ := json.Marshal(map[string]any{
		"filePath": "/repo/main.go",
		"structuredPatch": []map[string]any{
			{"oldStart": 3, "oldLines": 4, "newStart": 3, "newLines": 6},
		},
	})
	p.ToolResponse = resp
	entries, err := Capture(p, "/repo")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got := entries[0].EditRef.Hunk; got != "@@ -3,4 +3,6 @@" {
		t.Fatalf("hunk summary %q, want \"@@ -3,4 +3,6 @@\"", got)
	}

	// Garbage tool_response must not break capture.
	p.ToolResponse = json.RawMessage("{not json")
	entries, err = Capture(p, "/repo")
	if err != nil || entries[0].EditRef.Hunk != "" {
		t.Fatalf("unparseable tool_response must yield empty hunk, got %q, %v", entries[0].EditRef.Hunk, err)
	}
}

func TestCaptureSnippetsLongQuotes(t *testing.T) {
	p := editPayload("s1", "", "toolu_01")
	p.ToolInput, _ = json.Marshal(map[string]any{
		"file_path":  "/repo/main.go",
		"old_string": strings.Repeat("假设", 60),
		"new_string": "x",
	})
	entries, err := Capture(p, "")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	stmt := entries[0].Statement
	quote, _, _ := strings.Cut(stmt, "：")
	if !strings.HasSuffix(stmt, "…") || len([]rune(quote)) > 81 { // 80 snippet runes + ellipsis
		t.Fatalf("long quote must be truncated in the statement: %q", stmt)
	}
}

func TestCaptureOutsideRepoPathStaysVerbatim(t *testing.T) {
	p := editPayload("s1", "", "toolu_01")
	entries, err := Capture(p, "/other")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if entries[0].EditRef.File != "/repo/internal/config/config.go" {
		t.Fatalf("path outside base dir must stay verbatim, got %q", entries[0].EditRef.File)
	}
}

func TestRunAppendsToLedger(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, ".preledger", "ledger.jsonl")
	payload, err := json.Marshal(editPayload("s1", "", "toolu_01"))
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	entries, err := Run(strings.NewReader(string(payload)), ledgerPath)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one appended entry, got %d", len(entries))
	}

	// Replaying the identical payload must fold to one current assumption.
	if _, err := Run(strings.NewReader(string(payload)), ledgerPath); err != nil {
		t.Fatalf("replay run: %v", err)
	}
	store := ledger.Open(ledgerPath)
	all, _ := store.ReadAll()
	if len(all) != 2 {
		t.Fatalf("append-only ledger must hold one record per event, has %d", len(all))
	}
	current, _ := store.Current()
	if len(current) != 1 || current[0].Status != ledger.StatusOpen {
		t.Fatalf("replayed edit must fold to one open assumption, got %+v", current)
	}
}

func TestRunRejectsUnparseableStdin(t *testing.T) {
	if _, err := Run(strings.NewReader("{not json"), filepath.Join(t.TempDir(), "ledger.jsonl")); err == nil {
		t.Fatal("unparseable stdin must error so the caller can report it")
	}
}

func TestHooksConfigShape(t *testing.T) {
	cfg := HooksConfig("preledger hook")
	groups, ok := cfg[EventPostToolUse].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("PostToolUse must map to one matcher group: %#v", cfg)
	}
	group := groups[0].(map[string]any)
	if group["matcher"] != Matcher {
		t.Fatalf("matcher %v, want %q", group["matcher"], Matcher)
	}
	commands := group["hooks"].([]any)
	entry := commands[0].(map[string]any)
	if entry["type"] != "command" || entry["command"] != "preledger hook" {
		t.Fatalf("command entry malformed: %#v", entry)
	}
	if _, isNum := entry["timeout"].(int); !isNum {
		t.Fatalf("timeout must be a number like real settings files: %#v", entry["timeout"])
	}
}
