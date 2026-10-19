// Package hook implements the Claude Code capture side of Preledger.
//
// "preledger install" wires a PostToolUse hook (matcher "Edit|Write") into the
// repo's .claude/settings.json; Claude Code then starts "preledger hook" as a
// short-lived child process per edit and writes one JSON payload to its stdin.
// Run maps that payload to assumption entries and appends them to the ledger.
//
// Schema notes (from the plan's schema smoke against real samples):
//   - Verified on stdin: tool_name and tool_input. For Edit, tool_input keys
//     are {file_path, old_string, new_string, replace_all}; for Write, content.
//   - Documented but unverified against a stored sample: session_id,
//     transcript_path, hook_event_name, tool_use_id, tool_response. Every one
//     is treated as optional: the session id falls back to the transcript file
//     stem (Claude Code names transcript files after the session id), and the
//     diff hunk is taken from tool_response.structuredPatch only when present.
//   - Transcript paths may point at main-session files or nested sidechain
//     files (e.g. <session>/subagents/workflows/<wf>/agent-*.jsonl); both are
//     recorded verbatim in the edit ref.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SuperMarioYL/preledger/internal/ledger"
)

// Matcher lists the edit tools the installed hook captures.
const Matcher = "Edit|Write"

// Hook event and tool names used by the capture path.
const (
	EventPostToolUse = "PostToolUse"
	ToolEdit         = "Edit"
	ToolWrite        = "Write"
)

// Payload mirrors the JSON document Claude Code writes to a hook command's
// stdin. Only tool_name and tool_input are guaranteed present.
type Payload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	HookEventName  string          `json:"hook_event_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

// editInput carries the tool_input keys the capture path understands; unknown
// keys are ignored by encoding/json, so payloads from other tool versions
// still parse.
type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Content    string `json:"content"` // Write tool
}

// toolUseResult carries the diff hunks Claude Code attaches to edit tool
// results; unverified, so parsed leniently.
type toolUseResult struct {
	StructuredPatch []hunk `json:"structuredPatch"`
}

type hunk struct {
	OldStart int `json:"oldStart"`
	OldLines int `json:"oldLines"`
	NewStart int `json:"newStart"`
	NewLines int `json:"newLines"`
}

// Run processes one hook invocation: read the stdin payload, derive the
// per-edit assumption entries, and append them to the ledger at ledgerPath.
// It returns the appended entries; non-edit payloads yield no entries and no
// error. The hook process itself must still exit 0 on failure — that policy
// lives in the caller (a capture hiccup must never block the agent's edits).
func Run(stdin io.Reader, ledgerPath string) ([]ledger.Assumption, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read hook stdin: %w", err)
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse hook payload: %w", err)
	}

	// Claude Code starts hooks with the project dir as cwd, so relative file
	// paths in ledger entries resolve against it.
	base, _ := os.Getwd()
	entries, err := Capture(p, base)
	if err != nil || len(entries) == 0 {
		return entries, err
	}
	store := ledger.Open(ledgerPath)
	for _, a := range entries {
		if err := store.Append(a); err != nil {
			return entries, fmt.Errorf("append to ledger: %w", err)
		}
	}
	return entries, nil
}

// Capture maps one hook payload to ledger entries. Only PostToolUse events
// from edit tools produce entries; the mechanical premise recorded here is
// the one every edit provably makes from its own payload (the replaced text
// exists in the file, the rewritten file supersedes its old content). Deeper
// transcript-based heuristics layer on top in internal/extract.
func Capture(p Payload, baseDir string) ([]ledger.Assumption, error) {
	if p.HookEventName != "" && p.HookEventName != EventPostToolUse {
		return nil, nil
	}
	if p.ToolName != ToolEdit && p.ToolName != ToolWrite {
		return nil, nil
	}
	var in editInput
	if len(p.ToolInput) > 0 {
		if err := json.Unmarshal(p.ToolInput, &in); err != nil {
			return nil, fmt.Errorf("parse tool_input for %s: %w", p.ToolName, err)
		}
	}
	if in.FilePath == "" {
		return nil, nil
	}

	file := relativize(in.FilePath, baseDir)
	ref := ledger.EditRef{
		SessionID:      sessionID(p),
		TranscriptPath: p.TranscriptPath,
		ToolUseID:      p.ToolUseID,
		Tool:           p.ToolName,
		File:           file,
		Hunk:           hunkSummary(p.ToolResponse),
	}
	a := ledger.Assumption{EditRef: ref, Status: ledger.StatusOpen}
	switch p.ToolName {
	case ToolEdit:
		a.Statement = editStatement(file, in)
		a.Basis = in.OldString
	case ToolWrite:
		a.Statement = fmt.Sprintf("%s 将被整体重写，旧内容全部废弃", file)
		a.Basis = in.Content
	}
	a.ID = ledger.NewID(ref, a.Statement)
	return []ledger.Assumption{a}, nil
}

// editStatement states the premise every Edit call makes: the text being
// replaced exists in the file, and is unique in it unless replace_all is set
// (otherwise the Edit itself fails).
func editStatement(file string, in editInput) string {
	quote := snippet(in.OldString)
	if in.ReplaceAll {
		return fmt.Sprintf("%s 的现有内容包含待替换片段：%s", file, quote)
	}
	return fmt.Sprintf("%s 的现有内容中待替换片段存在且唯一：%s", file, quote)
}

// snippet compresses a quote into a short single-line fragment for the
// statement; the verbatim text stays in Basis.
func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 80 {
		return string(runes[:80]) + "…"
	}
	return s
}

// sessionID prefers the payload's session_id and falls back to the transcript
// file stem, which Claude Code names after the session id.
func sessionID(p Payload) string {
	if p.SessionID != "" {
		return p.SessionID
	}
	if p.TranscriptPath != "" {
		return strings.TrimSuffix(filepath.Base(p.TranscriptPath), ".jsonl")
	}
	return ""
}

// hunkSummary renders the diff hunks from the tool result as unified-diff
// headers. tool_response is unverified, so absence or any parse failure
// simply yields an empty hunk reference.
func hunkSummary(toolResponse json.RawMessage) string {
	if len(toolResponse) == 0 {
		return ""
	}
	var res toolUseResult
	if err := json.Unmarshal(toolResponse, &res); err != nil || len(res.StructuredPatch) == 0 {
		return ""
	}
	parts := make([]string, 0, len(res.StructuredPatch))
	for _, h := range res.StructuredPatch {
		parts = append(parts, fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines))
	}
	return strings.Join(parts, " ")
}

// relativize renders paths under the repo root (the hook's working directory)
// as repo-relative so ledger entries stay readable and stable across
// checkouts; paths outside the root stay verbatim.
func relativize(path, baseDir string) string {
	if baseDir == "" || !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// HooksConfig returns the hooks-map value "preledger install" merges into the
// project's .claude/settings.json, in the shape verified against real
// settings: hooks.<EventName>[] -> matcher groups -> command entries.
func HooksConfig(command string) map[string]any {
	return map[string]any{
		EventPostToolUse: []any{
			map[string]any{
				"matcher": Matcher,
				"hooks": []any{
					map[string]any{
						"type":          "command",
						"command":       command,
						"timeout":       10,
						"statusMessage": "Preledger 记录本次编辑的假设",
					},
				},
			},
		},
	}
}
