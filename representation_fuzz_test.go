package router

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func FuzzNegotiate(f *testing.F) {
	for _, seed := range []string{
		"",
		"*/*",
		"application/json",
		"text/html;q=0.9, application/json;q=0.5",
		"application/json;q=0, */*;q=1",
		"APPLICATION/JSON",
		"garbage",
	} {
		f.Add(seed)
	}

	offers := []string{MIMEApplicationJSON, MIMETextHTML, MIMETextPlain}
	f.Fuzz(func(t *testing.T, accept string) {
		if len(accept) > 8<<10 {
			t.Skip()
		}
		got := negotiate(accept, offers)
		if got == "" {
			return
		}
		found := false
		for _, offer := range offers {
			found = found || got == offer
		}
		if !found {
			t.Fatalf("negotiate(%q) returned an offer outside the input: %q", accept, got)
		}
		if strings.TrimSpace(accept) != "" && acceptQuality(accept, got) <= 0 {
			t.Fatalf("negotiate(%q) returned refused offer %q", accept, got)
		}
	})
}

func FuzzCookieCodecRoundTripAndMutation(f *testing.F) {
	f.Add("uid", []byte("alice"))
	f.Add("flash", []byte(`[{"kind":"info","message":"saved"}]`))
	f.Add("", []byte(nil))

	cc := NewCookieCodec(bytes.Repeat([]byte("k"), MinCookieKeyLen))
	f.Fuzz(func(t *testing.T, name string, value []byte) {
		if len(name) > 1<<10 || len(value) > 16<<10 {
			t.Skip()
		}
		signed := cc.encode(name, value, time.Now().Add(time.Hour).Unix())
		got, err := cc.Decode(name, signed)
		if err != nil {
			t.Fatalf("Decode(Encode()) = %v", err)
		}
		if !bytes.Equal(got, value) {
			t.Fatalf("Decode(Encode()) = %q, want %q", got, value)
		}

		firstDot := strings.IndexByte(signed, cookieSep)
		if firstDot < 0 || firstDot+1 >= len(signed) {
			t.Fatalf("encoded cookie has no expiry: %q", signed)
		}
		mutated := []byte(signed)
		if mutated[firstDot+1] == '9' {
			mutated[firstDot+1] = '8'
		} else {
			mutated[firstDot+1] = '9'
		}
		if _, err := cc.Decode(name, string(mutated)); err == nil {
			t.Fatalf("Decode accepted a mutated expiry: %q", mutated)
		}
	})
}

func FuzzSSEDataLineRoundTrip(f *testing.F) {
	for _, seed := range []string{"", "one", "one\ntwo", "one\rtwo", "one\r\ntwo", "one\n", "one\n\n"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		var frame bytes.Buffer
		lines := sseLines{buf: &frame, prefix: "data: "}
		lines.WriteString(data)
		lines.end()
		frame.WriteByte('\n')

		got := parseSSEData(frame.String())
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

func parseSSEData(frame string) string {
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
