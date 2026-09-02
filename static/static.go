// Package static serves static assets and builds their fingerprinted URLs.
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

// DefaultIndex is the file that answers a directory when Config.Index is
// empty.
const DefaultIndex = "index.html"

const immutableCacheControl = "public, max-age=31536000, immutable"

// Config is what [New] builds an asset set from. Set FS for an embedded set,
// or Dir for a directory it reads on every request; the two exclude each
// other, and only FS is fingerprinted.
//
// Root is a subdirectory of FS or Dir to serve from. Prefix is where the
// assets live in the URL space. Build is the tag that goes into every URL; an
// embedded set that leaves it empty gets one from the contents, so a release
// invalidates the caches of the clients by itself.
//
// SPA answers an unknown path with Index, so a client-side router can take it,
// and Fallback narrows that to the requests it recognizes as navigation.
// RedirectDir sends a directory without a trailing slash to the path with one.
// MaxAge is the cache lifetime of a set without a build tag. NotFound answers
// a path the set does not hold.
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

	// notFound always holds a handler so ServeHTTP can call it; this says
	// whether the caller chose it. Handler answers with router.ErrNotFound
	// otherwise, so the router's own 404 keeps its place.
	hasNotFound bool
}

// New reads cfg and builds the asset set. An embedded set is walked once here,
// which fingerprints every file for its ETag and builds the tag of the set.
//
// New reports an error for a cfg that names neither FS nor Dir or names both,
// a build tag that is not one path segment, an index that is not a file name,
// a directory it cannot read, and an index that is a directory.
func New(cfg Config) (*Assets, error) {
	switch {
	case cfg.FS == nil && cfg.Dir == "":
		return nil, errors.New("static: New needs Config.FS or Config.Dir")
	case cfg.FS != nil && cfg.Dir != "":
		return nil, errors.New("static: Config.FS and Config.Dir exclude each other")
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
		a.fsys = liveFS(dir)
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
	etags, sum, err := index(a.fsys)
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

// FuncMap reports an "asset" function for [template.FuncMap], so a template
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
		_, _ = sum.Write([]byte(name))
		_, _ = sum.Write(h[:])
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("static: read the asset set: %w", err)
	}
	return etags, hex.EncodeToString(sum.Sum(nil)[:6]), nil
}

type liveFS string

func (d liveFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return os.OpenInRoot(string(d), filepath.FromSlash(name))
}
