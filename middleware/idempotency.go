package middleware

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmitrymomot/go-router"
)

// The defaults of [IdempotencyWithConfig] and of the memory store.
const (
	// DefaultIdempotencyFormField is the form field that carries the key of a
	// plain HTML form.
	DefaultIdempotencyFormField = "_idempotency_key"
	// DefaultIdempotencyMaxBody is the longest answer that is kept for a
	// replay, and the longest request body, other than a form, that the
	// default fingerprint takes.
	DefaultIdempotencyMaxBody = 64 << 10
	// DefaultIdempotencyWait is how long a repeat waits for the request that
	// holds its key.
	DefaultIdempotencyWait = 10 * time.Second
	// DefaultIdempotencyExpiry is how long the memory store keeps a key.
	DefaultIdempotencyExpiry = 24 * time.Hour
	// DefaultIdempotencyMaxEntries is how many keys the memory store holds.
	DefaultIdempotencyMaxEntries = 64 << 10
	// MaxIdempotencyKeyLength is the longest key a client may send.
	MaxIdempotencyKeyLength = 255
)

// The causes of the answers of [IdempotencyWithConfig]. Match them with
// [errors.Is] in an error handler.
var (
	ErrIdempotencyKeyRequired = errors.New("middleware: the request needs an idempotency key")
	ErrIdempotencyKeyReused   = errors.New("middleware: the idempotency key was used for a different request")
	ErrIdempotencyInProgress  = errors.New("middleware: a request with this idempotency key is still running")
	ErrIdempotencyTooLarge    = errors.New("middleware: the answer to this idempotency key was too large to keep")
	// ErrIdempotencyBodyTooLarge is the cause of the 413 that the default
	// fingerprint answers to a body over MaxBody.
	ErrIdempotencyBodyTooLarge = errors.New("middleware: the request body is too large to fingerprint")
)

var errIdempotencyStoreFull = errors.New("middleware: every idempotency key in the store is still running")

// IdempotencyConfig configures [IdempotencyWithConfig].
//
// Store is required. Client names whose keys these are, and a nil one takes
// the address that [ClientIP] reports, so one client cannot claim, replay or
// push out the keys of another. Report the id of the signed-in user instead to
// share the keys of a user across addresses. A Client that reports "" makes
// the key global.
//
// Fingerprint says what makes two requests the same, and an error from it is
// returned as it stands. A nil one hashes the method, the path, the raw query,
// the media type of Content-Type and the body. A form body counts by its
// fields, in any order and without the [DefaultCSRFFormField] and
// [DefaultIdempotencyFormField] fields, and an uploaded file by its field,
// name, size and type; so the form counts the same whether or not [ParseForm]
// read it first. Any other body counts whole, so it may be MaxBody bytes at
// most: a longer one is refused with a 413 and [ErrIdempotencyBodyTooLarge]
// before the handler runs, and so is a body over the cap in force. The parameters
// of Content-Type do not count, since a multipart boundary changes on every
// send.
//
// Sources say where the key comes from, and an empty list reads the
// Idempotency-Key header, then the [DefaultIdempotencyFormField] form field.
// The first key found counts.
//
// MaxBody is the longest answer kept for a replay, and under the default
// fingerprint the longest request body, other than a form, that a request with
// a key may send; zero takes [DefaultIdempotencyMaxBody]. Wait is
// how long a repeat waits for the request that holds its key, and zero takes
// [DefaultIdempotencyWait]; a negative Wait answers 409 at once. Required
// refuses an unsafe request that carries no key with a 400.
type IdempotencyConfig[C router.Context] struct {
	Skip        func(c C) bool
	Store       IdempotencyStore
	Client      func(c C) string
	Fingerprint func(c C) ([]byte, error)
	Sources     []TokenSource
	MaxBody     int
	Wait        time.Duration
	Required    bool
}

// Idempotency runs an unsafe request once per key, so a double submit or a
// client that retries after a lost answer does not charge twice. The client
// sends the key in the Idempotency-Key header, or a form sends it in the
// [DefaultIdempotencyFormField] field. A request without a key runs as usual.
// See [IdempotencyWithConfig] for how each repeat is answered.
//
// The keys belong to the address of the client, so a client that changes its
// address loses them; set Client through IdempotencyWithConfig to give each
// user their own.
//
// Put it inside auth and CSRF; see Order in the package doc.
//
// Idempotency panics if store is nil.
func Idempotency[C router.Context](store IdempotencyStore) router.Middleware[C] {
	return IdempotencyWithConfig(IdempotencyConfig[C]{Store: store})
}

