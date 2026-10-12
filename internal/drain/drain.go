// Package drain carries the drain signal of package serve to the request
// contexts, where package sse and a hand-written handler read it. Package
// serve puts the channel in, and the others take it out.
package drain

import "context"

type key struct{}

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
