package mocka2a

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// ToolProgressExtensionURI is the Agentlings tool-progress extension carried
// in streamed status-update metadata; the mock reproduces its shape so
// clients can test extension passthrough.
const ToolProgressExtensionURI = "https://donkeywork.dev/a2a/extensions/tool-progress/v1"

// streamStepDelay paces scripted stream events so tests observe ordering
// without meaningful wall-clock cost.
const streamStepDelay = 15 * time.Millisecond

// WithStreaming makes the mock advertise and serve message/stream and
// tasks/subscribe.
func WithStreaming() Option {
	return func(s *Server) { s.streaming = true }
}

// sendStreamingMessage implements the scripted message/stream behaviors:
//
//	echo:<t> / default / busy: -> a single final Message event
//	task:<n>:<t>  -> Task (working), n status updates carrying tool-progress
//	                 extension metadata, then a completed status with <t>
//	fail:<t>      -> Task (working), then a failed status with <t>
func (s *Server) sendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	if !s.streaming {
		return unsupportedStream
	}
	return func(yield func(a2a.Event, error) bool) {
		if req == nil || req.Message == nil || len(req.Message.Parts) == 0 {
			yield(nil, fmt.Errorf("message with parts is required: %w", a2a.ErrInvalidParams))
			return
		}
		text := messageText(req.Message)
		contextID := req.Message.ContextID
		if contextID == "" {
			contextID = a2a.NewContextID()
		}

		var polls int
		var finalText string
		var isTask bool
		finalState := a2a.TaskStateCompleted
		if rest, ok := strings.CutPrefix(text, "task:"); ok {
			if n, t, ok := parseTaskScript(rest); ok {
				polls, finalText, isTask = n, t, true
			}
		}
		if rest, ok := strings.CutPrefix(text, "fail:"); ok {
			polls, finalText, isTask = 1, rest, true
			finalState = a2a.TaskStateFailed
		}

		if !isTask {
			// Immediate-message behaviors stream as one final Message event.
			res, err := s.sendMessage(req)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(res, nil)
			return
		}

		task := s.newScriptedTask(req.Message, contextID, a2a.TaskStateWorking, polls, finalState, finalText)
		if !yield(task, nil) {
			return
		}

		for i := 1; i <= polls; i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(streamStepDelay):
			}
			update := &a2a.TaskStatusUpdateEvent{
				TaskID:    task.ID,
				ContextID: task.ContextID,
				Status: a2a.TaskStatus{
					State:   a2a.TaskStateWorking,
					Message: agentText(fmt.Sprintf("step %d/%d", i, polls), task.ContextID),
				},
				Metadata: map[string]any{
					"agentling": map[string]any{
						"extension":    ToolProgressExtensionURI,
						"tool_call_id": fmt.Sprintf("toolu_mock_%d", i),
						"status":       "EXECUTING",
						"tool_name":    "mock_tool",
						"description":  fmt.Sprintf("Mock tool step %d of %d", i, polls),
					},
				},
			}
			if !yield(update, nil) {
				return
			}
		}

		yield(s.finalizeToUpdate(task.ID), nil)
	}
}

// subscribeToTask replays a scripted task's remaining lifecycle as stream
// events: current snapshot, remaining progress updates, terminal status.
func (s *Server) subscribeToTask(ctx context.Context, req *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	if !s.streaming {
		return unsupportedStream
	}
	return func(yield func(a2a.Event, error) bool) {
		s.mu.Lock()
		mt, ok := s.tasks[req.ID]
		var snapshot *a2a.Task
		var terminal bool
		var remaining int
		if ok {
			snapshot = snapshotTask(mt.task)
			terminal = mt.task.Status.State.Terminal()
			remaining = mt.pollsRemaining
		}
		s.mu.Unlock()
		if !ok {
			yield(nil, a2a.ErrTaskNotFound)
			return
		}

		if !yield(snapshot, nil) || terminal {
			return
		}

		for i := 1; i <= remaining; i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(streamStepDelay):
			}
			update := &a2a.TaskStatusUpdateEvent{
				TaskID:    snapshot.ID,
				ContextID: snapshot.ContextID,
				Status: a2a.TaskStatus{
					State:   a2a.TaskStateWorking,
					Message: agentText(fmt.Sprintf("step %d/%d", i, remaining), snapshot.ContextID),
				},
			}
			if !yield(update, nil) {
				return
			}
		}

		yield(s.finalizeToUpdate(snapshot.ID), nil)
	}
}

// finalizeToUpdate drives a scripted task to its terminal state and renders
// it as the final status-update event.
func (s *Server) finalizeToUpdate(id a2a.TaskID) *a2a.TaskStatusUpdateEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	mt := s.tasks[id]
	if !mt.task.Status.State.Terminal() {
		mt.pollsRemaining = 0
		finalizeTask(mt)
	}
	return &a2a.TaskStatusUpdateEvent{
		TaskID:    mt.task.ID,
		ContextID: mt.task.ContextID,
		Status:    mt.task.Status,
	}
}

func agentText(text, contextID string) *a2a.Message {
	m := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(text))
	m.ContextID = contextID
	return m
}