// IdempotencyWithConfig is [Idempotency] with a configuration. It follows the
// Idempotency-Key draft of the IETF httpapi group:
//
//   - The first request with a key runs, and its answer is stored.
//   - A repeat with the same fingerprint gets the stored answer again, and the
//     handler does not run.
//   - A repeat with another fingerprint gets 422, whether the first request is
//     done or still running.
//   - A repeat while the first request runs waits up to Wait for its answer,
//     then gets 409.
//   - Under Required, an unsafe request without a key gets 400.
//   - A key longer than [MaxIdempotencyKeyLength] bytes gets 400.
//   - Under the default fingerprint, a body other than a form that is longer
//     than MaxBody gets 413.
//
// The causes of these answers are [ErrIdempotencyKeyReused],
// [ErrIdempotencyInProgress], [ErrIdempotencyKeyRequired] and
// [ErrIdempotencyBodyTooLarge]. GET, HEAD,
// OPTIONS, TRACE and QUERY pass through.
//
// The key is released, so a repeat runs again, when the handler answers 5xx,
// when it returns an error before a status under 400 went out, and when it
// hijacks the connection. Any other answer is stored, a 4xx the handler wrote
// itself included. An answer over MaxBody keeps the key without its body, and
// a repeat gets 409 with [ErrIdempotencyTooLarge]. A panic releases the key,
// unless a status under 400 went out before it; then the key stays held until
// it expires, and a repeat gets 409.
//
// A replay carries the header fields the handler set or removed, and the
// Set-Cookie lines it added, on top of what the middleware in front set for the
// repeat. A recorded cookie is left out when the repeat sets one of the same
// name. A replay never calls the error handler itself: an error goes back up
// the chain.
//
// Put [ParseForm] in front: the form source drops a form that does not parse,
// and ParseForm refuses it first. See Order in the package doc.
//
// IdempotencyWithConfig panics on a nil Store, a nil token source, more than
// [MaxTokenSources] of them, and a negative MaxBody.
func IdempotencyWithConfig[C router.Context](cfg IdempotencyConfig[C]) router.Middleware[C] {
	if cfg.Store == nil {
		panic("middleware: IdempotencyWithConfig needs a Store")
	}
	if cfg.MaxBody < 0 {
		panic("middleware: IdempotencyWithConfig needs a MaxBody of zero or more")
	}
	if len(cfg.Sources) == 0 {
		cfg.Sources = []TokenSource{
			FromHeader(router.HeaderIdempotencyKey, ""),
			FromForm(DefaultIdempotencyFormField),
		}
	} else {
		cfg.Sources = slices.Clone(cfg.Sources)
		checkTokenSources("IdempotencyConfig", cfg.Sources)
	}
	if cfg.MaxBody == 0 {
		cfg.MaxBody = DefaultIdempotencyMaxBody
	}
	if cfg.Wait == 0 {
		cfg.Wait = DefaultIdempotencyWait
	}
	if cfg.Client == nil {
		cfg.Client = func(c C) string { return ClientIP(c) }
	}
	if cfg.Fingerprint == nil {
		maxBody := cfg.MaxBody
		cfg.Fingerprint = func(c C) ([]byte, error) { return idempotencyFingerprint(c, maxBody) }
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) || isSafeMethod(c.Request().Method) {
				return next(c)
			}

			var key string
			for token := range readTokens(c, cfg.Sources) {
				key = token
				break
			}
			if key == "" {
				if cfg.Required {
					return router.ErrBadRequest.WithMessage("the request needs an Idempotency-Key").
						WithError(ErrIdempotencyKeyRequired)
				}
				return next(c)
			}
			if len(key) > MaxIdempotencyKeyLength {
				return router.ErrBadRequest.WithMessage("the Idempotency-Key is longer than %d bytes",
					MaxIdempotencyKeyLength)
			}
			key = idempotencyStoreKey(cfg.Client(c), key)

			fp, err := cfg.Fingerprint(c)
			if err != nil {
				return err
			}
			held, claimed, err := claimIdempotencyKey(c, cfg.Store, cfg.Wait, key, fp)
			if err != nil {
				return err
			}
			if claimed {
				return runIdempotent(c, cfg.Store, cfg.MaxBody, key, next)
			}
			return replayIdempotent(c.Response(), held.Answer)
		}
	}
}

