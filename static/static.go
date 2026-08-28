// Package static serves the static assets of an application and builds the
// URLs that a template writes for them.
//
// It has two modes, which differ in where the bytes come from and in how long
// a browser may keep them:
//
//   - [Config.Dir] reads a directory on disk, once per request. An edit is
//     visible on the next reload, so this is the mode of a development build.
//   - [Config.FS] answers from an embedded file system. The URL of an asset
//     then carries a build tag, and a versioned answer is immutable, so the
//     browser caches it for a year and the next build invalidates it.
//
// A program picks the mode with a build tag of its own, so that one call site
// serves both builds:
//
//	//go:build dev
//
//	func assets() static.Config { return static.Config{Dir: "web/dist", Prefix: "/static"} }
//
//	//go:build !dev
//
//	//go:embed all:dist
//	var dist embed.FS
//
//	func assets() static.Config { return static.Config{FS: dist, Root: "dist", Prefix: "/static"} }
//
// Register the assets on a router with [Mount], and write their URLs from a
// template with [Assets.FuncMap]:
//
//	a := static.Must(assets())
//	static.Mount(r, a)
//
//	tmpl.Funcs(a.FuncMap())     // {{ asset "css/app.css" }}
//
// Set [Config.SPA] to serve a single page application: a request that matches
// no file then answers with the index file, which is how the application
// serves its own routes.
package static

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultIndex is the file that answers a request for a directory.
const DefaultIndex = "index.html"

// immutableCacheControl is the answer for a path that carried the build tag.
// Such a path names one build, so the browser never has to ask again.
const immutableCacheControl = "public, max-age=31536000, immutable"

// Config configures an asset set. Set either FS or Dir, never both.
type Config struct {
	// FS holds the assets, normally an embed.FS. New reads every file once,
	// to derive the build tag and one ETag per file, so the files must not
	// change afterwards.
	FS fs.FS

	// Dir reads the assets from a directory on disk, once per request. Use it
	// for a development build, where an edit has to reach the next reload.
	Dir string

	// Root is the subdirectory of FS or Dir that holds the assets. A
	// "//go:embed all:dist" keeps the directory name in every path, so that
	// embedding wants Root "dist".
	Root string

	// Prefix is the URL path that the assets answer under, such as "/static".
	// An empty prefix serves them at the root, which is what a single page
	// application wants.
	Prefix string

	// Build is the token that [Assets.URL] puts in front of the file name:
	// "/static/9f2c1ab40e3d/css/app.css". FS mode derives one from the content
	// of the assets when this is empty. Dir mode leaves the path unversioned,
	// because a development build has nothing to invalidate.
	//
	// A path without the token still answers, with a Cache-Control of MaxAge
	// instead of an immutable one.
	Build string

	// Index is the file that answers a request for a directory, and the file
	// that SPA falls back to. It defaults to [DefaultIndex].
	Index string

	// RedirectDir answers a request for a directory whose URL carries no
	// trailing slash with a 301 to the same path and a slash. "/docs" and
	// "/docs/" are different bases for a relative link, so an index that
	// writes "assets/app.css" reaches the wrong file under the unslashed one.
	//
	// The Location is relative, so it keeps the prefix that [http.StripPrefix]
	// and the MountHandler method of the router removed from the path before
	// this package saw it.
	//
	// It answers only for a directory that holds Index. A directory without
	// one keeps its 404, and SPA keeps answering it with the index of the
	// application, which is what makes this safe to set there.
	RedirectDir bool

	// SPA answers a request that matches no file with Index and status 200.
	//
	// The fallback answers a navigation only: the path carries no file
	// extension, or the client accepts text/html. A missing script or style
	// sheet keeps its 404, because an HTML body in its place breaks the page
	// in a way that is hard to read.
	SPA bool

	// Fallback replaces that navigation test. It runs only while SPA is set,
	// and it decides whether this request answers with Index instead of 404.
	//
	// Set it when the routes of the application do not fit the default, such
	// as a route that ends in something the default reads as a file
	// extension:
	//
	//	Fallback: func(r *http.Request) bool {
	//		return !strings.HasPrefix(r.URL.Path, "/api/")
	//	}
	Fallback func(r *http.Request) bool

	// MaxAge is how long a browser may keep an answer whose path carried no
	// build tag. It defaults to zero, which asks the browser to revalidate.
	// Dir mode ignores it and always revalidates, and so does a MaxAge below
	// one second, because "max-age=0" lets a shared cache keep the answer
	// where "no-cache" makes it ask again.
	//
	// The index file always revalidates, because it names the versioned
	// assets.
	MaxAge time.Duration

	// NotFound answers a request for a file that the asset set does not hold.
	// It defaults to [http.NotFoundHandler].
	//
	// [Handler] and [Mount] ignore it and return router.ErrNotFound, so that
	// the error handler of the router renders the answer.
	NotFound http.Handler
}

