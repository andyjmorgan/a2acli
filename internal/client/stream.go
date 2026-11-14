package client

import (
	"context"
	"iter"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// SupportsStreaming reports whether the connected agent advertises the
// streaming capability on its card. Direct-endpoint connections (no card)
// report false; callers fall back to polling.
func (c *Conn) SupportsStreaming() bool {
	return c.Card != nil && c.Card.Capabilities.Streaming
}

// Stream performs a streaming message/stream call, yielding protocol events.
// No default deadline: streams are legitimately long-lived; the caller's
// context (Ctrl-C, --timeout) bounds them.
func (c *Conn) Stream(ctx context.Context, text string, opts SendOptions) iter.Seq2[a2a.Event, error] {
	opts.ReturnImmediately = false // meaningless for a stream
	return c.Client.SendStreamingMessage(ctx, buildRequest(text, opts))
}

// Subscribe re-attaches to a running task's event stream.
func (c *Conn) Subscribe(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
	return c.Client.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: a2a.TaskID(id)})
}