// The backoff of a repeat that waits for the request holding its key.
const (
	idempotencyFirstPoll = 10 * time.Millisecond
	idempotencyMaxPoll   = 200 * time.Millisecond
)

// claimIdempotencyKey reports claimed, or an entry that holds an answer with
// the same fingerprint. The fingerprint is compared before any wait, so a
// mismatch is refused while the first request still runs.
func claimIdempotencyKey(
	c router.Context, store IdempotencyStore, wait time.Duration, key string, fp []byte,
) (IdempotencyEntry, bool, error) {
	deadline := time.Now().Add(wait)
	poll := idempotencyFirstPoll
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		held, claimed, err := store.Claim(c, key, fp)
		if err != nil {
			return held, false, router.ErrInternalServerError.WithError(
				fmt.Errorf("middleware: idempotency store: %w", err))
		}
		if claimed {
			return held, true, nil
		}
		if !bytes.Equal(held.Fingerprint, fp) {
			return held, false, router.ErrUnprocessableEntity.
				WithMessage("the idempotency key was used for a different request").
				WithError(ErrIdempotencyKeyReused)
		}
		if held.Answer != nil {
			return held, false, nil
		}
		if wait < 0 || !time.Now().Before(deadline) {
			return held, false, router.ErrConflict.
				WithMessage("a request with this idempotency key is still running").
				WithError(ErrIdempotencyInProgress)
		}

		// The wait never runs past the deadline, so the last Claim falls on it.
		d := min(poll, time.Until(deadline))
		poll = min(2*poll, idempotencyMaxPoll)
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			timer.Reset(d)
		}
		select {
		case <-c.Done():
			return held, false, c.Err()
		case <-timer.C:
		}
	}
}

func runIdempotent[C router.Context](
	c C, store IdempotencyStore, maxBody int, key string, next router.HandlerFunc[C],
) error {
	res := c.Response()
	before := res.Header().Clone()
	stop := res.Capture(maxBody)

	settled := false
	defer func() {
		if settled {
			return
		}
		// A panic is on its way up. It is left to Recover, and only the key
		// is dealt with here.
		if rec := stop(); rec.Status != 0 && rec.Status < http.StatusBadRequest {
			idempotencyLogger(c).Warn("middleware: a panic after a success holds the idempotency key until it expires",
				slog.Int("status", rec.Status))
			return
		}
		logIdempotencyStoreError(c, "release", store.Release(c, key))
	}()

	err := next(c)
	rec := stop()
	settled = true

	hijacked := res.Committed && rec.Status == 0
	failed := rec.Status >= http.StatusInternalServerError ||
		err != nil && (rec.Status == 0 || rec.Status >= http.StatusBadRequest)
	if hijacked || failed {
		logIdempotencyStoreError(c, "release", store.Release(c, key))
		return err
	}

	if rec.Status == 0 {
		// The handler wrote nothing, and net/http answers that with a 200.
		rec.Status, rec.Header = http.StatusOK, res.Header()
	}
	if rec.Truncated {
		rec.Header, rec.Body = nil, nil
	} else {
		rec.Header = idempotencyHeaderDelta(before, rec.Header)
	}
	logIdempotencyStoreError(c, "complete", store.Complete(c, key, rec))
	return err
}

func replayIdempotent(res *router.Response, rec *router.Recorded) error {
	if rec.Truncated {
		return router.ErrConflict.
			WithMessage("the answer to this idempotency key was too large to keep").
			WithError(ErrIdempotencyTooLarge)
	}
	if rec.Status < http.StatusOK || rec.Status > 599 {
		return router.ErrInternalServerError.WithError(
			fmt.Errorf("middleware: the idempotency store holds the status %d", rec.Status))
	}

	h := res.Header()
	for k, vs := range rec.Header {
		switch {
		case k == router.HeaderSetCookie:
		case len(vs) == 0:
			delete(h, k)
		default:
			h[k] = slices.Clone(vs)
		}
	}
	if cookies := rec.Header[router.HeaderSetCookie]; len(cookies) > 0 {
		// A cookie the middleware in front already set for the repeat wins.
		addMissingCookies(res.Header(), cookies)
	}
	res.WriteHeader(rec.Status)
	if len(rec.Body) == 0 {
		return nil
	}
	_, err := res.Write(rec.Body)
	return err
}