// Assets is a set of static files that answers requests and builds URLs. It
// implements [http.Handler]; the path that ServeHTTP reads carries no prefix,
// the way [http.StripPrefix] and the MountHandler method of the router hand it
// over.
//
// An Assets is read only after [New] returns it, so every method is safe for
// concurrent use.
type Assets struct {
	fsys     fs.FS
	notFound http.Handler

	// etags holds one ETag per file. It is nil in Dir mode, where the ETag
	// comes from the size and the modification time instead.
	etags map[string]string

	// isNavigation is Config.Fallback. It is nil while the default rule
	// decides which miss the index answers.
	isNavigation func(r *http.Request) bool

	prefix string
	build  string
	index  string

	// urlBase is the prefix joined with the build tag, so that URL appends a
	// name and nothing else.
	urlBase string

	// cache is the Cache-Control of an answer whose path carried no build tag.
	cache string

	spa bool

	// redirectDir is Config.RedirectDir.
	redirectDir bool
}

// New returns the asset set that cfg describes. It reports an error when the
// config names neither a file system nor a directory, when it names both, or
// when the assets are unreadable.
func New(cfg Config) (*Assets, error) {
	switch {
	case cfg.FS == nil && cfg.Dir == "":
		return nil, errors.New("static: New needs Config.FS or Config.Dir")
	case cfg.FS != nil && cfg.Dir != "":
		return nil, errors.New("static: Config.FS and Config.Dir exclude each other")
	}

	a := &Assets{
		notFound:     cfg.NotFound,
		isNavigation: cfg.Fallback,
		prefix:       normalizePrefix(cfg.Prefix),
		build:        strings.Trim(cfg.Build, "/"),
		index:        cfg.Index,
		cache:        "no-cache",
		spa:          cfg.SPA,
		redirectDir:  cfg.RedirectDir,
	}
	if a.notFound == nil {
		a.notFound = http.NotFoundHandler()
	}
	if a.index == "" {
		a.index = DefaultIndex
	}
	if strings.Contains(a.build, "/") {
		return nil, fmt.Errorf("static: the build tag %q spans more than one path segment", cfg.Build)
	}
	// An index that no file system can open answers 404 for every directory
	// and for every fallback, which reads as a missing page rather than as
	// the misconfiguration it is.
	if !fs.ValidPath(a.index) {
		return nil, fmt.Errorf("static: the index %q is not a file name inside the asset set", cfg.Index)
	}

	if live := cfg.Dir != ""; live {
		dir := cfg.Dir
		if root := path.Clean("/" + cfg.Root); root != "/" {
			dir = filepath.Join(dir, filepath.FromSlash(root[1:]))
		}
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("static: read the asset directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("static: %s is not a directory", dir)
		}
		a.fsys = liveFS(dir)
		a.setURLBase()
		return a, nil
	}

	a.fsys = cfg.FS
	if root := path.Clean("/" + cfg.Root); root != "/" {
		sub, err := fs.Sub(a.fsys, root[1:])
		if err != nil {
			return nil, fmt.Errorf("static: open the asset root %q: %w", cfg.Root, err)
		}
		a.fsys = sub
	}
	etags, sum, err := index(a.fsys)
	if err != nil {
		return nil, err
	}
	a.etags = etags
	if a.build == "" {
		a.build = sum
	}
	// A MaxAge under a second truncates to "max-age=0", which a shared cache
	// may store. Leave the stricter default in place instead.
	if secs := int64(cfg.MaxAge / time.Second); secs > 0 {
		a.cache = "public, max-age=" + strconv.FormatInt(secs, 10)
	}
	a.setURLBase()
	return a, nil
}

