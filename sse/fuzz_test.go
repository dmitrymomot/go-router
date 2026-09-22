package sse

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzDataLineRoundTrip(f *testing.F) {
	for _, seed := range []string{"", "one", "one\ntwo", "one\rtwo", "one\r\ntwo", "one\n", "one\n\n"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		var frame bytes.Buffer
		l := lines{buf: &frame, prefix: "data: "}
		l.WriteString(data)
		l.end()
		frame.WriteByte('\n')

		got := parseData(frame.String())
		want := strings.ReplaceAll(strings.ReplaceAll(data, "\r\n", "\n"), "\r", "\n")
		want = strings.TrimSuffix(want, "\n")
		if got != want {
			t.Fatalf("round trip = %q, want %q; frame = %q", got, want, frame.String())
		}
		if strings.ContainsRune(frame.String(), '\r') {
			t.Fatalf("frame contains a carriage return: %q", frame.String())
		}
	})
}

func parseData(frame string) string {
	var data strings.Builder
	for line := range strings.SplitSeq(frame, "\n") {
		value, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		data.WriteString(value)
		data.WriteByte('\n')
	}
	return strings.TrimSuffix(data.String(), "\n")
}