func logIdempotencyStoreError(c router.Context, op string, err error) {
	if err != nil {
		idempotencyLogger(c).Warn("middleware: idempotency store "+op+" failed; the key stays held until it expires",
			slog.Any("error", err))
	}
}

func idempotencyLogger(c router.Context) *slog.Logger {
	if c != nil {
		if b, ok := router.FromContext(c); ok {
			return b.Logger()
		}
	}
	return slog.Default()
}

// idempotencyFingerprint is the default fingerprint; see IdempotencyConfig.
func idempotencyFingerprint(c router.Context, maxBody int) ([]byte, error) {
	req := c.Request()
	contentType := req.Header.Get(router.HeaderContentType)
	mediaType, _, _ := strings.Cut(contentType, ";")
	buf := appendIdempotencyFields(nil, req.Method, req.URL.Path, req.URL.RawQuery,
		strings.ToLower(strings.TrimSpace(mediaType)))

	// The rule of ParseForm and of the form readers, so a form counts the same
	// whether or not it was read before.
	if b, ok := router.FromContext(c); ok && isFormType(contentType) {
		form, err := b.FormValues()
		if err != nil {
			return nil, err
		}
		buf = appendIdempotencyForm(buf, form, req.MultipartForm)
	} else if req.Body != nil && req.Body != http.NoBody {
		// The whole body counts, or two bodies that share their first
		// maxBody bytes would share an answer.
		if req.ContentLength > int64(maxBody) {
			return nil, idempotencyBodyTooLarge(maxBody)
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, int64(maxBody)+1))
		if err != nil {
			return nil, idempotencyBodyError(err)
		}
		if len(body) > maxBody {
			return nil, idempotencyBodyTooLarge(maxBody)
		}
		// The handler reads the body that was read here.
		req.Body = readCloser{bytes.NewReader(body), req.Body}
		buf = appendIdempotencyFields(buf, string(body))
	}
	sum := sha256.Sum256(buf)
	return sum[:], nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

func idempotencyBodyTooLarge(maxBody int) error {
	return router.ErrPayloadTooLarge.WithMessage(
		"a request with an Idempotency-Key is limited to a body of %d bytes", maxBody).
		WithError(ErrIdempotencyBodyTooLarge)
}

func idempotencyBodyError(err error) error {
	if tooBig, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return tooLarge(err, "the request body is limited to %d bytes", tooBig.Limit)
	}
	return router.ErrBadRequest.WithMessage("the request body could not be read").WithError(err)
}

// appendIdempotencyForm appends the fields of a form in the order of their
// names, without the CSRF and idempotency fields, and each uploaded file by
// its field, name, size and type.
func appendIdempotencyForm(buf []byte, form url.Values, multi *multipart.Form) []byte {
	keys := slices.DeleteFunc(slices.Sorted(maps.Keys(form)), func(k string) bool {
		return k == DefaultCSRFFormField || k == DefaultIdempotencyFormField
	})
	buf = binary.AppendUvarint(buf, uint64(len(keys)))
	for _, k := range keys {
		buf = appendIdempotencyFields(buf, k)
		buf = binary.AppendUvarint(buf, uint64(len(form[k])))
		buf = appendIdempotencyFields(buf, form[k]...)
	}

	var files map[string][]*multipart.FileHeader
	if multi != nil {
		files = multi.File
	}
	buf = binary.AppendUvarint(buf, uint64(len(files)))
	for _, field := range slices.Sorted(maps.Keys(files)) {
		buf = appendIdempotencyFields(buf, field)
		buf = binary.AppendUvarint(buf, uint64(len(files[field])))
		for _, fh := range files[field] {
			buf = appendIdempotencyFields(buf, fh.Filename, fh.Header.Get(router.HeaderContentType))
			buf = binary.AppendUvarint(buf, uint64(max(fh.Size, 0)))
		}
	}
	return buf
}

// appendIdempotencyFields puts the length before each field, so two lists of
// fields encode alike only when they are alike.
func appendIdempotencyFields(dst []byte, fields ...string) []byte {
	for _, f := range fields {
		dst = binary.AppendUvarint(dst, uint64(len(f)))
		dst = append(dst, f...)
	}
	return dst
}

// idempotencyStoreKey puts the length of the client in front, so no client
// and key can pass for another pair. A global key is the pair with no client,
// so a client cannot name the key of another either.
func idempotencyStoreKey(client, key string) string {
	return strconv.Itoa(len(client)) + ":" + client + key
}

