package client

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// SendOptions shape one message/send call.
type SendOptions struct {
	ContextID         string
	ReturnImmediately bool
}

// Send performs a non-streaming message/send.
func (c *Conn) Send(ctx context.Context, text string, opts SendOptions) (a2a.SendMessageResult, error) {
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	msg.ContextID = opts.ContextID
	req := &a2a.SendMessageRequest{Message: msg}
	if opts.ReturnImmediately {
		req.Config = &a2a.SendMessageConfig{ReturnImmediately: true}
	}
	return c.Client.SendMessage(ctx, req)
}

// GetTask fetches current task state.
func (c *Conn) GetTask(ctx context.Context, id string) (*a2a.Task, error) {
	return c.Client.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(id)})
}

// CancelTask requests cancellation and returns the server-supplied task
// state. Callers should not assume an observable intermediate "cancelling"
// state; confirm terminal state via polling.
func (c *Conn) CancelTask(ctx context.Context, id string) (*a2a.Task, error) {
	return c.Client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: a2a.TaskID(id)})
}
