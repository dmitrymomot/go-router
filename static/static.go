// Package static serves static assets and builds their fingerprinted URLs.
//
// [New] reads a [Config] into an [Assets] set, either an embedded [io/fs.FS]
// that it fingerprints once, or a live directory that it reads on every
// request. [Mount] puts the set on a router, [Handler] serves it from a route
// of your own, and [Assets.ServeHTTP] makes it an [net/http.Handler] for any
// other mux. [Assets.URL] gives the fingerprinted path of a file, and
// [Assets.FuncMap] hands it to a template.
//
//	//go:embed dist
//	var dist embed.FS
//
//	assets := static.Must(static.Config{FS: dist, Root: "dist", Prefix: "/static"})
//	static.Mount(r, assets)
//	err := serve.Run(ctx, r, serve.Config{Addr: ":8080"})
//
// A file next to a precompressed copy, "app.js" beside "app.js.br" or
// "app.js.gz", is sent compressed to a client that accepts it. See
// [Config.FS].
//
// The set refuses a path with a dot segment, such as .git or .env, unless
// Config.Dotfiles is set, for every backend.
package static

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// DefaultIndex is the file that answers a directory when Config.Index is
// empty.
const DefaultIndex = "index.html"

const immutableCacheControl = "public, max-age=31536000, immutable"

// Config is what [New] builds an asset set from. Set FS for an embedded set,
// or Dir for a directory it reads on every request; the two exclude each
// other, and only FS is fingerprinted.
//
// FS is read once, in [New]: every file is hashed for its ETag and for the
// build tag. That suits [embed.FS]. A live directory passed as os.DirFS keeps
// the ETags it had at New, so an edit made afterwards is served with a stale
// ETag; use Dir for a directory that changes. An FS set also serves a
// precompressed sibling: when the set holds name+".br" or name+".gz" and the
// client accepts br or gzip (a q of 0 refuses it), the sibling goes out with
// Content-Encoding, Vary: Accept-Encoding, the Content-Type of name and an
// ETag with the encoding as a suffix; br wins over gzip. A sibling is not an
// asset of its own: Has reports false for it, and a request for it is a 404.
//
// Root is a subdirectory of FS or Dir to serve from. Prefix is where the
// assets live in the URL space. Build is the tag that goes into every URL; an
// embedded set that leaves it empty gets one from the contents, so a release
// invalidates the caches of the clients by itself.
//
// SPA answers an unknown path with Index, so a client-side router can take it,
// and Fallback narrows that to the requests it recognizes as navigation.
// RedirectDir sends a directory without a trailing slash to the path with one.
// MaxAge is the cache lifetime of an embedded set without a build tag.
// NotFound answers a path the set does not hold, for [Assets.ServeHTTP] and
// for [Handler] alike. Without it, ServeHTTP writes a plain-text 404, and
// Handler reports [router.ErrNotFound] to the error handler of the router.
// Both backends refuse a path with a segment that starts with a dot, such as
// .git or .env, unless Dotfiles is set; an FS set does not even index such an
// entry, so it adds nothing to the ETags or the build tag.
type Config struct {
	FS          fs.FS
	Dir         string
	Root        string
	Prefix      string
	Build       string
	Index       string
	RedirectDir bool
	SPA         bool
	Fallback    func(r *http.Request) bool
	MaxAge      time.Duration
	NotFound    http.Handler
	Dotfiles    bool
}

// Assets is a set of static files and the URLs that reach them. It is safe for
// concurrent use, and it is an [http.Handler] of its own. See [Mount] to put
// it on a router.
type Assets struct {
	fsys         fs.FS
	notFound     http.Handler
	etags        map[string]string
	isNavigation func(r *http.Request) bool
	prefix       string
	build        string
	index        string
	urlBase      string
	cache        string
	spa          bool
	redirectDir  bool
	dotfiles     bool

	// notFound always holds a handler so ServeHTTP can call it; this says
	// whether the caller chose it. Handler answers with router.ErrNotFound
	// otherwise, so the router's own 404 keeps its place.
	hasNotFound bool
}

