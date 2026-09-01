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

const DefaultIndex = "index.html"

const immutableCacheControl = "public, max-age=31536000, immutable"

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

func Must(cfg Config) *Assets {
	a, err := New(cfg)
	if err != nil {
		panic(err.Error())
	}
	return a
}

func (a *Assets) Prefix() string {
	if a.prefix == "" {
		return "/"
	}
	return a.prefix
}

func (a *Assets) Build() string { return a.build }

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

func (a *Assets) URL(name string) string {
	name = cleanName(name)
	if name == "" {
		return a.Prefix()
	}
	u := url.URL{Path: a.urlBase + "/" + name}
	return u.EscapedPath()
}

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
