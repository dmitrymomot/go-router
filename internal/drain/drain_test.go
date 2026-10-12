package drain_test

import (
	"context"
	"testing"

	"github.com/dmitrymomot/go-router/internal/drain"
)

func TestDone(t *testing.T) {
	if drain.Done(context.Background()) != nil {
		t.Error("Done of a context without a drain channel is not nil")
	}
	ch := make(chan struct{})
	ctx := drain.With(context.Background(), ch)
	if drain.Done(ctx) != (<-chan struct{})(ch) {
		t.Error("Done does not report the channel With put in")
	}
}
