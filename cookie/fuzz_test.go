package cookie

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func FuzzCodecRoundTripAndMutation(f *testing.F) {
	f.Add("uid", []byte("alice"))
	f.Add("flash", []byte(`[{"kind":"info","message":"saved"}]`))
	f.Add("", []byte(nil))

	old := bytes.Repeat([]byte("k"), MinKeyLen)
	cc := NewCodec(old)
	rotated := NewCodec(bytes.Repeat([]byte("n"), MinKeyLen), old)
	f.Fuzz(func(t *testing.T, name string, value []byte) {
		if len(name) > 1<<10 || len(value) > 16<<10 {
			t.Skip()
		}
		signed := cc.Encode(name, value, time.Now().Add(time.Hour))
		got, err := cc.Decode(name, signed)
		if err != nil {
			t.Fatalf("Decode(Encode()) = %v", err)
		}
		if !bytes.Equal(got, value) {
			t.Fatalf("Decode(Encode()) = %q, want %q", got, value)
		}
		got, err = rotated.Decode(name, signed)
		if err != nil {
			t.Fatalf("a rotated codec refused the previous key: %v", err)
		}
		if !bytes.Equal(got, value) {
			t.Fatalf("a rotated codec decoded %q, want %q", got, value)
		}

		firstDot := strings.IndexByte(signed, sep)
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
		if _, err := rotated.Decode(name, string(mutated)); err == nil {
			t.Fatalf("a rotated codec accepted a mutated expiry: %q", mutated)
		}
	})
}
