package serve_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/serve"
)

var ja4Shape = regexp.MustCompile(`^t13[di]\d{4}(h2|00)_[0-9a-f]{12}_[0-9a-f]{12}$`)

func echoJA4() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:errcheck // The assertions of the test read what arrived.
		w.Write([]byte(serve.JA4(r.Context()) + "|" + serve.JA3(r.Context())))
	})
}

func TestCaptureJA4(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		name := "HTTP/1.1"
		if http2 {
			name = "HTTP/2"
		}
		t.Run(name, func(t *testing.T) {
			certPEM, cert := selfSignedCert(t)
			s := start(t, echoJA4(), serve.Config{Certificates: []tls.Certificate{cert}, CaptureJA4: true})
			res := await(t, call(tlsClient(t, certPEM, http2), s.url("https", "/")))
			if res.err != nil {
				t.Fatalf("request: %v", res.err)
			}
			ja4, ja3, _ := strings.Cut(res.body, "|")
			if !ja4Shape.MatchString(ja4) || ja3 != "" {
				t.Fatalf("JA4, JA3 = %q, %q; want a JA4 of the Go client and no JA3", ja4, ja3)
			}
			// A request to an IP sends no SNI, and the HTTP/1.1 client no ALPN.
			want := "00"
			if http2 {
				want = "h2"
			}
			if ja4[8:10] != want {
				t.Errorf("JA4 %q names the ALPN %q, want %q", ja4, ja4[8:10], want)
			}
		})
	}
}

func TestCaptureJA4KeepsTheConnContextOfOnServer(t *testing.T) {
	type key struct{}
	certPEM, cert := selfSignedCert(t)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := r.Context().Value(key{}).(string)
		//nolint:errcheck // The assertions of the test read what arrived.
		w.Write([]byte(v + "|" + serve.JA4(r.Context())))
	})
	s := start(t, h, serve.Config{
		Certificates: []tls.Certificate{cert},
		CaptureJA4:   true,
		OnServer: func(srv *http.Server) error {
			srv.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
				return context.WithValue(ctx, key{}, "mine")
			}
			return nil
		},
	})
	res := await(t, call(tlsClient(t, certPEM, false), s.url("https", "/")))
	mine, ja4, _ := strings.Cut(res.body, "|")
	if mine != "mine" || !ja4Shape.MatchString(ja4) {
		t.Fatalf("body = %q, want the value of OnServer and a JA4", res.body)
	}
}

func TestJA4IsEmptyWithoutTheCapture(t *testing.T) {
	certPEM, cert := selfSignedCert(t)
	s := start(t, echoJA4(), serve.Config{Certificates: []tls.Certificate{cert}})
	if res := await(t, call(tlsClient(t, certPEM, false), s.url("https", "/"))); res.body != "|" {
		t.Fatalf("body = %q, want no fingerprint", res.body)
	}
}

func TestCaptureJA4NeedsTLS(t *testing.T) {
	err := serve.Run(context.Background(), ok(), serve.Config{Addr: "127.0.0.1:0", CaptureJA4: true})
	if err == nil || !strings.Contains(err.Error(), "CaptureJA4 needs a server that ends TLS itself") {
		t.Fatalf("err = %v, want the missing TLS named", err)
	}
}
