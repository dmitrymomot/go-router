package htmx

import (
	"net/url"
	"strings"
	"testing"
)

func FuzzTargetID(f *testing.F) {
	for _, seed := range []string{"", "list", "row 7", "a#b", "кл", "50%", "a+b"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		if got := (Request{Target: "div#" + url.PathEscape(s)}).TargetID(); got != s {
			t.Errorf("TargetID() of the escaped %q = %q", s, got)
		}

		// s as a raw header, as a client could send it.
		id := (Request{Target: s}).TargetID()
		_, after, ok := strings.Cut(s, "#")
		if !ok && id != "" {
			t.Errorf("TargetID() of %q = %q, want \"\" without a '#'", s, id)
		}
		if len(id) > len(after) {
			t.Errorf("TargetID() of %q = %q, longer than what follows the '#'", s, id)
		}
	})
}
