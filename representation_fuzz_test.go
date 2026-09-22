package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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

func FuzzParseBool(f *testing.F) {
	for _, seed := range []string{"on", "off", "true", "1", "yes", ""} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := parseBool(s)
		switch s {
		case "on", "On", "ON":
			if err != nil || !got {
				t.Fatalf("parseBool(%q) = %v, %v, want true", s, got, err)
			}
			return
		case "off", "Off", "OFF":
			if err != nil || got {
				t.Fatalf("parseBool(%q) = %v, %v, want false", s, got, err)
			}
			return
		}
		want, wantErr := strconv.ParseBool(s)
		if got != want || (err == nil) != (wantErr == nil) {
			t.Fatalf("parseBool(%q) = %v, %v; strconv.ParseBool says %v, %v", s, got, err, want, wantErr)
		}
	})
}

func FuzzBindJSONErrorShape(f *testing.F) {
	for _, seed := range []string{
		"",
		"{",
		`{"n":"x"}`,
		`{"a":{"b":[1,"x"]}}`,
		`[1]`,
		`{"m":{"a/b":"x"}}`,
		`null`,
		`{"n":1.5}`,
		`{"n":1e400}`,
		`{"n":1} x`,
	} {
		f.Add(seed)
	}

	type target struct {
		N int `json:"n"`
		A struct {
			B []int `json:"b"`
		} `json:"a"`
		M map[string]int `json:"m"`
	}

	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set(HeaderContentType, MIMEApplicationJSON)
		b := NewBase(httptest.NewRecorder(), req)

		_, err := b.BindJSON[target]()
		if err == nil {
			return
		}
		he, ok := errors.AsType[*HTTPError](err)
		if !ok {
			t.Fatalf("error = %#v, want an *HTTPError", err)
		}
		if he.Status != http.StatusBadRequest && he.Status != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 400 or 413", he.Status)
		}
		if strings.Contains(he.Message, "json:") || strings.Contains(he.Message, " Go ") {
			t.Errorf("message = %q, carries the decoder text", he.Message)
		}
		fields := FieldErrorsOf(err)
		if len(fields) > 1 {
			t.Errorf("FieldErrorsOf = %+v, want at most one", fields)
		}
		for _, fe := range fields {
			if fe.Field == "" || fe.Message == "" {
				t.Errorf("field error = %+v, want a field and a message", fe)
			}
		}
	})
}
