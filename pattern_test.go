package router

import "testing"

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
