package main

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmitrymomot/go-router/session"
)

func TestTheSessionTableDropsExpiredRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		table := NewSessionTable()
		for _, tok := range []string{"read", "swept"} {
			if _, err := table.Save(ctx, tok, []byte("x"), time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Minute)

		// A Load of an expired row deletes it.
		if _, err := table.Load(ctx, "read"); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Load = %v, want ErrNotFound", err)
		}
		// A sweep after enough saves deletes the rest.
		for range sweepEvery {
			if _, err := table.Save(ctx, session.NewToken(), nil, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok := table.rows["swept"]; ok || len(table.rows) != sweepEvery {
			t.Errorf("the table holds %d rows, the expired one among them: %v", len(table.rows), ok)
		}
	})
}