// IdempotencyEntry is what an [IdempotencyStore] holds for one key.
//
//betteralign:check
type IdempotencyEntry struct {
	// Answer is nil while that request still runs.
	Answer *router.Recorded
	// Fingerprint is the fingerprint of the request that claimed the key. The
	// caller must not change it.
	Fingerprint []byte
}

// IdempotencyStore holds the keys of [IdempotencyWithConfig].
//
// Claim is atomic. With no live entry for key, it stores a running entry that
// holds fp and reports claimed. Otherwise it reports the entry it holds. The
// store never compares fingerprints; the middleware does.
//
// Complete stores the answer and Release deletes the entry, both only while
// the entry is still running. They run after the handler, when c may already
// be done, so a store that goes over the network uses context.WithoutCancel(c).
// c is the context of the request, and [router.FromContext] reaches the
// router, its logger included.
// Keep every field of the [router.Recorded], Truncated included, and a header
// field with no value, which stands for one the handler removed.
//
// An error from Claim answers 500. An error from Complete or Release is
// logged, and the key stays held until it expires.
//
// [NewIdempotencyMemoryStore] holds the keys in this process. Implement the
// interface to share them across several, such as through Redis.
type IdempotencyStore interface {
	Claim(c router.Context, key string, fp []byte) (held IdempotencyEntry, claimed bool, err error)
	Complete(c router.Context, key string, rec router.Recorded) error
	Release(c router.Context, key string) error
}

// IdempotencyMemoryStoreConfig configures [NewIdempotencyMemoryStoreWithConfig].
// ExpiresIn is how long a key lives, counted from its claim, and zero takes
// [DefaultIdempotencyExpiry]. MaxEntries caps how many keys the store holds,
// and zero takes [DefaultIdempotencyMaxEntries].
type IdempotencyMemoryStoreConfig struct {
	ExpiresIn  time.Duration
	MaxEntries int
}

// NewIdempotencyMemoryStore builds an [IdempotencyStore] in this process that
// keeps a key for expiresIn after its claim. An expiresIn of zero takes
// [DefaultIdempotencyExpiry].
//
// The keys live in one process, so several instances each hold their own.
//
// NewIdempotencyMemoryStore panics on a negative expiresIn.
func NewIdempotencyMemoryStore(expiresIn time.Duration) IdempotencyStore {
	return NewIdempotencyMemoryStoreWithConfig(IdempotencyMemoryStoreConfig{ExpiresIn: expiresIn})
}

// NewIdempotencyMemoryStoreWithConfig is [NewIdempotencyMemoryStore] with a
// configuration, which also caps how many keys the store holds.
//
// A full store makes room by dropping the oldest key whose answer is stored,
// before that key expires, and logs a warning through the logger of the router.
// A retry with that key then runs the handler again. So MaxEntries trades
// memory for the guarantee: the store holds up to MaxEntries answers of up to
// the MaxBody of the middleware each, 4 GiB at the defaults if every answer
// were that large. The keys belong to one client each by default, so one
// client can push out the keys of another only by filling the whole store;
// put [RateLimit] in front to make that slow. A store full of running keys
// refuses a new one, and the request gets a 500.
//
// A key lives for ExpiresIn from its claim, however long its request runs. A
// request that outlives it can store its answer on, or release, the claim of a
// later request with the same key.
//
// NewIdempotencyMemoryStoreWithConfig panics on a negative ExpiresIn or
// MaxEntries.
func NewIdempotencyMemoryStoreWithConfig(cfg IdempotencyMemoryStoreConfig) IdempotencyStore {
	if cfg.ExpiresIn < 0 {
		panic("middleware: NewIdempotencyMemoryStore needs an ExpiresIn of zero or more")
	}
	if cfg.MaxEntries < 0 {
		panic("middleware: NewIdempotencyMemoryStore needs a MaxEntries of zero or more")
	}
	cfg.ExpiresIn = cmp.Or(cfg.ExpiresIn, DefaultIdempotencyExpiry)
	cfg.MaxEntries = cmp.Or(cfg.MaxEntries, DefaultIdempotencyMaxEntries)
	return &idempotencyMemoryStore{
		entries:    make(map[string]*idempotencyMemoryEntry),
		expiresIn:  cfg.ExpiresIn,
		maxEntries: cfg.MaxEntries,
	}
}

