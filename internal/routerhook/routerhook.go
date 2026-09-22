// Package routerhook lets the packages of this module reach the state of a
// router.Base that the public API of package router does not expose. Package
// router fills the hooks when it initializes, and only packages of this module
// can import this one.
//
// The hooks take any, because this package cannot import router: router
// imports it. Package router checks the types.
package routerhook

import (
	"encoding/json/v2"
	"net/http"
)

var (
	// SetRoute gives b, a *router.Base, a route pattern and its parameters.
	// names and vals pair up by index.
	SetRoute func(b any, pattern string, names, vals []string)

	// FillPattern fills every parameter of a route pattern, or of a host
	// pattern when host is set, with what value reports for its name and its
	// constraint, such as "int", or "" for none. An anonymous * label of a
	// host reaches value as the name "*". It checks nothing: path values are
	// escaped, host values go in as they are, and pairs lists the names and
	// values in the order Expand takes them.
	FillPattern func(pattern string, host bool, value func(name, constraint string) string) (filled string, pairs []string)

	// JSONOptions reports the JSON options of the router that serves b, a
	// *router.Base, followed by opts, so opts win. Package sse encodes with
	// them.
	JSONOptions func(b any, opts []json.Options) []json.Options

	// InnermostWriter follows Unwrap from w to the writer net/http created.
	// [http.MaxBytesReader] closes the connection after a 413 only when it is
	// handed that writer. Package middleware caps a body with it.
	InnermostWriter func(w http.ResponseWriter) http.ResponseWriter

	// RemoveSpilledParts removes the temporary files of the multipart forms
	// that b, a *router.Base, parsed. The router does it when a request is
	// done; routertest does it for a context built outside a router.
	RemoveSpilledParts func(b any)
)
