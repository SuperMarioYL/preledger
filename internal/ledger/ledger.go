// Package ledger implements the assumption ledger: an append-only JSONL file
// at .preledger/ledger.jsonl with one record per event. Each record carries a
// full snapshot of an assumption; resolving statuses appends a new record, so
// the file stays append-only and diffable in git. Readers fold the file by
// assumption ID and keep the latest record per ID.
package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Assumption statuses. An entry starts open and is resolved from the review UI.
const (
	StatusOpen      = "open"
	StatusConfirmed = "confirmed"
	StatusRefuted   = "refuted"
)

// EditRef locates the agent edit an assumption was recorded against: the
// Claude Code session, its transcript file, the tool_use that performed the
// edit, the touched file, and the diff hunk when the tool result provides one.
type EditRef struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	ToolUseID      string `json:"tool_use_id,omitempty"`
	Tool           string `json:"tool,omitempty"`
	File           string `json:"file"`
	Hunk           string `json:"hunk,omitempty"`
}

// Refutation describes the test compiled from a refuted assumption (m3).
type Refutation struct {
	TestPath   string `json:"test_path"`
	TestName   string `json:"test_name"`
	LastResult string `json:"last_result,omitempty"`
}

// Assumption is one entry in the ledger. Basis carries the verbatim transcript
// quote that triggered the entry; Statement is the human-readable premise.
type Assumption struct {
	ID         string      `json:"id"`
	EditRef    EditRef     `json:"edit_ref"`
	Statement  string      `json:"statement"`
	Basis      string      `json:"basis"`
	Status     string      `json:"status"`
	Refutation *Refutation `json:"refutation,omitempty"`
}

// NewID derives the assumption ID from the plan: hash(edit_ref + statement).
// The hash is deterministic — no timestamps or randomness — so re-capturing
// the same edit event (e.g. re-reading a transcript) always yields the same
// ID and folds onto the existing entry instead of duplicating it.
func NewID(ref EditRef, statement string) string {
	canonical := strings.Join([]string{
		ref.SessionID, ref.ToolUseID, ref.Tool, ref.File, ref.Hunk, statement,
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:16]
}

// Store is the append-only JSONL ledger at a fixed path.
type Store struct {
	path string
}

// Open returns a Store backed by the JSONL file at path.
func Open(path string) *Store { return &Store{path: path} }

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// Append writes one record, creating the ledger directory on first write.
func (s *Store) Append(a Assumption) error {
	if a.ID == "" {
		return errors.New("assumption id is empty")
	}
	line, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode assumption %s: %w", a.ID, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create ledger dir: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append assumption %s: %w", a.ID, err)
	}
	return nil
}

// ReadAll returns every record in file order, duplicates included.
func (s *Store) ReadAll() ([]Assumption, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	var out []Assumption
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; sc.Scan(); line++ {
		var a Assumption
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			return out, fmt.Errorf("ledger line %d: %w", line, err)
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

// Current folds the ledger to its latest record per assumption ID, keeping
// first-appearance order. This is the view the review UI operates on.
func (s *Store) Current() ([]Assumption, error) {
	all, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	idx := make(map[string]int)
	out := make([]Assumption, 0, len(all))
	for _, a := range all {
		if i, ok := idx[a.ID]; ok {
			out[i] = a
			continue
		}
		idx[a.ID] = len(out)
		out = append(out, a)
	}
	return out, nil
}

// SetStatus appends a record that moves the assumption to the given status,
// preserving its refutation state. The record is the latest snapshot folded
// from the ledger, so no in-place edits ever happen.
func (s *Store) SetStatus(id, status string) error {
	switch status {
	case StatusOpen, StatusConfirmed, StatusRefuted:
	default:
		return fmt.Errorf("unknown status %q", status)
	}
	current, err := s.Current()
	if err != nil {
		return err
	}
	for _, a := range current {
		if a.ID != id {
			continue
		}
		a.Status = status
		return s.Append(a)
	}
	return fmt.Errorf("assumption %s not found in ledger", id)
}
