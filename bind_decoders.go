package router

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"reflect"
	"strings"
)

// BodyDecoder decodes a request body, read from r, into v. v is a pointer to
// the value that [Base.Bind] fills. An error it returns is a 400 "malformed
// body" whose cause it is, unless it is an [HTTPError], which passes through
// as it stands. [Router.Decoder] registers one.
type BodyDecoder func(r io.Reader, v any) error

// bodyDecoder is a registered BodyDecoder and the clearer of its body.
type bodyDecoder struct {
	decode  BodyDecoder
	clearer *clearer
}

// Decoder registers d to decode a body of mediaType in [Base.Bind], such as
// application/xml with encoding/xml, so Bind accepts a body of that type. A
// later call for the same type replaces d. JSON, the two form types and
// multipart stay built in and cannot be replaced.
//
// The body reaches d under the cap of [Router.MaxBodyBytes], and Bind
// validates what d filled as it validates JSON. As with JSON, a field tagged
// for another source, `param`, `query`, `header` or `form`, takes nothing
// from the body, so a client cannot set through the body what the struct
// takes from the request. The exception is a field that also carries the tag
// named by the subtype of mediaType: `xml` for application/xml, text/xml and
// any "+xml" type, `msgpack` for application/x-msgpack, `cbor` for
// application/cbor.
//
// Decoder panics on a scope, on a mounted router, or after the router started
// serving. It panics on a nil d, on a mediaType that does not parse or that
// carries parameters or a wildcard, and on a built-in type.
func (r *Router[C]) Decoder(mediaType string, d BodyDecoder) {
	r.mustBeRoot("Decoder", "it applies to the whole router")
	if d == nil {
		panic(fmt.Sprintf("router: Decoder got a nil BodyDecoder for %q; pass the function that decodes the body", mediaType))
	}
	mt := decoderMediaType(mediaType)
	defer r.guard("register a body decoder")()
	decoders := maps.Clone(r.root.ropts.decoders)
	if decoders == nil {
		decoders = make(map[string]*bodyDecoder)
	}
	decoders[mt] = &bodyDecoder{decode: d, clearer: &clearer{own: ownTag(mt)}}
	r.root.ropts.decoders = decoders
}

// decoderMediaType checks a media type that Decoder registers and reports it
// in lower case, as the request side parses it.
func decoderMediaType(mediaType string) string {
	mt, params, err := mime.ParseMediaType(mediaType)
	if err != nil {
		panic(fmt.Sprintf("router: Decoder got the media type %q, which does not parse (%v); pass a type such as application/xml", mediaType, err))
	}
	typ, sub, ok := strings.Cut(mt, "/")
	switch {
	case len(params) > 0:
		panic(fmt.Sprintf("router: Decoder got the media type %q with parameters; pass the bare type %q", mediaType, mt))
	case !ok || typ == "" || sub == "" || strings.Contains(mt, "*"):
		panic(fmt.Sprintf("router: Decoder got the media type %q; pass one type and subtype, such as application/xml, without a wildcard", mediaType))
	case isJSON(mt) || mt == MIMEApplicationForm || mt == MIMEMultipartForm:
		panic(fmt.Sprintf("router: Decoder cannot replace the built-in decoder of %s", mt))
	}
	return mt
}

// ownTag names the struct tag that marks a field as the body's own for a
// media type: the subtype, or its suffix after a "+", without an "x-".
func ownTag(mt string) string {
	_, sub, _ := strings.Cut(mt, "/")
	if i := strings.LastIndexByte(sub, '+'); i >= 0 {
		sub = sub[i+1:]
	}
	return strings.TrimPrefix(sub, "x-")
}

// decodeWith decodes the body through a registered decoder.
func (b *Base) decodeWith(d *bodyDecoder, dst any, mt string) error {
	body := countingBody{r: b.limitedBody()}
	err := d.decode(&body, dst)
	// Even a failed decode may have filled fields, so they are cleared
	// either way.
	d.clearer.clear(reflect.ValueOf(dst))
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return ErrPayloadTooLarge.WithError(err)
	}
	if _, ok := httpErrorOf(err); ok {
		return err
	}
	if body.read == 0 {
		return ErrBadRequest.WithMessage("the request body is empty").WithError(err)
	}
	return ErrBadRequest.WithMessage("malformed %s body", mt).WithError(err)
}
