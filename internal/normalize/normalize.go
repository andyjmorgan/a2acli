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
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Status    string         `json:"status"`
	Text      string         `json:"text,omitempty"` // final/status message text if present
	Artifacts []ArtifactView `json:"artifacts,omitempty"`
}

// ArtifactView is the normalized shape of one task artifact. Text parts are
// concatenated; file/data parts are described (never silently dropped) and
// downloadable via `task artifacts --save`.
type ArtifactView struct {
	ID          string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Text        string         `json:"text,omitempty"`
	Files       []FileView     `json:"files,omitempty"`
	DataParts   int            `json:"dataParts,omitempty"` // structured-data parts (fetch via --save)
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// FileView describes one non-text artifact part.
type FileView struct {
	Name      string `json:"name,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	SizeBytes int    `json:"sizeBytes,omitempty"` // inline bytes only
	URI       string `json:"uri,omitempty"`       // URL parts only
}

// Artifacts normalizes a task's artifacts.
func Artifacts(arts []*a2a.Artifact) []ArtifactView {
	if len(arts) == 0 {
		return nil
	}
	out := make([]ArtifactView, 0, len(arts))
	for _, a := range arts {
		if a == nil {
			continue
		}
		out = append(out, ArtifactValue(a))
	}
	return out
}

// ArtifactValue normalizes one artifact.
func ArtifactValue(a *a2a.Artifact) ArtifactView {
	v := ArtifactView{
		ID:          string(a.ID),
		Name:        a.Name,
		Description: a.Description,
		Metadata:    a.Metadata,
	}
	var text strings.Builder
	for _, p := range a.Parts {
		if p == nil {
			continue
		}
		switch c := p.Content.(type) {
		case a2a.Text:
			text.WriteString(string(c))
		case a2a.Raw:
			v.Files = append(v.Files, FileView{Name: p.Filename, MediaType: p.MediaType, SizeBytes: len(c)})
		case a2a.URL:
			v.Files = append(v.Files, FileView{Name: p.Filename, MediaType: p.MediaType, URI: string(c)})
		case a2a.Data:
			v.DataParts++
		}
	}
	v.Text = Truncate(text.String())
	return v
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

// ParseState maps a CLI state name back to the SDK task state; ok=false for
// unknown names. Accepts "canceled" as well as the CLI's "cancelled".
func ParseState(name string) (a2a.TaskState, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "submitted":
		return a2a.TaskStateSubmitted, true
	case "working":
		return a2a.TaskStateWorking, true
	case "completed":
		return a2a.TaskStateCompleted, true
	case "failed":
		return a2a.TaskStateFailed, true
	case "cancelled", "canceled":
		return a2a.TaskStateCanceled, true
	case "input-required":
		return a2a.TaskStateInputRequired, true
	case "auth-required":
		return a2a.TaskStateAuthRequired, true
	case "rejected":
		return a2a.TaskStateRejected, true
	}
	return "", false
}

// TaskPage is the normalized result of a remote ListTasks call.
type TaskPage struct {
	Tasks         []TaskView `json:"tasks"`
	TotalSize     int        `json:"totalSize"`
	NextPageToken string     `json:"nextPageToken,omitempty"`
}

// Terminal reports whether a normalized state name is terminal.
func Terminal(state string) bool {
	switch state {
	case "completed", "failed", "cancelled", "rejected":
		return true
	}
	return false
}

// Paused reports whether a task is waiting on the caller: the remote agent
// asked a question (input-required) or needs credentials (auth-required).
// Continue it with `send --task-id`.
func Paused(state string) bool {
	return state == "input-required" || state == "auth-required"
}

// MaxText, when > 0, caps every normalized Text field. It exists for AI
// agents whose context window pays for every output byte: `--max-text 500`
// keeps event lines cheap; the full text stays one `task get` away.
var MaxText int

const truncationMark = "…[truncated: full text via task get]"

// Truncate applies the MaxText cap.
func Truncate(s string) string {
	if MaxText <= 0 || len(s) <= MaxText {
		return s
	}
	return s[:MaxText] + truncationMark
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

// SendResult normalizes a SendMessage outcome.
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
		return Result{Type: "message", Text: Truncate(text), ContextID: v.ContextID}
	case *a2a.Task:
		return Result{
			Type:      "task",
			TaskID:    string(v.ID),
			ContextID: v.ContextID,
			Status:    State(v.Status.State),
			Text:      Truncate(TaskText(v)),
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
		Text:      Truncate(TaskText(t)),
		Artifacts: Artifacts(t.Artifacts),
	}
}

// StreamEvent is one JSONL line of `stream` / `task subscribe` output. The
// shape is a superset of Snapshot so consumers can treat both uniformly.
// Metadata preserves extension payloads (e.g. Agentlings tool-progress)
// verbatim.
type StreamEvent struct {
	TS        string         `json:"ts"`
	Event     string         `json:"event"` // "message" | "task" | "status_update" | "artifact_update" | "busy"
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	Status    string         `json:"status,omitempty"`
	Text      string         `json:"text,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`

	// Artifact-update fields.
	ArtifactID   string     `json:"artifactId,omitempty"`
	ArtifactName string     `json:"artifactName,omitempty"`
	Append       bool       `json:"append,omitempty"`
	LastChunk    bool       `json:"lastChunk,omitempty"`
	Files        []FileView `json:"files,omitempty"`

	// Busy-envelope fields (same detection as send).
	Error        string `json:"error,omitempty"`
	ActiveTaskID string `json:"activeTaskId,omitempty"`
}

// Stream normalizes one streaming protocol event.
func Stream(ev a2a.Event, now time.Time) StreamEvent {
	out := StreamEvent{TS: now.UTC().Format(time.RFC3339)}
	switch v := ev.(type) {
	case *a2a.Message:
		text := MessageText(v)
		if env, ok := parseBusy(text); ok {
			out.Event = "busy"
			out.Status = "busy"
			out.Error = env.Error
			out.ContextID = env.ContextID
			out.ActiveTaskID = env.ActiveTaskID
			out.Text = env.Message
			return out
		}
		out.Event = "message"
		out.Text = Truncate(text)
		out.ContextID = v.ContextID
		out.Metadata = v.Metadata
	case *a2a.Task:
		out.Event = "task"
		out.TaskID = string(v.ID)
		out.ContextID = v.ContextID
		out.Status = State(v.Status.State)
		out.Text = Truncate(TaskText(v))
		out.Metadata = v.Metadata
	case *a2a.TaskStatusUpdateEvent:
		out.Event = "status_update"
		out.TaskID = string(v.TaskID)
		out.ContextID = v.ContextID
		out.Status = State(v.Status.State)
		out.Text = Truncate(MessageText(v.Status.Message))
		out.Metadata = v.Metadata
	case *a2a.TaskArtifactUpdateEvent:
		out.Event = "artifact_update"
		out.TaskID = string(v.TaskID)
		out.ContextID = v.ContextID
		out.Metadata = v.Metadata
		out.Append = v.Append
		out.LastChunk = v.LastChunk
		if v.Artifact != nil {
			av := ArtifactValue(v.Artifact)
			out.ArtifactID = av.ID
			out.ArtifactName = av.Name
			out.Text = av.Text
			out.Files = av.Files
		}
	default:
		out.Event = "unknown"
	}
	return out
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
		Message:        v.Text, // already truncated by Task
	}
}
