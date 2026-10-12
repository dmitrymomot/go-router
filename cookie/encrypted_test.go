package cookie

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

var testAEADKey = bytes.Repeat([]byte("e"), KeyLen)

func encryptedCodec() *Codec { return NewEncryptedCodec(testAEADKey) }

func TestEncryptedCodecRoundTripsAndHidesTheValue(t *testing.T) {
	cc := encryptedCodec()
	for _, value := range [][]byte{nil, []byte("ann"), []byte("secret value, with spaces"), bytes.Repeat([]byte{0, 255}, 100)} {
		got := encode(cc, "session", value)
		if len(value) >= 5 && strings.Contains(got, string(value)) {
			t.Errorf("the encrypted cookie holds the value in the clear: %q", got)
		}
		back, err := cc.Decode("session", got)
		if err != nil || !bytes.Equal(back, value) {
			t.Errorf("Decode = %q, %v, want %q", back, err, value)
		}
	}
}

func TestEncryptedCodecUsesAFreshNonce(t *testing.T) {
	cc := encryptedCodec()
	expires := time.Now().Add(time.Hour)
	if a, b := cc.Encode("n", []byte("v"), expires), cc.Encode("n", []byte("v"), expires); a == b {
		t.Error("two encryptions of one value are the same string")
	}
}

func TestEncryptedCodecRejectsATamperedValue(t *testing.T) {
	cc := encryptedCodec()
	good := encode(cc, "session", []byte("ann"))
	expiry, sealed, _ := strings.Cut(good, ".")

	flipped := []byte(sealed)
	if flipped[len(flipped)/2] == 'A' {
		flipped[len(flipped)/2] = 'B'
	} else {
		flipped[len(flipped)/2] = 'A'
	}
	for name, bad := range map[string]string{
		"flipped ciphertext": expiry + "." + string(flipped),
		"truncated":          expiry + "." + sealed[:10],
		"another expiry":     "9999999999." + sealed,
		"no separator":       sealed,
		"not base64":         expiry + ".!!!",
		"bad expiry":         "x." + sealed,
		"empty":              "",
		"a signed value":     encode(testCodec(), "session", []byte("ann")),
	} {
		if _, err := cc.Decode("session", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Decode = %v, want ErrInvalid", name, err)
		}
	}
}

func TestEncryptedCodecRejectsAnotherName(t *testing.T) {
	cc := encryptedCodec()
	if _, err := cc.Decode("other", encode(cc, "session", []byte("ann"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("Decode under another name = %v, want ErrInvalid", err)
	}
}

func TestEncryptedCodecReportsAnExpiredValue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc := encryptedCodec()
		got := cc.Encode("session", []byte("ann"), time.Now().Add(time.Minute))
		time.Sleep(2 * time.Minute)
		if _, err := cc.Decode("session", got); !errors.Is(err, ErrExpired) {
			t.Errorf("Decode = %v, want ErrExpired", err)
		}
	})
}

func TestEncryptedCodecRotatesKeys(t *testing.T) {
	oldKey, newKey := bytes.Repeat([]byte("o"), KeyLen), bytes.Repeat([]byte("n"), KeyLen)
	before := encode(NewEncryptedCodec(oldKey), "session", []byte("ann"))

	rotated := NewEncryptedCodec(newKey, oldKey)
	if v, err := rotated.Decode("session", before); err != nil || string(v) != "ann" {
		t.Errorf("a value of the old key = %q, %v, want ann", v, err)
	}
	after := encode(rotated, "session", []byte("ann"))
	if _, err := NewEncryptedCodec(oldKey).Decode("session", after); !errors.Is(err, ErrInvalid) {
		t.Errorf("the old key opened a value of the new one: %v", err)
	}
	if _, err := NewEncryptedCodec(newKey).Decode("session", after); err != nil {
		t.Errorf("the new key refused its own value: %v", err)
	}
}

func TestNewEncryptedCodecPanicsOnABadKey(t *testing.T) {
	for name, build := range map[string]func(){
		"short":          func() { NewEncryptedCodec(make([]byte, KeyLen-1)) },
		"long":           func() { NewEncryptedCodec(make([]byte, KeyLen+1)) },
		"nil":            func() { NewEncryptedCodec(nil) },
		"previous short": func() { NewEncryptedCodec(testAEADKey, make([]byte, 16)) },
	} {
		func() {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, "NewEncryptedCodec") || !strings.Contains(msg, "32") {
					t.Errorf("%s: panic = %q, want a message naming NewEncryptedCodec and 32 bytes", name, msg)
				}
			}()
			build()
		}()
	}
}

func TestNewEncryptedCodecCopiesTheKey(t *testing.T) {
	key := bytes.Clone(testAEADKey)
	cc := NewEncryptedCodec(key)
	got := encode(cc, "session", []byte("ann"))
	clear(key)
	if _, err := cc.Decode("session", got); err != nil {
		t.Errorf("clearing the caller key broke the codec: %v", err)
	}
}

func TestEncryptedSetAndGetRoundTrip(t *testing.T) {
	cc := encryptedCodec()
	b := newBase()
	if err := cc.Set(b, b.NewCookie("session", "ann", time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookies := setCookies(t, b)
	if len(cookies) != 1 || strings.Contains(cookies[0].Value, "ann") {
		t.Fatalf("cookies = %+v, want one that hides the value", cookies)
	}
	got, err := cc.Get(newBase(&http.Cookie{Name: "session", Value: cookies[0].Value}), "session")
	if err != nil || got != "ann" {
		t.Errorf("Get = %q, %v, want ann", got, err)
	}
}

func TestEncryptedSetRefusesACookieTooLarge(t *testing.T) {
	cc := encryptedCodec()
	b := newBase()
	err := cc.Set(b, b.NewCookie("session", strings.Repeat("a", MaxSize), time.Hour))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Set = %v, want ErrTooLarge", err)
	}
	if got := setCookies(t, b); len(got) != 0 {
		t.Errorf("a refused cookie was written: %+v", got)
	}
}

func TestEncryptedCodecCarriesFlashes(t *testing.T) {
	cc := encryptedCodec()
	added := newBase()
	want := []Flash{{Kind: "success", Message: "saved"}, {Kind: "error", Message: "but not that"}}
	for _, f := range want {
		if err := cc.AddFlash(added, f); err != nil {
			t.Fatal(err)
		}
	}
	cookies := setCookies(t, added)
	if len(cookies) != 1 || strings.Contains(cookies[0].Value, "saved") {
		t.Fatalf("flash cookies = %+v, want one that hides the messages", cookies)
	}

	got := cc.Flashes(newBase(&http.Cookie{Name: FlashName, Value: cookies[0].Value}))
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Flashes = %+v, want %+v", got, want)
	}

	// A flash cookie of another key reads as no flash at all.
	other := NewEncryptedCodec(bytes.Repeat([]byte("z"), KeyLen))
	if got := other.Flashes(newBase(&http.Cookie{Name: FlashName, Value: cookies[0].Value})); len(got) != 0 {
		t.Errorf("another key read the flashes: %+v", got)
	}
}
