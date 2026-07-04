package cli

import (
	"fmt"
	"os"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/andyjmorgan/a2acli/internal/client"
	"github.com/andyjmorgan/a2acli/internal/ledger"
	"github.com/andyjmorgan/a2acli/internal/normalize"
)

// ledgerEntry aliases the ledger type for command code.
type ledgerEntry = ledger.Entry

// ledgerList reads the local delegation ledger.
func ledgerList() ([]ledgerEntry, error) { return ledger.List() }

// recordTask notes a task observation in the local ledger. Best-effort: a
// ledger problem is a stderr note, never a command failure.
func recordTask(target string, conn *client.Conn, t *a2a.Task) {
	if t == nil {
		return
	}
	url := ""
	if conn != nil {
		url = conn.URL
	}
	err := ledger.Record(ledger.Entry{
		Target:    target,
		URL:       url,
		TaskID:    string(t.ID),
		ContextID: t.ContextID,
		State:     normalize.State(t.Status.State),
		Text:      normalize.TaskText(t),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: ledger write failed: %v\n", err)
	}
}

// recordResult records send results that carry a task handle.
func recordResult(target string, conn *client.Conn, res a2a.SendMessageResult) {
	if t, ok := res.(*a2a.Task); ok {
		recordTask(target, conn, t)
	}
}

// resolveTaskArgs supports both explicit and ledger-resolved task addressing:
//
//	<alias-or-url> <task-id>  — explicit
//	<task-id>                 — the agent is recalled from the local ledger
//	                            (unique task-id prefixes accepted)
func resolveTaskArgs(args []string) (target, taskID string, err error) {
	if len(args) == 2 {
		return args[0], args[1], nil
	}
	e, err := ledger.Find(args[0])
	if err != nil {
		return "", "", err
	}
	target = e.Target
	if target == "" {
		target = e.URL
	}
	return target, e.TaskID, nil
}
