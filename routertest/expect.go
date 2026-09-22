package routertest

import (
	"bytes"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
)

// Expect is a chain of checks on one [Response]. Status and Redirect stop the
// test on a miss. The other checks report the miss and go on, so one run shows
// every check that failed.
type Expect struct {
	tb  testing.TB
	res *Response
}

// Expect binds a chain of checks on r to tb.
func (r *Response) Expect(tb testing.TB) *Expect {
	tb.Helper()
	return &Expect{tb: tb, res: r}
}

// where names the request the response answered, for the start of a message.
func (e *Expect) where() string {
	if e.res.Request == nil {
		return ""
	}
	return e.res.Request.Method + " " + e.res.Request.URL.RequestURI() + ": "
}

// Status stops the test unless the status is code. The message carries the
// body, which usually says why.
func (e *Expect) Status(code int) *Expect {
	e.tb.Helper()
	if e.res.StatusCode != code {
		e.tb.Fatalf("%sstatus = %d, want %d; body: %s", e.where(), e.res.StatusCode, code, e.res.Body)
	}
	return e
}

// Body reports a body that is not exactly want.
func (e *Expect) Body(want string) *Expect {
	e.tb.Helper()
	if got := e.res.String(); got != want {
		e.tb.Errorf("%sbody = %q, want %q", e.where(), got, want)
	}
	return e
}

// Contains reports a body that does not hold sub.
func (e *Expect) Contains(sub string) *Expect {
	e.tb.Helper()
	if !strings.Contains(e.res.String(), sub) {
		e.tb.Errorf("%sbody does not contain %q; body: %s", e.where(), sub, e.res.Body)
	}
	return e
}

// NotContains reports a body that holds sub.
func (e *Expect) NotContains(sub string) *Expect {
	e.tb.Helper()
	if strings.Contains(e.res.String(), sub) {
		e.tb.Errorf("%sbody contains %q; body: %s", e.where(), sub, e.res.Body)
	}
	return e
}

// Header reports a first value of the header key other than want. A want of ""
// also takes a header that is absent.
func (e *Expect) Header(key, want string) *Expect {
	e.tb.Helper()
	if got := e.res.Header.Get(key); got != want {
		e.tb.Errorf("%sheader %s = %q, want %q", e.where(), key, got, want)
	}
	return e
}

// ContentType reports a Content-Type whose media type is not mediaType. Case
// and parameters such as the charset do not count.
//
// ContentType panics if mediaType carries parameters; check the whole value
// with Header.
func (e *Expect) ContentType(mediaType string) *Expect {
	e.tb.Helper()
	if strings.Contains(mediaType, ";") {
		panic("routertest: ContentType takes a media type without parameters; check " +
			mediaType + " with Header")
	}
	got := e.res.Header.Get(router.HeaderContentType)
	parsed, _, err := mime.ParseMediaType(got)
	if err != nil || !strings.EqualFold(parsed, mediaType) {
		e.tb.Errorf("%sContent-Type = %q, want %s", e.where(), got, mediaType)
	}
	return e
}

// Redirect stops the test unless the status is code, as Status does, and
// reports a Location other than location.
func (e *Expect) Redirect(code int, location string) *Expect {
	e.tb.Helper()
	e.Status(code)
	if got := e.res.Header.Get(router.HeaderLocation); got != location {
		e.tb.Errorf("%sLocation = %q, want %q", e.where(), got, location)
	}
	return e
}

// FieldErrors reports an answer of [router.JSONErrorHandler] whose details do
// not name exactly the fields names, in any order. With no names it wants an
// error body without field errors. See [Response.ErrorBody].
func (e *Expect) FieldErrors(names ...string) *Expect {
	e.tb.Helper()
	body, err := e.res.ErrorBody()
	if err != nil {
		e.tb.Errorf("%s%v", e.where(), err)
		return e
	}
	fields, _ := body.Details.([]router.FieldError)
	got := make([]string, len(fields))
	for i, f := range fields {
		got[i] = f.Field
	}
	want := slices.Sorted(slices.Values(names))
	if !slices.Equal(slices.Sorted(slices.Values(got)), want) {
		e.tb.Errorf("%sfield errors = %v, want %v; body: %s", e.where(), got, want, e.res.Body)
	}
	return e
}

// Events reports a body that does not hold exactly the server-sent events
// want, in order. See [Events] for how the body is read.
func (e *Expect) Events(want ...Event) *Expect {
	e.tb.Helper()
	if got := Events(e.res); !slices.Equal(got, want) {
		e.tb.Errorf("%sevents = %+v, want %+v; body: %s", e.where(), got, want, e.res.Body)
	}
	return e
}

// Golden reports a body that differs from the file testdata/name. Run the test
// with -routertest.update, or with an -update flag of your own, to write the
// file instead.
//
// name is a slash path inside testdata, and it cannot leave that directory.
func (e *Expect) Golden(name string) *Expect {
	e.tb.Helper()
	rel, err := goldenName(name)
	if err != nil {
		e.tb.Errorf("routertest: invalid golden file name %q: %v", name, err)
		return e
	}
	file := filepath.Join("testdata", rel)
	if goldenUpdate() {
		if err := writeGolden(e.tb, rel, e.res.Body); err != nil {
			e.tb.Errorf("routertest: write %s: %v", file, err)
		}
		return e
	}
	root, err := os.OpenRoot("testdata")
	if err != nil {
		e.tb.Errorf("routertest: open the golden directory: %v", err)
		return e
	}
	defer closeGoldenRoot(e.tb, root)
	want, err := root.ReadFile(rel)
	if err != nil {
		e.tb.Errorf("%sread %s: %v; run the test with -%s to write it", e.where(), file, err, updateFlagName)
		return e
	}
	if !bytes.Equal(e.res.Body, want) {
		e.tb.Errorf("%s%s differs; run the test with -%s to accept the change\ngot:\n%s\nwant:\n%s",
			e.where(), file, updateFlagName, e.res.Body, want)
	}
	return e
}
