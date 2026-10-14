package i18n_test

import (
	"testing"
	"time"

	"github.com/dmitrymomot/go-router/i18n"
)

var refTS = time.Date(2026, 7, 17, 15, 4, 5, 0, time.UTC)

func TestDateTimeInvariant(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()

	// Invariant is ISO-8601: a neutral default, not a claim about English.
	if got, want := b.Date(en, refTS), "2026-07-17"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
	if got, want := b.Time(en, refTS), "15:04"; got != want {
		t.Errorf("Time = %q, want %q", got, want)
	}
	if got, want := b.DateTime(en, refTS), "2026-07-17 15:04"; got != want {
		t.Errorf("DateTime = %q, want %q", got, want)
	}
}

func TestDateTimeWiredSpec(t *testing.T) {
	b := fmtBundle(t)
	de := b.ParseOrDefault("de")

	if got, want := b.Date(de, refTS), "17.07.2026"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
	if got, want := b.Time(de, refTS), "15:04"; got != want {
		t.Errorf("Time = %q, want %q", got, want)
	}
	if got, want := b.DateTime(de, refTS), "17.07.2026 15:04"; got != want {
		t.Errorf("DateTime = %q, want %q", got, want)
	}
}

// TestPartialFormatSpecCompletes covers a partial FormatSpec: a caller who
// wires only the separators must still get real dates, not the empty string a
// zero DateLayout would feed time.AppendFormat. The empty layout fields fall
// back to Invariant while the separators the caller set are kept.
func TestPartialFormatSpecCompletes(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("de", i18n.FormatSpec{DecimalSep: ",", GroupSep: "."}))
	de := b.ParseOrDefault("de")
	ts := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)

	// Layouts were empty, so they default to Invariant: real output, never "".
	if got, want := b.Date(de, ts), "2026-01-02"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
	if got, want := b.Time(de, ts), "15:04"; got != want {
		t.Errorf("Time = %q, want %q", got, want)
	}
	if got, want := b.DateTime(de, ts), "2026-01-02 15:04"; got != want {
		t.Errorf("DateTime = %q, want %q", got, want)
	}
	// The separators the caller did wire are preserved.
	if got, want := b.Number(de, 1234.5), "1.234,5"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
}

// TestPartialFormatSpecEmptyDecimalSep covers the other completed field: an
// empty DecimalSep would fuse integer and fraction digits.
func TestPartialFormatSpecEmptyDecimalSep(t *testing.T) {
	b := newBundle(t, i18n.WithFormat("de", i18n.FormatSpec{GroupSep: "."}))
	if got, want := b.Number(b.ParseOrDefault("de"), 1234.5), "1.234.5"; got != want {
		t.Errorf("Number = %q, want %q", got, want)
	}
}

func TestDateTimeUsesTimesOwnLocation(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	kyiv := time.FixedZone("EEST", 3*3600)
	ts := time.Date(2026, 7, 17, 23, 30, 0, 0, time.UTC)

	// The package renders the time as given; converting is the caller's job.
	if got, want := b.Time(en, ts), "23:30"; got != want {
		t.Errorf("Time(UTC) = %q, want %q", got, want)
	}
	if got, want := b.Time(en, ts.In(kyiv)), "02:30"; got != want {
		t.Errorf("Time(Kyiv) = %q, want %q", got, want)
	}
	if got, want := b.Date(en, ts.In(kyiv)), "2026-07-18"; got != want {
		t.Errorf("Date(Kyiv) = %q, want %q", got, want)
	}
}

func TestDateTimeZeroLocale(t *testing.T) {
	b := fmtBundle(t)
	var zero i18n.Locale
	if got, want := b.Date(zero, refTS), "2026-07-17"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
}

// TestDateTimeZeroTime pins totality against the zero time.Time value.
func TestDateTimeZeroTime(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	var zero time.Time
	if got, want := b.Date(en, zero), "0001-01-01"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
	if got, want := b.Time(en, zero), "00:00"; got != want {
		t.Errorf("Time = %q, want %q", got, want)
	}
	if got, want := b.DateTime(en, zero), "0001-01-01 00:00"; got != want {
		t.Errorf("DateTime = %q, want %q", got, want)
	}
}

func TestAppendDateTimeAppends(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	if got, want := string(b.AppendDate([]byte("x:"), en, refTS)), "x:2026-07-17"; got != want {
		t.Errorf("AppendDate = %q, want %q", got, want)
	}
	if got, want := string(b.AppendTime([]byte("x:"), en, refTS)), "x:15:04"; got != want {
		t.Errorf("AppendTime = %q, want %q", got, want)
	}
	if got, want := string(b.AppendDateTime([]byte("x:"), en, refTS)), "x:2026-07-17 15:04"; got != want {
		t.Errorf("AppendDateTime = %q, want %q", got, want)
	}
}

func TestAppendDateTimeZeroAlloc(t *testing.T) {
	b := fmtBundle(t)
	en := b.Default()
	dst := make([]byte, 0, 64)
	tests := []struct {
		name string
		f    func()
	}{
		{"AppendDate", func() { _ = b.AppendDate(dst[:0], en, refTS) }},
		{"AppendTime", func() { _ = b.AppendTime(dst[:0], en, refTS) }},
		{"AppendDateTime", func() { _ = b.AppendDateTime(dst[:0], en, refTS) }},
	}
	for _, tc := range tests {
		if allocs := testing.AllocsPerRun(200, tc.f); allocs != 0 {
			t.Errorf("%s into a sized buffer allocated %v times, want 0", tc.name, allocs)
		}
	}
}
