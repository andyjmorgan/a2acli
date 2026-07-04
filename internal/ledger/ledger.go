// Package ledger is a2acli's local memory of delegated work: an append-only
// JSONL record of every task handle the CLI has ever observed, per config
// dir. It exists for the orchestrating agent that loses its context between
// invocations — "what did I delegate, where, and what state was it in last
// time I looked" must survive the process.
//
// Writes are best-effort: a ledger failure must never break the command that
// observed the task.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andyjmorgan/a2acli/internal/config"
)

const fileName = "ledger.jsonl"

// maxText caps the stored last-known text snippet.
const maxText = 200

// Entry is one observation of a task.
type Entry struct {
	TS        string `json:"ts"`
	Target    string `json:"target"` // alias or URL as the user typed it
	URL       string `json:"url"`    // resolved base URL (survives alias removal)
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId,omitempty"`
	State     string `json:"state"`
	Text      string `json:"text,omitempty"` // last observed status/output snippet
}

// Path returns the ledger file location.
func Path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Record appends one observation. Best-effort by contract: callers ignore
// the error except for diagnostics.
func Record(e Entry) error {
	if e.TaskID == "" {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if len(e.Text) > maxText {
		e.Text = e.Text[:maxText] + "…"
	}
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// List returns the latest observation per task, newest first. A missing
// ledger is an empty list, not an error.
func List() ([]Entry, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	latest := make(map[string]Entry)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // tolerate a torn/corrupt line rather than losing the ledger
		}
		if e.TaskID != "" {
			latest[e.TaskID] = e
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(latest))
	for _, e := range latest {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	return out, nil
}

// Find resolves a task-id prefix to its ledger entry. It errors when the
// prefix is unknown or ambiguous, naming the candidates.
func Find(prefix string) (Entry, error) {
	entries, err := List()
	if err != nil {
		return Entry{}, err
	}
	var matches []Entry
	for _, e := range entries {
		if e.TaskID == prefix {
			return e, nil
		}
		if strings.HasPrefix(e.TaskID, prefix) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Entry{}, fmt.Errorf("task %q not found in local ledger (it records tasks this machine observed; pass <agent> <task-id> explicitly for others)", prefix)
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.TaskID)
		}
		return Entry{}, fmt.Errorf("task prefix %q is ambiguous: %s", prefix, strings.Join(ids, ", "))
	}
}
