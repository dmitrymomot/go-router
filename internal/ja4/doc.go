// Package ja4 computes the JA4 fingerprint of a TLS ClientHello, captures it
// from the connections of a listener, and carries it to the request context.
// Package serve wraps its listener and exports the reader, and the RealIP
// middleware puts the fingerprint that a trusted CDN reports in its place.
//
// The parser and the JA4 assembly are ported from web/fingerprint/tlsprint of
// dmitrymomot/forge, and follow the JA4 spec of FoxIO.
package ja4