// setURLBase joins the prefix and the build tag once. Both are fixed after New
// returns, so URL never has to build them again.
func (a *Assets) setURLBase() {
	a.urlBase = a.prefix
	if a.build != "" {
		a.urlBase += "/" + a.build
	}
}

// Must returns the asset set that cfg describes and panics when [New] reports
// an error. Use it in a package level variable or in main, where a broken
// asset set has to stop the program anyway.
func Must(cfg Config) *Assets {
	a, err := New(cfg)
	if err != nil {
		panic(err.Error())
	}
	return a
}

// Prefix returns the URL prefix of the asset set, normalized. It is "/" when
// the assets answer at the root.
func (a *Assets) Prefix() string {
	if a.prefix == "" {
		return "/"
	}
	return a.prefix
}

// Build returns the build tag that [Assets.URL] writes into a path. It is
// empty when the paths carry no tag, which is the default of Dir mode.
func (a *Assets) Build() string { return a.build }

// Has reports whether the asset set holds the named file.
func (a *Assets) Has(name string) bool {
	name = cleanName(name)
	if a.etags != nil {
		_, ok := a.etags[name]
		return ok
	}
	f, info, err := a.openFile(name)
	if err != nil {
		return false
	}
	//nolint:errcheck // The file is only read for its type.
	defer f.Close()
	return !info.IsDir()
}

// URL returns the public URL of the named asset:
//
//	a.URL("css/app.css")   // "/static/9f2c1ab40e3d/css/app.css"
//
// The URL carries the build tag when the asset set has one. URL does not check
// that the asset exists; use [Assets.Has] for that.
func (a *Assets) URL(name string) string {
	name = cleanName(name)
	if name == "" {
		return a.Prefix()
	}
	// EscapedPath percent encodes what a path may not carry raw, such as a
	// space or a "#", and returns the string untouched when nothing needs it.
	// text/template writes the result as it stands, so URL cannot leave that
	// to the html/template escaper.
	u := url.URL{Path: a.urlBase + "/" + name}
	return u.EscapedPath()
}

// FuncMap returns the template functions of the asset set: "asset" is
// [Assets.URL].
//
//	tmpl := template.Must(template.New("page").Funcs(a.FuncMap()).Parse(page))
//
//	<link rel="stylesheet" href="{{ asset "css/app.css" }}">
//
// The map type is map[string]any, which both [text/template.FuncMap] and
// [html/template.FuncMap] accept.
func (a *Assets) FuncMap() map[string]any {
	return map[string]any{"asset": a.URL}
}

// normalizePrefix gives a prefix a leading "/" and removes a trailing one. The
// root prefix becomes the empty string, so that a join never doubles a slash.
func normalizePrefix(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if p[0] != '/' {
		p = "/" + p
	}
	return p
}

// cleanName turns a URL path into a path inside the asset set. The leading
// slash makes Clean drop every "..", so the result never escapes the set.
func cleanName(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

// index reads every file of fsys once. It returns one ETag per file and a
// build tag that changes whenever any file changes.
//
// An embedded file system reports no modification time, so the ETag has to
// come from the content. Reading the set once at startup is the price of a
// correct conditional request afterwards.
func index(fsys fs.FS) (map[string]string, string, error) {
	etags := make(map[string]string)
	sum := sha256.New()
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		etags[name] = `"` + hex.EncodeToString(h[:16]) + `"`
		// WalkDir visits in lexical order, so the same set always folds into
		// the same build tag.
		_, _ = sum.Write([]byte(name))
		_, _ = sum.Write(h[:])
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("static: read the asset set: %w", err)
	}
	return etags, hex.EncodeToString(sum.Sum(nil)[:6]), nil
}

// liveFS opens a file under a directory on every call, so that an edit on disk
// reaches the next request. It resolves the path through [os.OpenInRoot],
// which refuses a path that leaves the directory, including through a symlink.
//
// It opens the directory per request rather than holding one handle, so that a
// build which replaces the whole directory keeps working.
type liveFS string

// Open implements [fs.FS].
func (d liveFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return os.OpenInRoot(string(d), filepath.FromSlash(name))
}
