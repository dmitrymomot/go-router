package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

type session struct {
	csrfToken  string
	csrfCookie *http.Cookie
	userCookie *http.Cookie
}

func TestIndexIssuesCSRFAndPinsAssets(t *testing.T) {
	rm := newRoom()
	rec := send(t, newRouter(rm), http.MethodGet, "/", nil, false)
	wantStatus(t, rec, http.StatusOK)

	token := hiddenToken(t, rec.Body.String())
	cookie := responseCookie(t, rec, middleware.DefaultCSRFCookieName)
	if cookie.Value != token {
		t.Errorf("CSRF cookie = %q, form token = %q", cookie.Value, token)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Errorf("CSRF cookie = %#v", cookie)
	}

	body := rec.Body.String()
	assets := []string{
		`src="https://cdn.jsdelivr.net/npm/htmx.org@2.0.10/dist/htmx.min.js" integrity="sha384-H5SrcfygHmAuTDZphMHqBJLc3FhssKjG7w/CeCpFReSfwBWDTKpkzPP8c+cLsK+V" crossorigin="anonymous"`,
		`src="https://cdn.jsdelivr.net/npm/htmx-ext-sse@2.2.4" integrity="sha384-A986SAtodyH8eg8x8irJnYUk7i9inVQqYigD6qZ9evobksGNIXfeFvDwLSHcp31N" crossorigin="anonymous"`,
		`action="/join" method="post" hx-post="/join"`,
	}
	for _, asset := range assets {
		if !strings.Contains(body, asset) {
			t.Errorf("index does not contain %q", asset)
		}
	}
	if strings.Contains(body, "unpkg.com") {
		t.Error("index still loads an unverified unpkg asset")
	}
	if got := rec.Header().Get(router.HeaderXContentTypeOptions); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get(router.HeaderXFrameOptions); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q", got)
	}
}

func TestJoinRequiresCSRFAndAuthenticates(t *testing.T) {
	rm := newRoom()
	h := newRouter(rm)

	missing := send(t, h, http.MethodPost, "/join", url.Values{"name": {"Alice"}}, true)
	wantStatus(t, missing, http.StatusForbidden)
	wantNoResponseCookie(t, missing, cookieName)

	s := startSession(t, h)
	invalid := send(t, h, http.MethodPost, "/join", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
		"name":                          {"\x00 \t"},
	}, true, s.csrfCookie)
	wantStatus(t, invalid, http.StatusOK)
	if !strings.Contains(invalid.Body.String(), `role="alert"`) {
		t.Error("invalid join did not return the form error")
	}
	if got := hiddenToken(t, invalid.Body.String()); got != s.csrfToken {
		t.Errorf("replacement form token = %q, want %q", got, s.csrfToken)
	}
	wantNoResponseCookie(t, invalid, cookieName)

	joined := send(t, h, http.MethodPost, "/join", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
		"name":                          {"  Alice  "},
	}, true, s.csrfCookie)
	wantStatus(t, joined, http.StatusOK)
	if got := joined.Header().Get(router.HeaderHXRedirect); got != "/room" {
		t.Errorf("HX-Redirect = %q, want /room", got)
	}
	s.userCookie = responseCookie(t, joined, cookieName)
	if s.userCookie.Value != "Alice" || !s.userCookie.HttpOnly || s.userCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("user cookie = %#v", s.userCookie)
	}

	chat := send(t, h, http.MethodGet, "/room", nil, false, s.csrfCookie, s.userCookie)
	wantStatus(t, chat, http.StatusOK)
	body := chat.Body.String()
	if !strings.Contains(body, "<strong>Alice</strong>") {
		t.Error("chat page did not authenticate Alice")
	}
	if strings.Count(body, `name="_csrf" value="`+s.csrfToken+`"`) != 2 {
		t.Error("chat page did not carry the token in both state-changing forms")
	}
	for _, form := range []string{
		`action="/leave" method="post" hx-post="/leave"`,
		`action="/room/messages" method="post" hx-post="/room/messages"`,
	} {
		if !strings.Contains(body, form) {
			t.Errorf("chat page does not contain %q", form)
		}
	}
}