// New reads cfg and builds the asset set. An embedded set is walked once here,
// which fingerprints every file for its ETag and builds the tag of the set.
//
// New reports an error for a cfg that names neither FS nor Dir or names both,
// a Dir with a build tag or a MaxAge (its files change under the same URL, so
// only no-cache is honest), a Fallback without SPA, a build tag that is not
// one path segment, an index that is not a file name,
// a directory it cannot read, and an index that is a directory.
func New(cfg Config) (*Assets, error) {
	switch {
	case cfg.FS == nil && cfg.Dir == "":
		return nil, errors.New("static: New needs Config.FS or Config.Dir")
	case cfg.FS != nil && cfg.Dir != "":
		return nil, errors.New("static: Config.FS and Config.Dir exclude each other")
	case cfg.Dir != "" && cfg.Build != "":
		return nil, errors.New("static: a Config.Dir set changes under the same URL, so it takes no Config.Build")
	case cfg.Dir != "" && cfg.MaxAge != 0:
		return nil, errors.New("static: a Config.Dir set changes under the same URL, so it takes no Config.MaxAge")
	case cfg.Fallback != nil && !cfg.SPA:
		return nil, errors.New("static: Config.Fallback narrows the SPA fallback, so it needs Config.SPA")
	}

	a := &Assets{
		notFound:     cfg.NotFound,
		hasNotFound:  cfg.NotFound != nil,
		isNavigation: cfg.Fallback,
		prefix:       normalizePrefix(cfg.Prefix),
		build:        strings.Trim(cfg.Build, "/"),
		index:        cfg.Index,
		cache:        "no-cache",
		spa:          cfg.SPA,
		redirectDir:  cfg.RedirectDir,
		dotfiles:     cfg.Dotfiles,
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
	// "." and ".." survive Trim and then read as path segments: URL would hand
	// out /./app.js, which a browser normalises away, so cutSegment never
	// matched the tag back and the asset was served no-cache instead of
	// immutable. The index is checked the same way just below.
	if a.build == "." || a.build == ".." {
		return nil, fmt.Errorf("static: the build tag %q is not a path segment", cfg.Build)
	}
	if a.index == "." || !fs.ValidPath(a.index) {
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
		a.fsys = liveFS{dir: dir, dotfiles: cfg.Dotfiles}
		if err := validateIndex(a.fsys, a.index); err != nil {
			return nil, err
		}
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
	if err := validateIndex(a.fsys, a.index); err != nil {
		return nil, err
	}
	etags, sum, err := index(a.fsys, cfg.Dotfiles)
	if err != nil {
		return nil, err
	}
	a.etags = etags
	if a.build == "" {
		a.build = sum
	}
	if secs := int64(cfg.MaxAge / time.Second); secs > 0 {
		a.cache = "public, max-age=" + strconv.FormatInt(secs, 10)
	}
	a.setURLBase()
	return a, nil
}

func validateIndex(fsys fs.FS, name string) error {
	info, err := fs.Stat(fsys, name)
	switch {
	case err == nil && info.IsDir():
		return fmt.Errorf("static: the index %q is a directory", name)
	case err == nil, errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("static: inspect the index %q: %w", name, err)
	}
}

func (a *Assets) setURLBase() {
	a.urlBase = a.prefix
	if a.build != "" {
		a.urlBase += "/" + a.build
	}
}

// Must is [New] for a package-level variable. It panics where New reports an
// error.
func Must(cfg Config) *Assets {
	a, err := New(cfg)
	if err != nil {
		panic(err.Error())
	}
	return a
}

// Prefix reports where the assets live in the URL space, "/" when Config named
// none.
func (a *Assets) Prefix() string {
	if a.prefix == "" {
		return "/"
	}
	return a.prefix
}

// Build reports the tag that every URL of this set carries. For an embedded
// set that named none, it comes from the contents.
func (a *Assets) Build() string { return a.build }

// Has reports whether the set holds the file name.
func (a *Assets) Has(name string) bool {
	name = cleanName(name)
	if a.etags != nil {
		_, ok := a.etags[name]
		return ok && !a.isSibling(name)
	}
	f, info, err := a.openFile(name)
	if err != nil {
		return false
	}
	//nolint:errcheck // The file is only read for its type.
	defer f.Close()
	return !info.IsDir()
}

// URL reports the path that reaches the file name, with the prefix and the
// build tag. The URL changes with the contents of an embedded set, which is
// what lets the answer be cached forever.
func (a *Assets) URL(name string) string {
	name = cleanName(name)
	if name == "" {
		return a.Prefix()
	}
	u := url.URL{Path: a.urlBase + "/" + name}
	return u.EscapedPath()
}

// FuncMap reports an "asset" function for a template.FuncMap, so a template
// writes {{ asset "app.css" }} and gets the fingerprinted URL.
func (a *Assets) FuncMap() map[string]any {
	return map[string]any{"asset": a.URL}
}

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

func cleanName(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return strings.TrimPrefix(path.Clean(p), "/")
	}
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func index(fsys fs.FS, dotfiles bool) (map[string]string, string, error) {
	etags := make(map[string]string)
	sum := sha256.New()
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !dotfiles && name != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		h, err := hashFile(fsys, name)
		if err != nil {
			return err
		}
		etags[name] = `"` + hex.EncodeToString(h[:16]) + `"`
		_, _ = sum.Write([]byte(name))
		_, _ = sum.Write(h[:])
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("static: read the asset set: %w", err)
	}
	return etags, hex.EncodeToString(sum.Sum(nil)[:6]), nil
}

// hashFile streams the file into the hash, so a large asset is never held
// whole in memory.
func hashFile(fsys fs.FS, name string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	f, err := fsys.Open(name)
	if err != nil {
		return out, err
	}
	//nolint:errcheck // The file is read only.
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return out, err
	}
	h.Sum(out[:0])
	return out, nil
}

type liveFS struct {
	dir      string
	dotfiles bool
}

func (d liveFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if !d.dotfiles && hasDotSegment(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return os.OpenInRoot(d.dir, filepath.FromSlash(name))
}

// hasDotSegment reports a segment such as .git or .env. fs.ValidPath already
// refused "." and ".." inside a name.
func hasDotSegment(name string) bool {
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
