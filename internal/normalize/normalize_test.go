package normalize

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestStateMapping(t *testing.T) {
	cases := map[a2a.TaskState]string{
		a2a.TaskStateWorking:   "working",
		a2a.TaskStateCompleted: "completed",
		a2a.TaskStateFailed:    "failed",
		a2a.TaskStateCanceled:  "cancelled",
		a2a.TaskStateSubmitted: "submitted",
	}
	for in, want := range cases {
		if got := State(in); got != want {
			t.Errorf("State(%q) = %q, want %q", in, got, want)
		}
	}
	if !Terminal("completed") || !Terminal("cancelled") || Terminal("working") {
		t.Error("Terminal misclassifies states")
	}
}

func TestSendResultMessage(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("hello"))
	msg.ContextID = "ctx_1"
	r := SendResult(msg)
	if r.Type != "message" || r.Text != "hello" || r.ContextID != "ctx_1" {
		t.Fatalf("got %+v", r)
	}
}

func TestSendResultTask(t *testing.T) {
	task := &a2a.Task{ID: "task_9", ContextID: "ctx_9", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}
	r := SendResult(task)
	if r.Type != "task" || r.TaskID != "task_9" || r.Status != "working" {
		t.Fatalf("got %+v", r)
	}
}

func TestSendResultBusyEnvelope(t *testing.T) {
	env := `{"status":"busy","error":"context_busy","contextId":"ctx_abc","activeTaskId":"task_123","message":"Context ctx_abc is busy with task task_123. Retry shortly."}`
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(env))
	r := SendResult(msg)
	if r.Type != "busy" || r.Error != "context_busy" || r.ActiveTaskID != "task_123" || r.ContextID != "ctx_abc" {
		t.Fatalf("got %+v", r)
	}
}

func TestBusyParsingIsStrict(t *testing.T) {
	for _, text := range []string{
		`{"status":"ok"}`,
		`{"foo":"bar"}`,
		`plain text`,
		`{invalid json`,
	} {
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(text))
		if r := SendResult(msg); r.Type != "message" {
			t.Errorf("text %q misdetected as %s", text, r.Type)
		}
	}
}

func TestTaskTextPrefersStatusMessage(t *testing.T) {
	task := &a2a.Task{
		ID: "t", ContextID: "c",
		Status: a2a.TaskStatus{
			State:   a2a.TaskStateCompleted,
			Message: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("final answer")),
		},
		History: []*a2a.Message{a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("older"))},
	}
	if got := TaskText(task); got != "final answer" {
		t.Fatalf("got %q", got)
	}
}

func TestTaskTextFallsBackToHistory(t *testing.T) {
	task := &a2a.Task{
		ID: "t", ContextID: "c",
		Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
		History: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("question")),
			a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("from history")),
		},
	}
	if got := TaskText(task); got != "from history" {
		t.Fatalf("got %q", got)
	}
}