// The entries form a list in the order of their claims. Every entry lives for
// the same time from its claim, so that is also the order they expire in.
type idempotencyMemoryStore struct {
	entries    map[string]*idempotencyMemoryEntry
	head, tail *idempotencyMemoryEntry
	expiresIn  time.Duration
	maxEntries int
	mu         sync.Mutex
}

type idempotencyMemoryEntry struct {
	expires    time.Time
	answer     *router.Recorded
	prev, next *idempotencyMemoryEntry
	key        string
	fp         []byte
}

func (s *idempotencyMemoryStore) Claim(c router.Context, key string, fp []byte) (IdempotencyEntry, bool, error) {
	held, claimed, evicted, err := s.claim(key, fp)
	if evicted > 0 {
		idempotencyLogger(c).Warn("middleware: the idempotency store is full and dropped a stored answer "+
			"before it expired; a retry with that key runs again",
			slog.Duration("left", evicted), slog.Int("max_entries", s.maxEntries))
	}
	return held, claimed, err
}

// claim reports, on top of what Claim reports, how long the answer it dropped
// to make room had left to live, or zero when it dropped none.
func (s *idempotencyMemoryStore) claim(key string, fp []byte) (IdempotencyEntry, bool, time.Duration, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	for s.head != nil && !now.Before(s.head.expires) {
		s.remove(s.head)
	}
	if e, ok := s.entries[key]; ok {
		return IdempotencyEntry{Fingerprint: e.fp, Answer: cloneRecorded(e.answer)}, false, 0, nil
	}
	var evicted time.Duration
	if len(s.entries) >= s.maxEntries {
		// A running entry stays: dropping it would let its key run twice.
		victim := s.head
		for victim != nil && victim.answer == nil {
			victim = victim.next
		}
		if victim == nil {
			return IdempotencyEntry{}, false, 0, errIdempotencyStoreFull
		}
		evicted = victim.expires.Sub(now)
		s.remove(victim)
	}

	e := &idempotencyMemoryEntry{key: key, fp: slices.Clone(fp), expires: now.Add(s.expiresIn), prev: s.tail}
	if s.tail != nil {
		s.tail.next = e
	} else {
		s.head = e
	}
	s.tail = e
	s.entries[key] = e
	return IdempotencyEntry{Fingerprint: e.fp}, true, evicted, nil
}

func (s *idempotencyMemoryStore) Complete(_ router.Context, key string, rec router.Recorded) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && e.answer == nil {
		e.answer = cloneRecorded(&rec)
	}
	return nil
}

func (s *idempotencyMemoryStore) Release(_ router.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && e.answer == nil {
		s.remove(e)
	}
	return nil
}

func (s *idempotencyMemoryStore) remove(e *idempotencyMemoryEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		s.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.tail = e.prev
	}
	e.prev, e.next = nil, nil
	delete(s.entries, e.key)
}

func cloneRecorded(rec *router.Recorded) *router.Recorded {
	if rec == nil {
		return nil
	}
	out := *rec
	out.Header = rec.Header.Clone()
	out.Body = slices.Clone(rec.Body)
	return &out
}

// addMissingCookies adds each Set-Cookie line whose cookie h does not set yet.
func addMissingCookies(h http.Header, lines []string) {
	set := h[router.HeaderSetCookie]
	for _, line := range lines {
		name := setCookieName(line)
		if !slices.ContainsFunc(set, func(v string) bool { return setCookieName(v) == name }) {
			h[router.HeaderSetCookie] = append(h[router.HeaderSetCookie], line)
		}
	}
}

func setCookieName(line string) string {
	name, _, _ := strings.Cut(line, "=")
	return strings.TrimSpace(name)
}

// idempotencyHeaderDelta reports what changed in the header while the handler
// ran: the fields that changed, the Set-Cookie lines that were added, and a
// field with no value for each one that was removed.
func idempotencyHeaderDelta(before, after http.Header) http.Header {
	delta := make(http.Header, len(after))
	for k := range before {
		if _, ok := after[k]; !ok && k != router.HeaderSetCookie {
			delta[k] = nil
		}
	}
	for k, vs := range after {
		old := before[k]
		if k == router.HeaderSetCookie {
			added := slices.DeleteFunc(slices.Clone(vs), func(v string) bool { return slices.Contains(old, v) })
			if len(added) > 0 {
				delta[k] = added
			}
			continue
		}
		if !slices.Equal(old, vs) {
			delta[k] = slices.Clone(vs)
		}
	}
	return delta
}
