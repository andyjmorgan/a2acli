package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyjmorgan/a2acli/internal/mocka2a"
)

type streamEvent struct {
	TS        string         `json:"ts"`
	Event     string         `json:"event"`
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Status    string         `json:"status"`
	Text      string         `json:"text"`
	Metadata  map[string]any `json:"metadata"`
}

func parseJSONL(t *testing.T, s string) []streamEvent {
	t.Helper()
	var events []streamEvent
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("non-JSONL line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestStreamTaskLifecycle(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	out := filepath.Join(t.TempDir(), "stream.jsonl")
	r := c.mustRun("stream", srv.URL, "task:3:stream complete", "--out", out)
	events := parseJSONL(t, r.stdout)

	if len(events) < 5 { // task + 3 progress + terminal
		t.Fatalf("want >=5 events, got %d: %s", len(events), r.stdout)
	}
	if events[0].Event != "task" || events[0].Status != "working" || events[0].TaskID == "" {
		t.Fatalf("first event: %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Event != "status_update" || last.Status != "completed" || !strings.Contains(last.Text, "stream complete") {
		t.Fatalf("last event: %+v", last)
	}

	// --out mirrors stdout.
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(r.stdout) {
		t.Fatal("--out file differs from stdout")
	}
}

func TestStreamPreservesToolProgressExtensionMetadata(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("stream", srv.URL, "task:2:done")
	events := parseJSONL(t, r.stdout)

	found := 0
	for _, ev := range events {
		agentling, ok := ev.Metadata["agentling"].(map[string]any)
		if !ok {
			continue
		}
		found++
		if agentling["extension"] != mocka2a.ToolProgressExtensionURI {
			t.Fatalf("extension URI mangled: %v", agentling["extension"])
		}
		if agentling["tool_name"] != "mock_tool" || agentling["status"] != "EXECUTING" {
			t.Fatalf("tool progress payload mangled: %v", agentling)
		}
	}
	if found != 2 {
		t.Fatalf("want 2 tool-progress events preserved, got %d", found)
	}
}

func TestStreamImmediateMessage(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("stream", srv.URL, "echo:fast answer")
	events := parseJSONL(t, r.stdout)
	if len(events) != 1 || events[0].Event != "message" || !strings.Contains(events[0].Text, "fast answer") {
		t.Fatalf("events: %+v", events)
	}
}

func TestStreamFailedTaskExitsNonZero(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.run("stream", srv.URL, "fail:broke mid-stream")
	if r.code == 0 {
		t.Fatalf("failed stream should exit non-zero: %s", r.stdout)
	}
	events := parseJSONL(t, r.stdout)
	if events[len(events)-1].Status != "failed" {
		t.Fatalf("last event: %+v", events[len(events)-1])
	}
}

func TestStreamFallbackToPollingWhenUnsupported(t *testing.T) {
	srv := mocka2a.New().Start() // streaming NOT enabled
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("stream", srv.URL, "task:1:poll result", "--interval", "1")
	if !strings.Contains(r.stderr, "falling back") {
		t.Fatalf("expected fallback note on stderr: %s", r.stderr)
	}
	events := parseJSONL(t, r.stdout)
	last := events[len(events)-1]
	if last.Status != "completed" || !strings.Contains(last.Text, "poll result") {
		t.Fatalf("fallback events: %+v", events)
	}
}

func TestTaskSubscribeReattaches(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming()).Start()
	defer srv.Close()
	c := newCLI(t)

	// Create a long task non-streamingly, then subscribe to its stream.
	r := c.mustRun("send", srv.URL, "task:3:subscribed result", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.mustRun("task", "subscribe", srv.URL, id)
	events := parseJSONL(t, r.stdout)
	if events[0].Event != "task" || events[0].TaskID != id {
		t.Fatalf("first event: %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Status != "completed" || !strings.Contains(last.Text, "subscribed result") {
		t.Fatalf("last event: %+v", last)
	}
}

func TestTaskSubscribeFallbackPolling(t *testing.T) {
	srv := mocka2a.New().Start()
	defer srv.Close()
	c := newCLI(t)

	r := c.mustRun("send", srv.URL, "task:1:done polling", "--json")
	id := decode[sendResult](t, r.stdout).TaskID

	r = c.mustRun("task", "subscribe", srv.URL, id, "--interval", "1")
	if !strings.Contains(r.stderr, "falling back") {
		t.Fatalf("expected fallback note: %s", r.stderr)
	}
	events := parseJSONL(t, r.stdout)
	if events[len(events)-1].Status != "completed" {
		t.Fatalf("events: %+v", events)
	}
}

func TestStreamWithAuth(t *testing.T) {
	srv := mocka2a.New(mocka2a.WithStreaming(), mocka2a.WithAPIKey("stream-key")).Start()
	defer srv.Close()
	c := newCLI(t)

	c.mustRun("agent", "add", "mock", srv.URL, "--secret", "name:sk")
	c.setenv("A2ACLI_SECRET_SK=stream-key")
	r := c.mustRun("stream", "mock", "task:1:authed stream")
	events := parseJSONL(t, r.stdout)
	if events[len(events)-1].Status != "completed" {
		t.Fatalf("events: %+v", events)
	}
}
