package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
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
	// replay.
	DefaultIdempotencyMaxBody = 64 << 10
	// DefaultIdempotencyWait is how long a repeat waits for the request that
	// holds its key.
	DefaultIdempotencyWait = 10 * time.Second
	// DefaultIdempotencyExpiry is how long the memory store keeps a key.
	DefaultIdempotencyExpiry = 24 * time.Hour
	// DefaultIdempotencyMaxEntries is how many keys the memory store holds.
	DefaultIdempotencyMaxEntries = 1024
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
)

var errIdempotencyStoreFull = errors.New("middleware: every idempotency key in the store is still running")

// IdempotencyConfig configures [IdempotencyWithConfig].
//
// Store is required. Scope names whose keys these are, such as the id of the
// signed-in user, and a nil Scope or one that reports "" makes the keys global.
// An error from Scope answers 403.
//
// Fingerprint says what makes two requests the same, and a nil one takes
// [IdempotencyFormFingerprint]. An error from it is returned as it stands.
//
// Sources say where the key comes from, and an empty list reads the
// Idempotency-Key header, then the [DefaultIdempotencyFormField] form field.
// The first key found counts.
//
// MaxBody is the longest answer kept for a replay, and zero takes
// [DefaultIdempotencyMaxBody]. Wait is how long a repeat waits for the request
// that holds its key, and zero takes [DefaultIdempotencyWait]; a negative Wait
// answers 409 at once. Required refuses an unsafe request that carries no key
// with a 400.
type IdempotencyConfig[C router.Context] struct {
	Skip        func(c router.Context) bool
	Store       IdempotencyStore[C]
	Scope       func(c C) (string, error)
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
// The keys are global, so they have to be unguessable, such as a UUID the
// client makes; set Scope through IdempotencyWithConfig to give each user
// their own.
//
// Put it inside auth and CSRF; see Order in the package doc.
//
// Idempotency panics if store is nil.
func Idempotency[C router.Context](store IdempotencyStore[C]) router.Middleware[C] {
	return IdempotencyWithConfig[C](IdempotencyConfig[C]{Store: store})
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
//   - Under Required, an unsafe request without a key gets 400. So does a key
//     longer than [MaxIdempotencyKeyLength] bytes.
//
// The causes of these answers are [ErrIdempotencyKeyReused],
// [ErrIdempotencyInProgress] and [ErrIdempotencyKeyRequired]. GET, HEAD,
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
// A replay carries the header fields the handler set, and the Set-Cookie lines
// it added, on top of what the middleware in front set for the repeat. It
// never calls the error handler itself: an error goes back up the chain.
//
// The default fingerprint covers the method, the path and a form body; see
// [IdempotencyFormFingerprint]. Set Fingerprint for a JSON body.
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
	if cfg.Fingerprint == nil {
		cfg.Fingerprint = func(c C) ([]byte, error) { return IdempotencyFormFingerprint(c) }
	}

	return func(next router.HandlerFunc[C]) router.HandlerFunc[C] {
		return func(c C) error {
			if skipped(cfg.Skip, c) || idempotentMethod(c.Request().Method) {
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
			var scope string
			if cfg.Scope != nil {
				var err error
				if scope, err = cfg.Scope(c); err != nil {
					return router.ErrForbidden.WithMessage("the client is not identified").WithError(err)
				}
			}
			key = idempotencyStoreKey(scope, key)

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

// idempotentMethod reports the methods RFC 9110 calls safe, which a repeat
// cannot harm.
func idempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, router.MethodQuery:
		return true
	}
	return false
}

// The backoff of a repeat that waits for the request holding its key.
const (
	idempotencyFirstPoll = 10 * time.Millisecond
	idempotencyMaxPoll   = 200 * time.Millisecond
)

// claimIdempotencyKey reports claimed, or an entry that holds an answer with
// the same fingerprint. The fingerprint is compared before any wait, so a
// mismatch is refused while the first request still runs.
func claimIdempotencyKey[C router.Context](
	c C, store IdempotencyStore[C], wait time.Duration, key string, fp []byte,
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
	c C, store IdempotencyStore[C], maxBody int, key string, next router.HandlerFunc[C],
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
		if k == headerSetCookie {
			h[k] = append(h[k], vs...)
			continue
		}
		h[k] = slices.Clone(vs)
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
	if b, ok := router.FromContext(c); ok {
		return b.Logger()
	}
	return slog.Default()
}

// IdempotencyFormFingerprint is the default fingerprint of
// [IdempotencyWithConfig]: a SHA-256 of the method, the path and, for a
// urlencoded or multipart body, the form. The order of the fields does not
// count, and neither do the [DefaultCSRFFormField] and
// [DefaultIdempotencyFormField] fields. An uploaded file counts by its field,
// name, size and type, not by its content.
//
// The query, the host and any other body are not covered, so a JSON API sets
// a Fingerprint of its own that hashes the body too.
//
// An error comes from reading the form, and is a 400 or a 413.
func IdempotencyFormFingerprint(c router.Context) ([]byte, error) {
	req := c.Request()
	buf := appendIdempotencyFields(nil, req.Method, req.URL.Path)

	b, ok := router.FromContext(c)
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get(router.HeaderContentType))
	if ok && (mediaType == router.MIMEApplicationForm || mediaType == router.MIMEMultipartForm) {
		form, err := b.FormValues()
		if err != nil {
			return nil, err
		}
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
		if req.MultipartForm != nil {
			files = req.MultipartForm.File
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
	}
	sum := sha256.Sum256(buf)
	return sum[:], nil
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

// idempotencyStoreKey puts the length of the scope in front, so no scope and
// key can pass for another pair. A global key is the pair with no scope, so a
// client cannot name the key of a user either.
func idempotencyStoreKey(scope, key string) string {
	return strconv.Itoa(len(scope)) + ":" + scope + key
}

// IdempotencyEntry is what an [IdempotencyStore] holds for one key.
//
//betteralign:check
type IdempotencyEntry struct {
	// Fingerprint is the fingerprint of the request that claimed the key. The
	// caller must not change it.
	Fingerprint []byte
	// Answer is nil while that request still runs.
	Answer *router.Recorded
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
// Keep every field of the [router.Recorded], Truncated included.
//
// An error from Claim answers 500. An error from Complete or Release is
// logged, and the key stays held until it expires.
//
// [NewIdempotencyMemoryStore] holds the keys in this process. Implement the
// interface to share them across several, such as through Redis.
type IdempotencyStore[C router.Context] interface {
	Claim(c C, key string, fp []byte) (held IdempotencyEntry, claimed bool, err error)
	Complete(c C, key string, rec router.Recorded) error
	Release(c C, key string) error
}

// IdempotencyMemoryStoreConfig configures [NewIdempotencyMemoryStoreWithConfig].
// ExpiresIn is how long a key lives, counted from its claim, and zero or less
// takes [DefaultIdempotencyExpiry]. MaxEntries caps how many keys the store
// holds, and zero takes [DefaultIdempotencyMaxEntries].
type IdempotencyMemoryStoreConfig struct {
	ExpiresIn  time.Duration
	MaxEntries int
}

// NewIdempotencyMemoryStore builds an [IdempotencyStore] in this process that
// keeps a key for expiresIn after its claim. An expiresIn of zero or less takes
// [DefaultIdempotencyExpiry].
//
// The keys live in one process, so several instances each hold their own.
func NewIdempotencyMemoryStore[C router.Context](expiresIn time.Duration) IdempotencyStore[C] {
	return NewIdempotencyMemoryStoreWithConfig[C](IdempotencyMemoryStoreConfig{ExpiresIn: expiresIn})
}

// NewIdempotencyMemoryStoreWithConfig is [NewIdempotencyMemoryStore] with a
// configuration, which also caps how many keys the store holds.
//
// A full store makes room by dropping the oldest key whose answer is stored. A
// store full of running keys refuses a new one, and the request gets a 500. It
// holds up to MaxEntries answers of up to the MaxBody of the middleware each.
//
// A key lives for ExpiresIn from its claim, however long its request runs. A
// request that outlives it can store its answer on, or release, the claim of a
// later request with the same key.
//
// NewIdempotencyMemoryStoreWithConfig panics on a negative MaxEntries.
func NewIdempotencyMemoryStoreWithConfig[C router.Context](cfg IdempotencyMemoryStoreConfig) IdempotencyStore[C] {
	if cfg.MaxEntries < 0 {
		panic("middleware: NewIdempotencyMemoryStoreWithConfig needs a MaxEntries of zero or more")
	}
	if cfg.ExpiresIn <= 0 {
		cfg.ExpiresIn = DefaultIdempotencyExpiry
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = DefaultIdempotencyMaxEntries
	}
	return &idempotencyMemoryStore[C]{
		entries:    make(map[string]*idempotencyMemoryEntry),
		expiresIn:  cfg.ExpiresIn,
		maxEntries: cfg.MaxEntries,
	}
}

// The entries form a list in the order of their claims. Every entry lives for
// the same time from its claim, so that is also the order they expire in.
type idempotencyMemoryStore[C router.Context] struct {
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

func (s *idempotencyMemoryStore[C]) Claim(_ C, key string, fp []byte) (IdempotencyEntry, bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	for s.head != nil && !now.Before(s.head.expires) {
		s.remove(s.head)
	}
	if e, ok := s.entries[key]; ok {
		return IdempotencyEntry{Fingerprint: e.fp, Answer: cloneRecorded(e.answer)}, false, nil
	}
	if len(s.entries) >= s.maxEntries {
		// A running entry stays: dropping it would let its key run twice.
		victim := s.head
		for victim != nil && victim.answer == nil {
			victim = victim.next
		}
		if victim == nil {
			return IdempotencyEntry{}, false, errIdempotencyStoreFull
		}
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
	return IdempotencyEntry{Fingerprint: e.fp}, true, nil
}

func (s *idempotencyMemoryStore[C]) Complete(_ C, key string, rec router.Recorded) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && e.answer == nil {
		e.answer = cloneRecorded(&rec)
	}
	return nil
}

func (s *idempotencyMemoryStore[C]) Release(_ C, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && e.answer == nil {
		s.remove(e)
	}
	return nil
}

func (s *idempotencyMemoryStore[C]) remove(e *idempotencyMemoryEntry) {
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

// headerSetCookie is the one header whose lines add up rather than replace
// each other.
const headerSetCookie = "Set-Cookie"

// idempotencyHeaderDelta reports what the handler added to the header: the
// fields it changed, and the Set-Cookie lines it added.
func idempotencyHeaderDelta(before, after http.Header) http.Header {
	delta := make(http.Header, len(after))
	for k, vs := range after {
		old := before[k]
		if k == headerSetCookie {
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
