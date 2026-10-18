package session

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/dmitrymomot/go-router/cookie"
)

// ErrNotFound is what a [Store] reports for a token that names no live
// session: one it never saved, deleted, or let run out.
var ErrNotFound = errors.New("session: no such session")

// Store keeps the session records. A token is what the cookie carries.
//
// The middleware makes the token of a new session with [NewToken] and passes
// it to Save. A store on the server, such as a table of the application,
// keys the record by the token and reports it back as it stands. A store that
// keeps the record in the cookie itself, such as [CookieStore], reports the
// sealed record as the token.
//
// Load reports the data that Save stored for token, and [ErrNotFound] for a
// token that names no session or one whose expiry passed. Save stores data
// under token until expires, in place of what the token held. Delete removes
// the record, so the next Load of the token reports ErrNotFound; a token that
// names nothing is no error.
//
// A store on the server must keep its records where every process of the
// application reads them, and past a restart: a table of the database, or a
// key-value store such as Redis. A record kept in the memory of one process
// signs the user out at each deploy and on each process that did not save it,
// so the package ships no such store. Deleting a record revokes the session at
// the next request, since the middleware loads the record on every one.
//
// Any other error of a Store goes to the handler, or answers the request with
// a 500 when the middleware meets it. A Store must be safe for concurrent use.
type Store interface {
	Load(ctx context.Context, token string) ([]byte, error)
	Save(ctx context.Context, token string, data []byte, expires time.Time) (string, error)
	Delete(ctx context.Context, token string) error
}

// NewToken reports a token for a new session: 128 random bits from
// [rand.Text], which a cookie carries as it stands.
func NewToken() string {
	return rand.Text()
}

// CookieStore keeps the whole record in the cookie, sealed by a
// [cookie.Codec], so the server stores nothing. Build one with
// [NewCookieStore].
//
// It binds the record to the name of the cookie, which [CookieName] reads
// from ctx, as [cookie.Codec.Set] does: a value moved to a cookie of another
// name does not open.
//
// It cannot revoke a session: Delete does nothing, and a copy of the cookie
// stays valid until it runs out, after a logout too. Keep the Lifetime short,
// or use a store on the server where a logout must end the session at once.
// The record and the cookie together must fit in [cookie.MaxSize] bytes; a
// [Put] of a larger value reports [cookie.ErrTooLarge].
type CookieStore struct {
	codec *cookie.Codec
}

// NewCookieStore builds a [CookieStore] that seals with codec. A codec from
// [cookie.NewEncryptedCodec] hides the data from the client; one from
// [cookie.NewCodec] only signs it, and the client reads it.
//
// NewCookieStore panics if codec is nil.
func NewCookieStore(codec *cookie.Codec) *CookieStore {
	if codec == nil {
		panic("session: NewCookieStore needs a codec")
	}
	return &CookieStore{codec: codec}
}

// Load implements [Store]. A token that does not open, or that ran out,
// reports [ErrNotFound].
func (s *CookieStore) Load(ctx context.Context, token string) ([]byte, error) {
	data, err := s.codec.Decode(CookieName(ctx), token)
	if err != nil {
		return nil, ErrNotFound
	}
	return data, nil
}

// Save implements [Store]. It reports the sealed record as the token.
func (s *CookieStore) Save(ctx context.Context, _ string, data []byte, expires time.Time) (string, error) {
	return s.codec.Encode(CookieName(ctx), data, expires), nil
}

// Delete implements [Store]. It does nothing: the record lives in the cookie.
func (s *CookieStore) Delete(context.Context, string) error { return nil }
