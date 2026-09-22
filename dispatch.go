package router

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

func (r *Router[C]) preTerminal(c C) error { return r.eng.route(c, c.base().req, false) }

// ServeHTTP answers one request, which makes Router an [http.Handler]. The
// first call closes the router to further registration.
func (r *Router[C]) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := r.root
	if !root.started.Load() {
		root.freeze()
	}
	if root.observer != nil {
		root.serveObserved(w, req)
		return
	}
	c := root.acquire(w, req)
	defer root.release(c)
	if root.preChain != nil {
		//nolint:errcheck // The error handler already ran inside dispatch.
		root.dispatch(c, root.preChain)
		return
	}
	//nolint:errcheck // Same as above.
	root.eng.route(c, req, true)
}

func (r *Router[C]) serveObserved(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	c := r.acquire(w, req)
	var err error
	defer func() {
		rec := recover()
		b := c.base()
		if rec == http.ErrAbortHandler {
			b.removeSpilledParts()
			panic(rec)
		}
		if rec != nil {
			err = PanicError(rec, 0)
			r.handleError(c, err)
		}
		r.observer(c, ResolveStatus(b.res, err), b.res.Size, time.Since(start), err)
		b.removeSpilledParts()
		if rec == nil {
			r.recycle(c)
		}
	}()
	if r.preChain != nil {
		err = r.dispatch(c, r.preChain)
		return
	}
	err = r.eng.route(c, req, true)
}

func (r *Router[C]) acquire(w http.ResponseWriter, req *http.Request) C {
	var c C
	if r.pool != nil {
		c = r.pool.Get().(C)
	} else {
		c = r.newCtx(w, req)
	}
	b := c.base()
	b.init(w, req)
	b.ropts = r.ropts
	return c
}

// The pool lines repeat recycle: a call would cost a frame on every request
// that answers without a panic. A panicked context is dropped, not pooled.
// Every path removes the spilled parts of a multipart body first.
func (r *Router[C]) release(c C) {
	if rec := recover(); rec != nil {
		if rec == http.ErrAbortHandler {
			c.base().removeSpilledParts()
			panic(rec)
		}
		r.handleError(c, PanicError(rec, 0))
		c.base().removeSpilledParts()
		return
	}
	b := c.base()
	if b.deferred != nil {
		b.removeSpilledParts()
	}
	if r.pool != nil {
		r.reset(c)
		// The flag is lowered here rather than through the slow clear, so a 404
		// or a 405 on a pooled router stays on the fast path.
		b.req, b.res, b.errorHandled = releasedRequest, nil, false
		b.resStorage.ResponseWriter = nil
		if b.needsCleanup || cap(b.paramVals) > len(b.paramArr) {
			b.clearRequestSlow()
		} else {
			b.paramArr = [maxInlineParams]string{}
		}
		r.pool.Put(c)
	}
}

func (r *Router[C]) recycle(c C) {
	if r.pool != nil {
		b := c.base()
		r.reset(c)
		// The flag is lowered here rather than through the slow clear, so a 404
		// or a 405 on a pooled router stays on the fast path.
		b.req, b.res, b.errorHandled = releasedRequest, nil, false
		b.resStorage.ResponseWriter = nil
		if b.needsCleanup || cap(b.paramVals) > len(b.paramArr) {
			b.clearRequestSlow()
		} else {
			b.paramArr = [maxInlineParams]string{}
		}
		r.pool.Put(c)
	}
}

