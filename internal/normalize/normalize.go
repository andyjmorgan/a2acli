// Package normalize defines the stable output shapes of a2acli. These shapes
// are the CLI's contract with scripts and AI harnesses: they must stay stable
// even if the SDK's types churn.
package normalize

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Result is the normalized outcome of a send: exactly one of the three types.
type Result struct {
	Type      string `json:"type"` // "message" | "task" | "busy"
	Text      string `json:"text,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	ContextID string `json:"contextId,omitempty"`
	Status    string `json:"status,omitempty"`

	// Busy fields (Agentlings context_busy envelope; not generic A2A).
	Error        string `json:"error,omitempty"`
	ActiveTaskID string `json:"activeTaskId,omitempty"`
	Message      string `json:"message,omitempty"`
}

// TaskView is the normalized task object for `task get --json` and JSONL
// snapshots.
type TaskView struct {
	TaskID    string `json:"taskId"`
	ContextID string `json:"contextId"`
	Status    string `json:"status"`
	Text      string `json:"text,omitempty"` // final/status message text if present
}

// Snapshot is one JSONL line of `task watch --out`.
type Snapshot struct {
	TS             string `json:"ts"`
	Event          string `json:"event"`
	TaskID         string `json:"taskId"`
	ContextID      string `json:"contextId"`
	Status         string `json:"status"`
	ElapsedSeconds int64  `json:"elapsedSeconds"`
	Message        string `json:"message,omitempty"`
}

// State maps SDK task states to the CLI's stable lowercase names.
func State(s a2a.TaskState) string {
	switch s {
	case a2a.TaskStateSubmitted:
		return "submitted"
	case a2a.TaskStateWorking:
		return "working"
	case a2a.TaskStateCompleted:
		return "completed"
	case a2a.TaskStateFailed:
		return "failed"
	case a2a.TaskStateCanceled:
		return "cancelled"
	case a2a.TaskStateInputRequired:
		return "input-required"
	case a2a.TaskStateAuthRequired:
		return "auth-required"
	case a2a.TaskStateRejected:
		return "rejected"
	default:
		if s == "" {
			return "unknown"
		}
		return strings.ToLower(strings.TrimPrefix(string(s), "TASK_STATE_"))
	}
}

// Terminal reports whether a normalized state name is terminal.
func Terminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled", "rejected":
		return true
	}
	return false
}

// MessageText concatenates the text parts of a message.
func MessageText(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range m.Parts {
		if t, ok := p.Content.(a2a.Text); ok {
			b.WriteString(string(t))
		}
	}
	return b.String()
}

// TaskText extracts the most useful text from a task: its status message,
// else the last agent message in history.
func TaskText(t *a2a.Task) string {
	if t == nil {
		return ""
	}
	if s := MessageText(t.Status.Message); s != "" {
		return s
	}
	for i := len(t.History) - 1; i >= 0; i-- {
		if t.History[i].Role == a2a.MessageRoleAgent {
			if s := MessageText(t.History[i]); s != "" {
				return s
			}
		}
	}
	return ""
}

// busyEnvelope is the Agentlings context-contention affordance: a normal A2A
// text message whose body is this JSON. Not generic A2A semantics.
type busyEnvelope struct {
	Status       string `json:"status"`
	Error        string `json:"error"`
	ContextID    string `json:"contextId"`
	ActiveTaskID string `json:"activeTaskId"`
	Message      string `json:"message"`
}

// parseBusy best-effort detects the envelope.
func parseBusy(text string) (busyEnvelope, bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "{") {
		return busyEnvelope{}, false
	}
	var env busyEnvelope
	if err := json.Unmarshal([]byte(t), &env); err != nil {
		return busyEnvelope{}, false
	}
	if env.Status != "busy" || env.Error == "" {
		return busyEnvelope{}, false
	}
	return env, true
}

// SendResult normalizes a message/send outcome.
func SendResult(res a2a.SendMessageResult) Result {
	switch v := res.(type) {
	case *a2a.Message:
		text := MessageText(v)
		if env, ok := parseBusy(text); ok {
			return Result{
				Type:         "busy",
				Error:        env.Error,
				ContextID:    env.ContextID,
				ActiveTaskID: env.ActiveTaskID,
				Message:      env.Message,
			}
		}
		return Result{Type: "message", Text: text, ContextID: v.ContextID}
	case *a2a.Task:
		return Result{
			Type:      "task",
			TaskID:    string(v.ID),
			ContextID: v.ContextID,
			Status:    State(v.Status.State),
			Text:      TaskText(v),
		}
	default:
		return Result{Type: "message", Text: ""}
	}
}

// Task normalizes a full task.
func Task(t *a2a.Task) TaskView {
	return TaskView{
		TaskID:    string(t.ID),
		ContextID: t.ContextID,
		Status:    State(t.Status.State),
		Text:      TaskText(t),
	}
}

// TaskSnapshot builds a JSONL snapshot line.
func TaskSnapshot(t *a2a.Task, started time.Time, now time.Time) Snapshot {
	v := Task(t)
	return Snapshot{
		TS:             now.UTC().Format(time.RFC3339),
		Event:          "task_snapshot",
		TaskID:         v.TaskID,
		ContextID:      v.ContextID,
		Status:         v.Status,
		ElapsedSeconds: int64(now.Sub(started).Seconds()),
		Message:        v.Text,
	}
}
