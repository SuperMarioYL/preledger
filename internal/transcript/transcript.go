// Package transcript reads Claude Code transcript JSONL files.
//
// Hook payloads reference the transcript by path and the edit by its
// tool_use id; the transcript is where the agent's own words live. Capture
// stays heuristic-first and works fully offline, but when the transcript file
// is available the extractor can quote the assistant text that preceded an
// edit instead of reconstructing context after the fact.
//
// The transcript schema is parsed leniently: only the two fields the capture
// path relies on (entry type, message content blocks) are read, unknown
// fields are ignored, and malformed lines are skipped. Schema smoke tests
// verified tool_name/tool_input on hook stdin; the remaining transcript
// fields are documented but unverified against stored samples, so nothing in
// this package may hard-fail on their absence or shape changes.
package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Entry is one folded assistant turn from a transcript file.
type Entry struct {
	// Type is the record type, e.g. "assistant" or "user". Only assistant
	// entries carry the premise-bearing prose, so Load keeps those and skips
	// the rest.
	Type string
	// Text concatenates the entry's text blocks with blank lines.
	Text string
	// ToolUseIDs lists the tool_use ids in the entry, in file order.
	ToolUseIDs []string
}

// contentBlock is the subset of a message content block this package reads.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	ID   string `json:"id"`
}

// envelope is the subset of a transcript JSONL line this package reads.
type envelope struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Load parses a transcript JSONL file and returns its assistant entries in
// file order. A missing file yields no entries and no error — capture must
// never block an edit because the transcript path was absent.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var env envelope
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
			continue // lenient: skip lines from other schema versions
		}
		if env.Type != "assistant" {
			continue
		}
		blocks, err := parseBlocks(env.Message.Content)
		if err != nil {
			continue
		}
		entry := Entry{Type: env.Type}
		var texts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_use":
				entry.ToolUseIDs = append(entry.ToolUseIDs, b.ID)
			}
		}
		if len(texts) == 0 && len(entry.ToolUseIDs) == 0 {
			continue
		}
		for i, t := range texts {
			if i > 0 {
				entry.Text += "\n\n"
			}
			entry.Text += t
		}
		out = append(out, entry)
	}
	return out, sc.Err()
}

// parseBlocks accepts either a bare content string or the usual block array.
func parseBlocks(raw json.RawMessage) ([]contentBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []contentBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// AssistantTextsBefore returns up to limit assistant texts that appear before
// the entry containing toolUseID — the context in which the edit was decided.
// When toolUseID is empty or not found (the hook may fire before the turn is
// flushed), it falls back to the last limit assistant texts. Entries without
// text are skipped. A missing transcript yields no texts and no error.
func AssistantTextsBefore(path, toolUseID string, limit int) ([]string, error) {
	entries, err := Load(path)
	if err != nil || limit <= 0 {
		return nil, err
	}
	texts := make([]string, 0, len(entries))
	cut := len(entries)
	for i, e := range entries {
		if toolUseID != "" && containsID(e.ToolUseIDs, toolUseID) {
			cut = i
			break
		}
	}
	for _, e := range entries[:cut] {
		if e.Text != "" {
			texts = append(texts, e.Text)
		}
	}
	if len(texts) > limit {
		texts = texts[len(texts)-limit:]
	}
	return texts, nil
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
