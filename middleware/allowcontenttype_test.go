package middleware_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

func TestAllowContentType(t *testing.T) {
	r := newRouter()
	r.Logger(slog.New(slog.DiscardHandler))
	r.Use(middleware.AllowContentType[*appContext]("application/json", "Image/*"))
	r.POST("/", hello)

	post := func(ct, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if ct != "" {
			req.Header.Set(router.HeaderContentType, ct)
		}
		return do(r, req).Code
	}
	tests := []struct {
		name, ct, body string
		want           int
	}{
		{"allowed", "application/json", "{}", http.StatusOK},
		{"parameters and case", "Application/JSON; charset=utf-8", "{}", http.StatusOK},
		{"family", "image/png", "x", http.StatusOK},
		{"other type", "text/plain", "x", http.StatusUnsupportedMediaType},
		{"missing", "", "x", http.StatusUnsupportedMediaType},
		{"malformed", "json", "x", http.StatusUnsupportedMediaType},
		{"no body", "text/plain", "", http.StatusOK},
		{"no body no type", "", "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := post(tt.ct, tt.body); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAllowContentTypePanics(t *testing.T) {
	tests := map[string][]string{
		"empty":      nil,
		"junk":       {"nonsense"},
		"parameters": {"text/plain; charset=utf-8"},
	}
	for name, types := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic")
				}
			}()
			middleware.AllowContentType[*appContext](types...)
		})
	}
}
