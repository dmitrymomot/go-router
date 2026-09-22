package router

import (
	"net/http"
	"testing"
)

func TestValidatePatternMatchesWhatHandleAccepts(t *testing.T) {
	tests := []struct {
		pattern string
		ok      bool
	}{
		{"/", true},
		{"/users/{id}", true},
		{"/orders/{id:[0-9]+}", true},
		{"/files/{path...}", true},
		{"/users/{id", false},
		{"/files/{path...}/x", false},
		{"/users/{id:uuid}", true},
		{"/n/{n:int}", true},
		{"/t/{t:slug}", true},
		{"/rep-{d:int}.csv", true},
		{"/x/{id:[0-9]{6}}", true},
		{"/x/{id:(?:v1)}", true},
		{"/x/{id:123}", false},
		{"/x/{id:_a}", false},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			err := ValidatePattern(tc.pattern)
			if tc.ok && err != nil {
				t.Fatalf("ValidatePattern(%q) = %v, want nil", tc.pattern, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidatePattern(%q) = nil, want an error", tc.pattern)
			}
			r := newTestRouter()
			register := func() { r.GET(tc.pattern, echoRoute) }
			if tc.ok {
				register()
				return
			}
			mustPanicContaining(t, err.Error(), register)
		})
	}
}

// v0.1.0 compiled {id:uuid} as a regular expression, which matched only the
// word "uuid", and registered every other bare word just as silently.
func TestBareWordConstraintNamesAClass(t *testing.T) {
	const id = "0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d"
	r := newTestRouter()
	r.GET("/x/{id:uuid}", echoRoute)
	r.GET("/lit/{id:(?:v1)}", echoRoute)

	for target, want := range map[string]int{
		"/x/uuid":   http.StatusNotFound,
		"/x/" + id:  http.StatusOK,
		"/lit/v1":   http.StatusOK,
		"/lit/v2":   http.StatusNotFound,
		"/lit/(v1)": http.StatusNotFound,
	} {
		if got := do(r, http.MethodGet, target).Code; got != want {
			t.Errorf("GET %s = %d, want %d", target, got, want)
		}
	}

	for name, register := range map[string]func(r *Router[*tctx]){
		"misspelled class": func(r *Router[*tctx]) { r.GET("/x/{id:uuidd}", echoRoute) },
		"literal word":     func(r *Router[*tctx]) { r.GET("/x/{id:v1}", echoRoute) },
		"number":           func(r *Router[*tctx]) { r.GET("/x/{id:123}", echoRoute) },
		"template":         func(r *Router[*tctx]) { r.GET("/f-{id:nope}.csv", echoRoute) },
		"route prefix":     func(r *Router[*tctx]) { r.Route("/t/{t:nope}", nil) },
		"mount prefix":     func(r *Router[*tctx]) { r.Mount("/m/{m:nope}", newTestRouter()) },
		"host":             func(r *Router[*tctx]) { r.Host("{t:nope}.example.com", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			mustPanicContaining(t, "names no parameter class", func() { register(newTestRouter()) })
		})
	}
}

func TestBuiltinClasses(t *testing.T) {
	r := newTestRouter()
	r.GET("/uuid/{v:uuid}", echoRoute)
	r.GET("/int/{v:int}", echoRoute)
	r.GET("/slug/{v:slug}", echoRoute)

	tests := []struct {
		target string
		want   int
	}{
		{"/uuid/0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d", http.StatusOK},
		{"/uuid/0198C5B6-3F0E-7B3A-9C1D-2F4E6A8B0C1D", http.StatusOK},
		{"/uuid/0198c5b6-3F0E-7b3a-9C1D-2f4e6a8b0c1d", http.StatusOK},
		{"/uuid/0198c5b63f0e7b3a9c1d2f4e6a8b0c1d", http.StatusNotFound},
		{"/uuid/%7B0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d%7D", http.StatusNotFound},
		{"/uuid/urn:uuid:0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1d", http.StatusNotFound},
		{"/uuid/0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1", http.StatusNotFound},
		{"/uuid/0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1dd", http.StatusNotFound},
		{"/uuid/0198c5b6-3f0e-7b3a-9c1d-2f4e6a8b0c1g", http.StatusNotFound},
		{"/uuid/0198c5b6_3f0e-7b3a-9c1d-2f4e6a8b0c1d", http.StatusNotFound},
		{"/int/0", http.StatusOK},
		{"/int/007", http.StatusOK},
		{"/int/123", http.StatusOK},
		{"/int/-1", http.StatusNotFound},
		{"/int/+1", http.StatusNotFound},
		{"/int/1.5", http.StatusNotFound},
		{"/int/%EF%BC%91", http.StatusNotFound},
		{"/slug/acme", http.StatusOK},
		{"/slug/a-b", http.StatusOK},
		{"/slug/a--b", http.StatusOK},
		{"/slug/a1", http.StatusOK},
		{"/slug/-a", http.StatusNotFound},
		{"/slug/a-", http.StatusNotFound},
		{"/slug/Acme", http.StatusNotFound},
		{"/slug/a_b", http.StatusNotFound},
		{"/slug/a.b", http.StatusNotFound},
	}
	for _, tc := range tests {
		if got := do(r, http.MethodGet, tc.target).Code; got != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.target, got, tc.want)
		}
	}
}

func TestValidatePatternLeavesDeclaredClassesToHandle(t *testing.T) {
	if err := ValidatePattern("/r/{code:shortid}"); err != nil {
		t.Errorf("ValidatePattern with a declarable class = %v, want nil", err)
	}
	if err := ValidatePattern("/r/{code:9x}"); err == nil {
		t.Error("ValidatePattern with a class name no router can declare = nil, want an error")
	}
	r := newTestRouter()
	mustPanicContaining(t, "names no parameter class", func() { r.GET("/r/{code:shortid}", echoRoute) })
}
