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

// WithStreaming makes the mock advertise and serve SendStreamingMessage and
// SubscribeToTask.
func WithStreaming() Option {
	return func(s *Server) { s.streaming = true }
}

// sendStreamingMessage implements the scripted SendStreamingMessage behaviors:
//
//	echo:<t> / default / busy: -> a single final Message event
//	task:<n>:<t>  -> Task (working), n status updates carrying tool-progress
//	                 extension metadata, then a completed status with <t>
//	fail:<t>      -> Task (working), then a failed status with <t>
//	input:<q>     -> Task (working), then an input-required status carrying
//	                 <q>; input-required legitimately ends the stream
//	authreq:<m>   -> like input: but pausing in auth-required
//	artifact:<t>  -> Task (working), two artifact-update chunks for "art_1"
//	                 (<t>, then an appending last chunk "-more"), then a
//	                 completed status; GetTask serves the full artifact
//	drop:<n>:<t>  -> Task (working), <n> working updates, then the iterator
//	                 errors without finalizing; ~2 further GetTask polls
//	                 complete the task with <t>
//	msgfail:      -> a final-looking Message, then Task (working), then a
//	                 failed status — adversarial ordering for exit-code truth
//
// A message with TaskID set continues that task (see continueTask), then
// streams the continued lifecycle to its next settle point.
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

		if req.Message.TaskID != "" {
			s.streamContinuation(ctx, req, yield)
			return
		}

		if rest, ok := strings.CutPrefix(text, "input:"); ok {
			s.streamPaused(ctx, req.Message, contextID, a2a.TaskStateInputRequired, rest, yield)
			return
		}
		if rest, ok := strings.CutPrefix(text, "authreq:"); ok {
			s.streamPaused(ctx, req.Message, contextID, a2a.TaskStateAuthRequired, rest, yield)
			return
		}
		if rest, ok := strings.CutPrefix(text, "artifact:"); ok {
			s.streamArtifact(ctx, req.Message, contextID, rest, yield)
			return
		}
		if rest, ok := strings.CutPrefix(text, "drop:"); ok {
			if n, finalText, ok := parseTaskScript(rest); ok {
				s.streamDrop(ctx, req.Message, contextID, n, finalText, yield)
				return
			}
		}
		if strings.HasPrefix(text, "msgfail:") {
			s.streamMsgFail(ctx, req.Message, contextID, yield)
			return
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
			if !sleepStream(ctx) {
				return
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

// streamContinuation applies a continuation message (TaskID set) and streams
// the continued lifecycle: the resumed snapshot, then its settle update.
func (s *Server) streamContinuation(ctx context.Context, req *a2a.SendMessageRequest, yield func(a2a.Event, error) bool) {
	res, err := s.sendMessage(req)
	if err != nil {
		yield(nil, err)
		return
	}
	t, ok := res.(*a2a.Task)
	if !ok {
		yield(res, nil)
		return
	}
	if !yield(t, nil) {
		return
	}
	if t.Status.State.Terminal() || paused(t.Status.State) {
		return
	}
	if !sleepStream(ctx) {
		return
	}
	yield(s.finalizeToUpdate(t.ID), nil)
}

// streamPaused emits Task(working) then one status update pausing the task
// in pauseState with the question — a legitimate stream end.
func (s *Server) streamPaused(ctx context.Context, msg *a2a.Message, contextID string, pauseState a2a.TaskState, question string, yield func(a2a.Event, error) bool) {
	task := s.newPausedTask(msg, contextID, pauseState, question)
	if !yield(task, nil) {
		return
	}
	if !sleepStream(ctx) {
		return
	}
	yield(s.finalizeToUpdate(task.ID), nil)
}

// streamArtifact emits Task(working), two artifact-update chunks for art_1,
// then the completed status update. GetTask serves the full artifact.
func (s *Server) streamArtifact(ctx context.Context, msg *a2a.Message, contextID, text string, yield func(a2a.Event, error) bool) {
	task := s.newArtifactTask(msg, contextID, text)
	if !yield(task, nil) {
		return
	}
	if !sleepStream(ctx) {
		return
	}
	first := &a2a.TaskArtifactUpdateEvent{
		TaskID:    task.ID,
		ContextID: task.ContextID,
		Artifact: &a2a.Artifact{
			ID:    "art_1",
			Name:  "result",
			Parts: a2a.ContentParts{a2a.NewTextPart(text)},
		},
	}
	if !yield(first, nil) {
		return
	}
	if !sleepStream(ctx) {
		return
	}
	second := &a2a.TaskArtifactUpdateEvent{
		TaskID:    task.ID,
		ContextID: task.ContextID,
		Append:    true,
		LastChunk: true,
		Artifact: &a2a.Artifact{
			ID:    "art_1",
			Name:  "result",
			Parts: a2a.ContentParts{a2a.NewTextPart("-more")},
		},
	}
	if !yield(second, nil) {
		return
	}
	yield(s.finalizeToUpdate(task.ID), nil)
}

// streamDrop emits Task(working) and n working updates, then fails the
// iterator WITHOUT finalizing the task: the client is expected to keep
// observing via GetTask, which completes the task after ~2 polls.
func (s *Server) streamDrop(ctx context.Context, msg *a2a.Message, contextID string, n int, finalText string, yield func(a2a.Event, error) bool) {
	task := s.newScriptedTask(msg, contextID, a2a.TaskStateWorking, 2, a2a.TaskStateCompleted, finalText)
	if !yield(task, nil) {
		return
	}
	for i := 1; i <= n; i++ {
		if !sleepStream(ctx) {
			return
		}
		update := &a2a.TaskStatusUpdateEvent{
			TaskID:    task.ID,
			ContextID: task.ContextID,
			Status: a2a.TaskStatus{
				State:   a2a.TaskStateWorking,
				Message: agentText(fmt.Sprintf("step %d/%d", i, n), task.ContextID),
			},
		}
		if !yield(update, nil) {
			return
		}
	}
	yield(nil, fmt.Errorf("simulated stream drop"))
}

// streamMsgFail emits a final-looking Message, then Task(working), then a
// failed status update: the adversarial ordering that must still exit 1.
func (s *Server) streamMsgFail(ctx context.Context, msg *a2a.Message, contextID string, yield func(a2a.Event, error) bool) {
	if !yield(agentText("looks done", contextID), nil) {
		return
	}
	if !sleepStream(ctx) {
		return
	}
	task := s.newScriptedTask(msg, contextID, a2a.TaskStateWorking, 1, a2a.TaskStateFailed, "it actually failed")
	if !yield(task, nil) {
		return
	}
	if !sleepStream(ctx) {
		return
	}
	yield(s.finalizeToUpdate(task.ID), nil)
}

// sleepStream paces scripted stream events; false means the context ended.
func sleepStream(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(streamStepDelay):
		return true
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

// finalizeToUpdate drives a scripted task to its next settle point (pause or
// terminal state) and renders it as the final status-update event. Tasks
// already paused or terminal are rendered as-is.
func (s *Server) finalizeToUpdate(id a2a.TaskID) *a2a.TaskStatusUpdateEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	mt := s.tasks[id]
	if !mt.task.Status.State.Terminal() && !paused(mt.task.Status.State) {
		mt.pollsRemaining = 0
		advanceTask(mt)
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