func TestAuthenticationRedirectsPagesAndRejectsAnonymousSSE(t *testing.T) {
	h := newRouter(newRoom())

	page := send(t, h, http.MethodGet, "/room", nil, false)
	wantStatus(t, page, http.StatusSeeOther)
	if got := page.Header().Get(router.HeaderLocation); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	hx := send(t, h, http.MethodGet, "/room", nil, true)
	wantStatus(t, hx, http.StatusOK)
	if got := hx.Header().Get(router.HeaderHXRedirect); got != "/" {
		t.Errorf("HX-Redirect = %q, want /", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/room/events", nil)
	req.Header.Set(router.HeaderAccept, router.MIMETextEventStream)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusUnauthorized)
}

func TestLogoutIsPOSTAndCSRFProtected(t *testing.T) {
	h := newRouter(newRoom())
	s := joinedSession(t, h, "Alice")

	get := send(t, h, http.MethodGet, "/leave", nil, false, s.csrfCookie, s.userCookie)
	wantStatus(t, get, http.StatusMethodNotAllowed)

	missing := send(t, h, http.MethodPost, "/leave", nil, true, s.csrfCookie, s.userCookie)
	wantStatus(t, missing, http.StatusForbidden)
	wantNoResponseCookie(t, missing, cookieName)

	left := send(t, h, http.MethodPost, "/leave", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
	}, true, s.csrfCookie, s.userCookie)
	wantStatus(t, left, http.StatusOK)
	if got := left.Header().Get(router.HeaderHXRedirect); got != "/" {
		t.Errorf("HX-Redirect = %q, want /", got)
	}
	cleared := responseCookie(t, left, cookieName)
	if cleared.Value != "" || cleared.MaxAge != -1 || !cleared.HttpOnly {
		t.Errorf("cleared user cookie = %#v", cleared)
	}

	regular := send(t, h, http.MethodPost, "/leave", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
	}, false, s.csrfCookie, s.userCookie)
	wantStatus(t, regular, http.StatusSeeOther)
	if got := regular.Header().Get(router.HeaderLocation); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

func TestMessageRequiresCSRFAndBroadcasts(t *testing.T) {
	rm := newRoom()
	h := newRouter(rm)
	s := joinedSession(t, h, "Alice")
	messages, unsubscribe := rm.join()
	defer unsubscribe()

	missing := send(t, h, http.MethodPost, "/room/messages", url.Values{"text": {"forged"}}, true,
		s.csrfCookie, s.userCookie)
	wantStatus(t, missing, http.StatusForbidden)
	wantNoMessage(t, messages)

	sent := send(t, h, http.MethodPost, "/room/messages", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
		"text":                          {" hello\x00   room "},
	}, true, s.csrfCookie, s.userCookie)
	wantStatus(t, sent, http.StatusNoContent)
	if got := sent.Header().Get(router.HeaderHXTrigger); got != "message-sent" {
		t.Errorf("HX-Trigger = %q, want message-sent", got)
	}
	select {
	case got := <-messages:
		if got.Kind != kindMessage || got.Author != "Alice" || got.Text != "hello room" {
			t.Errorf("message = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("room did not receive the message")
	}

	empty := send(t, h, http.MethodPost, "/room/messages", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
		"text":                          {" \t "},
	}, true, s.csrfCookie, s.userCookie)
	wantStatus(t, empty, http.StatusNoContent)
	if got := empty.Header().Get(router.HeaderHXTrigger); got != "" {
		t.Errorf("empty message HX-Trigger = %q", got)
	}
	wantNoMessage(t, messages)

	large := send(t, h, http.MethodPost, "/room/messages", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
		"text":                          {strings.Repeat("x", maxBodyBytes)},
	}, true, s.csrfCookie, s.userCookie)
	wantStatus(t, large, http.StatusRequestEntityTooLarge)
	wantNoMessage(t, messages)
}

