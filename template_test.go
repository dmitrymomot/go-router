package router

import (
	"net/http"
	"testing"
)

func TestPartialSegmentParameters(t *testing.T) {
	r := newTestRouter()
	for _, p := range []string{
		"/reports/rep-{date}.csv",
		"/reports/sum-{date}.csv",
		"/reports/rep-{date}.json",
		"/reports/latest.csv",
		"/reports/{name}",
		"/files/{name}.{ext}",
		"/v{major}.{minor}/ping",
		"/logs/{app}-{level}.log",
		"/tickets/T-{id:[0-9]+}",
	} {
		r.GET(p, echoRoute)
	}

	tests := []struct {
		path string
		want string
	}{
		{"/reports/rep-20260102.csv", "/reports/rep-{date}.csv date=20260102"},
		{"/reports/sum-20260102.csv", "/reports/sum-{date}.csv date=20260102"},
		{"/reports/rep-20260102.json", "/reports/rep-{date}.json date=20260102"},
		{"/reports/latest.csv", "/reports/latest.csv"},
		{"/reports/rep-a.csv.csv", "/reports/rep-{date}.csv date=a.csv"},
		{"/reports/quarterly", "/reports/{name} name=quarterly"},
		{"/reports/rep-.csv", "/reports/{name} name=rep-.csv"},
		{"/files/notes.txt", "/files/{name}.{ext} name=notes ext=txt"},
		{"/files/a.b.txt", "/files/{name}.{ext} name=a.b ext=txt"},
		{"/v1.27/ping", "/v{major}.{minor}/ping major=1 minor=27"},
		{"/logs/api-warn.log", "/logs/{app}-{level}.log app=api level=warn"},
		{"/logs/a-b-c.log", "/logs/{app}-{level}.log app=a-b level=c"},
		{"/tickets/T-4711", "/tickets/T-{id:[0-9]+} id=4711"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPartialSegmentMisses(t *testing.T) {
	r := newTestRouter()
	r.GET("/reports/rep-{date}.csv", echoRoute)
	r.GET("/tickets/T-{id:[0-9]+}", echoRoute)

	for _, path := range []string{
		"/reports/rep-.csv",
		"/reports/rep-2026.txt",
		"/reports/sum-2026.csv",
		"/reports/2026.csv",
		"/tickets/T-abc",
		"/tickets/abc",
	} {
		t.Run(path, func(t *testing.T) {
			if code := do(r, http.MethodGet, path).Code; code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", code)
			}
		})
	}
}

func TestPartialSegmentParamAs(t *testing.T) {
	r := newTestRouter()
	r.GET("/reports/rep-{date}.csv", func(c *tctx) error {
		date, err := c.ParamAs[int]("date")
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d", date+1)
	})

	if got := do(r, http.MethodGet, "/reports/rep-20260102.csv").Body.String(); got != "20260103" {
		t.Errorf("body = %q, want %q", got, "20260103")
	}
	if code := do(r, http.MethodGet, "/reports/rep-abc.csv").Code; code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestPartialSegmentEscaping(t *testing.T) {
	r := newTestRouter()
	r.GET("/files/{name}.txt", func(c *tctx) error {
		return c.String(http.StatusOK, c.Param("name"))
	})

	if got := do(r, http.MethodGet, "/files/a%20b.txt").Body.String(); got != "a b" {
		t.Errorf("param = %q, want %q", got, "a b")
	}
}

func BenchmarkTemplateSegment(b *testing.B) {
	r, w := benchRouter("/reports/rep-{date}.csv", "/reports/{name}")
	benchServe(b, r, w, "/reports/rep-20260102.csv")
}

func TestParameterEscaping(t *testing.T) {
	r := newTestRouter()
	r.GET("/files/{name}", func(c *tctx) error { return c.String(http.StatusOK, c.Param("name")) })
	r.GET("/tree/{path...}", func(c *tctx) error { return c.String(http.StatusOK, c.Param("path")) })

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"an escaped separator stays inside one segment", "/files/a%2Fb", "a/b"},
		{"an escaped space decodes", "/files/a%20b", "a b"},
		{"a plus sign is literal in a path", "/files/a+b", "a+b"},
		{"a literal percent survives", "/files/a%25b", "a%b"},
		{"a double encoded separator survives", "/files/a%252Fb", "a%2Fb"},
		{"a catch-all decodes each segment", "/tree/x/a%2Fb/y", "x/a/b/y"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := do(r, http.MethodGet, tc.target).Body.String(); got != tc.want {
				t.Errorf("param = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMountHandlerKeepsTheEscapedPath(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // test helper
		w.Write([]byte(r.URL.Path + "|" + r.URL.RawPath))
	})
	r := newTestRouter()
	r.MountHandler("/static", inner)

	for _, tc := range []struct{ target, want string }{
		{"/static/a/b.css", "/a/b.css|"},
		{"/static/a%2Fb.css", "/a/b.css|/a%2Fb.css"},
		{"/static/a%20b.css", "/a b.css|"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			if got := do(r, http.MethodGet, tc.target).Body.String(); got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}
