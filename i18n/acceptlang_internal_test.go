package i18n

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func tagsOf(got []langQ) []string {
	out := make([]string, len(got))
	for i, lq := range got {
		out[i] = lq.tag
	}
	return out
}

func TestParseAcceptLanguage(t *testing.T) {
	got := parseAcceptLanguage("es-ES,es;q=0.9,en;q=0.8")
	want := []langQ{{"es-ES", 1}, {"es", 0.9}, {"en", 0.8}}
	if len(got) != len(want) {
		t.Fatalf("parseAcceptLanguage returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].tag != want[i].tag || math.Abs(got[i].q-want[i].q) > 1e-9 {
			t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParseAcceptLanguageOrdering(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   []string
	}{
		{"implicit q=1 sorts first", "en;q=0.5,de;q=0.9,fr", []string{"fr", "de", "en"}},
		// Equal q must preserve header order so server preference can break the
		// tie deterministically (RFC 7231 5.3.1).
		{"equal q keeps header order", "de;q=0.8,fr;q=0.8,es;q=0.8", []string{"de", "fr", "es"}},
		{"uppercase Q is recognized", "en;Q=0.5", []string{"en"}},
		{"whitespace is tolerated", " en ; q=0.5 , fr ", []string{"fr", "en"}},
		{"wildcard", "*", []string{"*"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tagsOf(parseAcceptLanguage(tc.header))
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseAcceptLanguage(%q) tags = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestParseAcceptLanguageRejects(t *testing.T) {
	for _, in := range []string{
		// q=0 means "not acceptable"; out-of-range, unparseable and NaN q drop the tag.
		"en;q=0", "en;q=1.5", "en;q=-1", "en;q=abc", "en;q=NaN", "en;q=",
		// A parameter that is not q.
		"en;level=1",
		// Junk.
		"", ",,,", ";q=0.5", "-;q=1",
	} {
		if got := parseAcceptLanguage(in); len(got) != 0 {
			t.Errorf("parseAcceptLanguage(%q) = %v, want none", in, got)
		}
	}
}

func TestParseAcceptLanguageCaps(t *testing.T) {
	// Oversized headers are rejected outright, before any parsing work.
	if got := parseAcceptLanguage(strings.Repeat("en,", maxAcceptLangLen)); len(got) != 0 {
		t.Errorf("oversized header parsed %d entries, want 0", len(got))
	}

	// A header within the size cap but with too many tags is truncated, not
	// rejected: the client still gets its highest-priority choices.
	var sb strings.Builder
	for i := range maxAcceptLangTags + 20 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("en")
	}
	if sb.Len() >= maxAcceptLangLen {
		t.Fatalf("test header is %d bytes, want < %d", sb.Len(), maxAcceptLangLen)
	}
	if got := len(parseAcceptLanguage(sb.String())); got != maxAcceptLangTags {
		t.Errorf("parsed %d entries, want %d", got, maxAcceptLangTags)
	}
}

func TestParseAcceptLanguageIsTotal(t *testing.T) {
	// Must never panic on adversarial input.
	for _, in := range []string{"\x00", "\xff\xfe", ";;;;;", "=====", "q=q=q=q", strings.Repeat(";", 200), "en;q=0.999999999999999999999999999"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parseAcceptLanguage(%q) panicked: %v", in, r)
				}
			}()
			_ = parseAcceptLanguage(in)
		}()
	}
}

func FuzzParseAcceptLanguage(f *testing.F) {
	f.Add("es-ES,es;q=0.9,en;q=0.8")
	f.Add("*")
	f.Add(";q=")
	f.Add("en;q=NaN")
	f.Fuzz(func(t *testing.T, s string) {
		got := parseAcceptLanguage(s)
		if len(got) > maxAcceptLangTags {
			t.Errorf("parsed %d entries, want <= %d", len(got), maxAcceptLangTags)
		}
		for _, lq := range got {
			// Every surviving entry must have a sane q; NaN fails both bounds.
			if !(lq.q > 0 && lq.q <= 1) {
				t.Errorf("entry %v has q outside (0,1]", lq)
			}
		}
	})
}
