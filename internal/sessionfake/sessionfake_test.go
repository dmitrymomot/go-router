package sessionfake_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router/internal/sessionfake"
	"github.com/dmitrymomot/go-router/session"
)

func TestStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		s := sessionfake.New()
		if tok, err := s.Save(ctx, "a", []byte("x"), time.Now().Add(time.Minute)); err != nil || tok != "a" {
			t.Fatalf("Save = %q, %v, want the token as it stands", tok, err)
		}
		if got, err := s.Load(ctx, "a"); err != nil || string(got) != "x" {
			t.Errorf("Load = %q, %v, want x", got, err)
		}
		time.Sleep(time.Minute)
		if _, err := s.Load(ctx, "a"); !errors.Is(err, session.ErrNotFound) {
			t.Errorf("Load of an expired record = %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "a"); err != nil || s.Len() != 0 {
			t.Errorf("Delete = %v and Len = %d, want nil and 0", err, s.Len())
		}
	})
}
