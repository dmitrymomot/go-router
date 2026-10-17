package useragent_test

import (
	"fmt"
	"testing"
)

// message renders the optional message of a check: a value alone, or a
// format and its arguments.
func message(msg []any) string {
	switch len(msg) {
	case 0:
		return ""
	case 1:
		return ": " + fmt.Sprint(msg[0])
	}
	format, _ := msg[0].(string)
	return ": " + fmt.Sprintf(format, msg[1:]...)
}

func equal[T comparable](t testing.TB, want, got T, msg ...any) {
	t.Helper()
	if got != want {
		t.Errorf("got %#v, want %#v%s", got, want, message(msg))
	}
}

func notEqual[T comparable](t testing.TB, unwanted, got T, msg ...any) {
	t.Helper()
	if got == unwanted {
		t.Errorf("got %#v, want anything else%s", got, message(msg))
	}
}

func isTrue(t testing.TB, ok bool, msg ...any) {
	t.Helper()
	if !ok {
		t.Errorf("got false, want true%s", message(msg))
	}
}

func isFalse(t testing.TB, ok bool, msg ...any) {
	t.Helper()
	if ok {
		t.Errorf("got true, want false%s", message(msg))
	}
}

func mustTrue(t testing.TB, ok bool, msg ...any) {
	t.Helper()
	if !ok {
		t.Fatalf("got false, want true%s", message(msg))
	}
}
