// Package drain carries the stop signals of package serve to the request
// contexts, where package sse, package health and a hand-written handler read
// them. Package serve puts the channels in, and the others take them out.
package drain

import "context"

type (
	key         struct{}
	stoppingKey struct{}
)

// With returns ctx carrying done, which closes when the server starts to
// drain.
func With(ctx context.Context, done <-chan struct{}) context.Context {
	return context.WithValue(ctx, key{}, done)
}

// Done reports the channel that [With] put into ctx. It is nil when ctx
// carries none, so a select on it never fires.
func Done(ctx context.Context) <-chan struct{} {
	done, _ := ctx.Value(key{}).(<-chan struct{})
	return done
}

// WithStopping returns ctx carrying stopping, which closes when the server
// begins to stop, before its drain delay.
func WithStopping(ctx context.Context, stopping <-chan struct{}) context.Context {
	return context.WithValue(ctx, stoppingKey{}, stopping)
}

// Stopping reports the channel that [WithStopping] put into ctx, or nil.
func Stopping(ctx context.Context) <-chan struct{} {
	ch, _ := ctx.Value(stoppingKey{}).(<-chan struct{})
	return ch
}
