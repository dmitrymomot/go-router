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
		read, swept := session.NewToken(), session.NewToken()
		table.Create("acme", "ann@example.com", read.Hash)
		table.Create("acme", "bob@example.com", swept.Hash)
		time.Sleep(sessionLifetime)

		// A Find of an expired row deletes it.
		if _, err := table.Find(ctx, read.Hash); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Find = %v, want ErrNotFound", err)
		}
		// A sweep after enough inserts deletes the rest.
		for range sweepEvery {
			table.Create("acme", "new@example.com", session.NewToken().Hash)
		}
		if _, ok := table.rows[string(swept.Hash)]; ok || len(table.rows) != sweepEvery {
			t.Errorf("the table holds %d rows, the expired one among them: %v", len(table.rows), ok)
		}
	})
}