func (e *engine[C]) route(c C, req *http.Request, handleErrors bool) error {
	b := c.base()

	path, escaped := requestPath(req.URL)
	b.pathEscaped = escaped
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	// Trimmed by the same rule as the scope prefixes. Written out because route
	// is too large for the inliner to fold a call in.
	trimmed := path
	for len(trimmed) > 1 && trimmed[len(trimmed)-1] == '/' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	b.tailSlash = len(trimmed) != len(path)

	var (
		host     *hostEntry[C]
		hostVals = b.paramArr[:0]
	)
	if e.hostSet != nil {
		var hostOK bool
		b.host, hostOK = normalizeHostOK(req.Host)
		b.hostKnown = true
		if e.owner.pool != nil && b.host != "" {
			b.needsCleanup = true
		}
		if hostOK {
			host, hostVals = e.hostSet.match(b.host, hostVals)
		}
		if host != nil {
			b.hostPattern = host.pattern
			b.setRoute(nil, host.names, hostVals)
		}
	}

	if e.redirectSlash && trimmed != path && !hasDotSegmentEscaped(trimmed) && e.canMatch(host, trimmed, req.Method, hostVals[len(hostVals):], escaped) {
		redirectTo(b.res, req, trimmed, escaped)
		return nil
	}

	var (
		hostSt, anySt matchState[C]
		n             *node[C]
		vals          []string
	)
	if host != nil {
		n, vals = search(host.tree, trimmed, req.Method, hostVals, &hostSt, escaped)
	}
	if n == nil && e.anyHostRoutes {
		if m, v := search(e.tree, trimmed, req.Method, hostVals[len(hostVals):], &anySt, escaped); m != nil {
			n, vals = m, v
			b.hostPattern = ""
		}
	}

	switch {
	case n != nil:
		if n.kind == edgeWildcard && len(vals) > 0 {
			b.rawTail = vals[len(vals)-1]
			b.needsCleanup = b.needsCleanup || b.rawTail != ""
			if decoded, ok := decodePathSegment(b.rawTail, escaped); ok {
				vals[len(vals)-1] = decoded
			}
		}
		b.setRoute(n.rec, n.names, vals)
		req.Pattern = n.rec.pattern
		mh := n.lookup(req.Method)
		b.errIdx = *mh.errIdx
		h := mh.handler
		if handleErrors {
			return e.owner.dispatch(c, h)
		}
		return h(c)

	case hostSt.pathMatch != nil || anySt.pathMatch != nil:
		match, matched, skip := hostSt.pathMatch, hostSt.pathVals, len(hostVals)
		if match == nil {
			match, matched, skip = anySt.pathMatch, anySt.pathVals, 0
			host = nil
			b.hostPattern = ""
		}
		if match.kind == edgeWildcard && len(matched) > skip {
			matched = slices.Clone(matched)
			if decoded, ok := decodePathSegment(matched[len(matched)-1], escaped); ok {
				matched[len(matched)-1] = decoded
			}
		}
		b.needsCleanup = true
		b.setRoute(match.rec, match.names, matched)
		req.Pattern = match.rec.pattern
		b.res.Header().Set(HeaderAllow, e.allowHeader(&hostSt, &anySt))

		_, _, notAllowed, options, errIdx := e.fallbackChains(host, trimmed, escaped)
		b.errIdx = errIdx
		if req.Method == http.MethodOptions && e.autoOptions {
			if handleErrors {
				return e.owner.dispatch(c, options)
			}
			return options(c)
		}
		if handleErrors {
			return e.owner.dispatch(c, notAllowed)
		}
		return notAllowed(c)

	default:
		scope, notFound, _, _, errIdx := e.fallbackChains(host, trimmed, escaped)
		b.errIdx = errIdx
		if scope != nil {
			scope.bindPrefixParams(b, trimmed, escaped)
		}
		if handleErrors {
			return e.owner.dispatch(c, notFound)
		}
		return notFound(c)
	}
}

// net/url fills RawPath only when a segment carries an escaped separator such
// as %2F. Every other request has the decoded path in Path already, and
// decoding twice would turn "%252F" into a separator the client never sent.
func requestPath(u *url.URL) (path string, escaped bool) {
	// EscapedPath rebuilds the escaped form byte by byte, which is a quarter of
	// a pooled request. It is only needed when the answer can differ from Path.
	// An empty RawPath means net/url reproduces the request target from Path,
	// and the canonical form decodes every escape it would add except the ones
	// standing for a backslash or a percent, so a Path holding neither is
	// already the answer.
	if u.RawPath == "" && strings.IndexByte(u.Path, '%') < 0 && strings.IndexByte(u.Path, '\\') < 0 {
		return u.Path, false
	}
	path = canonicalEscapedPath(u.EscapedPath())
	if path == u.Path {
		return u.Path, false
	}
	return path, true
}

func canonicalEscapedPath(path string) string {
	const hex = "0123456789ABCDEF"
	i := strings.IndexByte(path, '%')
	if i < 0 {
		return path
	}
	for ; i+2 < len(path); i++ {
		if path[i] != '%' {
			continue
		}
		v, ok := unhex(path[i+1], path[i+2])
		if !ok {
			continue
		}
		preserve := v == '/' || v == '\\' || v == '%'
		if preserve && path[i+1] == hex[v>>4] && path[i+2] == hex[v&15] {
			continue
		}
		out := make([]byte, 0, len(path))
		out = append(out, path[:i]...)
		for i < len(path) {
			if i+2 < len(path) && path[i] == '%' {
				if v, ok := unhex(path[i+1], path[i+2]); ok {
					if v == '/' || v == '\\' || v == '%' {
						out = append(out, '%', hex[v>>4], hex[v&15])
					} else {
						out = append(out, v)
					}
					i += 3
					continue
				}
			}
			out = append(out, path[i])
			i++
		}
		return string(out)
	}
	return path
}

func (r *Router[C]) dispatch(c C, h HandlerFunc[C]) error {
	err := h(c)
	if err != nil && !c.base().errorHandled {
		r.handleError(c, err)
	}
	return err
}

func (r *Router[C]) handleError(c C, err error) {
	answerError(c, err, r.eng.errorHandlerFor(c.base()))
}

func (e *engine[C]) errorHandlerFor(b *Base) ErrorHandlerFunc[C] {
	return e.errHandlers[b.errIdx]
}