func TestSSEFlowAndRoomShutdown(t *testing.T) {
	rm := newRoom()
	srv := httptest.NewServer(newRouter(rm))
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := srv.Client()
	client.Jar = jar

	index, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexBody, readErr := io.ReadAll(index.Body)
	indexCloseErr := index.Body.Close()
	if readErr != nil {
		t.Fatalf("read index response body: %v", readErr)
	}
	if indexCloseErr != nil {
		t.Fatalf("close index response body: %v", indexCloseErr)
	}
	token := hiddenToken(t, string(indexBody))

	form := url.Values{
		middleware.DefaultCSRFFormField: {token},
		"name":                          {"Alice"},
	}
	joinReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/join",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	joinReq.Header.Set(router.HeaderContentType, "application/x-www-form-urlencoded")
	joinReq.Header.Set(router.HeaderHXRequest, "true")
	joinRes, err := client.Do(joinReq)
	if err != nil {
		t.Fatal(err)
	}
	_, drainErr := io.Copy(io.Discard, joinRes.Body)
	joinCloseErr := joinRes.Body.Close()
	if drainErr != nil {
		t.Fatalf("drain join response body: %v", drainErr)
	}
	if joinCloseErr != nil {
		t.Fatalf("close join response body: %v", joinCloseErr)
	}
	if joinRes.StatusCode != http.StatusOK {
		t.Fatalf("join status = %d", joinRes.StatusCode)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	eventsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/room/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventsReq.Header.Set(router.HeaderAccept, router.MIMETextEventStream)
	eventsRes, err := client.Do(eventsReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := eventsRes.Body.Close(); err != nil {
			t.Errorf("close events response body: %v", err)
		}
	}()
	if eventsRes.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", eventsRes.StatusCode)
	}
	if got := eventsRes.Header.Get(router.HeaderContentType); !strings.HasPrefix(got, router.MIMETextEventStream) {
		t.Fatalf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(eventsRes.Body)
	if got := readFrame(t, reader); got != "retry: 2000" {
		t.Errorf("retry frame = %q", got)
	}
	joined := readFrame(t, reader)
	if !strings.Contains(joined, "event: notice") || !strings.Contains(joined, "Alice") ||
		!strings.Contains(joined, "joined the chat") {
		t.Errorf("join frame = %q", joined)
	}

	rm.broadcast(message{Kind: kindMessage, Author: "Alice", Text: "hello", At: time.Now()})
	messageFrame := readFrame(t, reader)
	if !strings.Contains(messageFrame, "event: message") || !strings.Contains(messageFrame, "hello") ||
		!strings.Contains(messageFrame, `class="msg own"`) {
		t.Errorf("message frame = %q", messageFrame)
	}

	rm.close()
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Errorf("stream after shutdown = %q, %v", rest, err)
	}
	if got := roomReaders(rm); got != 0 {
		t.Errorf("readers after shutdown = %d", got)
	}
}

func send(t *testing.T, h http.Handler, method, target string, form url.Values, hx bool,
	cookies ...*http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set(router.HeaderContentType, "application/x-www-form-urlencoded")
	}
	if hx {
		req.Header.Set(router.HeaderHXRequest, "true")
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func startSession(t *testing.T, h http.Handler) session {
	t.Helper()
	rec := send(t, h, http.MethodGet, "/", nil, false)
	wantStatus(t, rec, http.StatusOK)
	return session{
		csrfToken:  hiddenToken(t, rec.Body.String()),
		csrfCookie: responseCookie(t, rec, middleware.DefaultCSRFCookieName),
	}
}

func joinedSession(t *testing.T, h http.Handler, name string) session {
	t.Helper()
	s := startSession(t, h)
	rec := send(t, h, http.MethodPost, "/join", url.Values{
		middleware.DefaultCSRFFormField: {s.csrfToken},
		"name":                          {name},
	}, true, s.csrfCookie)
	wantStatus(t, rec, http.StatusOK)
	s.userCookie = responseCookie(t, rec, cookieName)
	return s
}

func hiddenToken(t *testing.T, body string) string {
	t.Helper()
	const prefix = `name="_csrf" value="`
	_, rest, ok := strings.Cut(body, prefix)
	if !ok {
		t.Fatal("response has no CSRF form field")
	}
	token, _, ok := strings.Cut(rest, `"`)
	if !ok || token == "" {
		t.Fatal("response has an invalid CSRF form field")
	}
	return token
}

func responseCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response has no %s cookie", name)
	return nil
}

func wantNoResponseCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			t.Errorf("response unexpectedly set %s", name)
		}
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, want, rec.Body.String())
	}
}

func wantNoMessage(t *testing.T, ch <-chan message) {
	t.Helper()
	select {
	case got := <-ch:
		t.Errorf("unexpected message = %#v", got)
	default:
	}
}

func readFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
}

func roomReaders(rm *room) int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return len(rm.readers)
}
