package static_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dmitrymomot/go-router/static"
)

const immutableCache = "public, max-age=31536000, immutable"

const (
	rootIndex = "<html>root</html>"
	subIndex  = "<html>sub</html>"
	appCSS    = "body{color:red}"
	appJS     = "console.log(1)"
)

func assetFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte(rootIndex)},
		"css/app.css":    {Data: []byte(appCSS)},
		"js/app.js":      {Data: []byte(appJS)},
		"sub/index.html": {Data: []byte(subIndex)},
	}
}

func assetDir(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	for name, f := range assetFS() {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatalf("make the asset directory: %v", err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			tb.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func newAssets(tb testing.TB, cfg static.Config) *static.Assets {
	tb.Helper()
	a, err := static.New(cfg)
	if err != nil {
		tb.Fatalf("New: %v", err)
	}
	return a
}

type reqOption func(*http.Request)

func header(key, value string) reqOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

func acceptHeaders(values ...string) reqOption {
	return func(r *http.Request) {
		for _, value := range values {
			r.Header.Add("Accept", value)
		}
	}
}

func method(m string) reqOption {
	return func(r *http.Request) { r.Method = m }
}

func get(h http.Handler, target string, opts ...reqOption) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewNeedsASource(t *testing.T) {
	if _, err := static.New(static.Config{}); err == nil {
		t.Fatal("New accepted a config with neither FS nor Dir")
	}
}

func TestNewRefusesBothSources(t *testing.T) {
	_, err := static.New(static.Config{FS: assetFS(), Dir: t.TempDir()})
	if err == nil {
		t.Fatal("New accepted both FS and Dir")
	}
}

func TestNewRefusesAMissingDirectory(t *testing.T) {
	_, err := static.New(static.Config{Dir: filepath.Join(t.TempDir(), "absent")})
	if err == nil {
		t.Fatal("New accepted a directory that does not exist")
	}
}

func TestNewRefusesAFileAsTheDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write the file: %v", err)
	}
	if _, err := static.New(static.Config{Dir: p}); err == nil {
		t.Fatal("New accepted a file as the asset directory")
	}
}

func TestNewRefusesABuildTagWithASlash(t *testing.T) {
	_, err := static.New(static.Config{FS: assetFS(), Build: "a/b"})
	if err == nil {
		t.Fatal("New accepted a build tag that spans two segments")
	}
}

func TestNewRefusesAnAbsentRoot(t *testing.T) {
	_, err := static.New(static.Config{FS: assetFS(), Root: "absent"})
	if err == nil {
		t.Fatal("New accepted a root that the file system does not hold")
	}
}

func TestRootSelectsASubdirectory(t *testing.T) {
	fsys := fstest.MapFS{"dist/css/app.css": {Data: []byte(appCSS)}}
	a := newAssets(t, static.Config{FS: fsys, Root: "dist"})

	if rec := get(a, "/css/app.css"); rec.Body.String() != appCSS {
		t.Fatalf("body = %q, want the file under the root", rec.Body.String())
	}
}

func TestServeAFile(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/css/app.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != appCSS {
		t.Errorf("body = %q, want %q", rec.Body.String(), appCSS)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content type = %q, want text/css", ct)
	}
	if rec.Header().Get("Etag") == "" {
		t.Error("no ETag; an embedded file has no modification time to fall back on")
	}
}

func TestUnversionedPathRevalidates(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/css/app.css")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control = %q, want no-cache", got)
	}
}

func TestVersionedPathIsImmutable(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})

	url := a.URL("css/app.css")
	want := "/static/" + a.Build() + "/css/app.css"
	if url != want {
		t.Fatalf("URL = %q, want %q", url, want)
	}

	rec := get(a, "/"+a.Build()+"/css/app.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != appCSS {
		t.Errorf("body = %q, want %q", rec.Body.String(), appCSS)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("cache control = %q, want an immutable one", got)
	}
}

func TestAStaleBuildTagIsNotFound(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/deadbeef0000/css/app.css")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for the tag of an older build", rec.Code)
	}
}

func TestTheBuildTagFollowsTheContent(t *testing.T) {
	first := newAssets(t, static.Config{FS: assetFS()})

	changed := assetFS()
	changed["css/app.css"] = &fstest.MapFile{Data: []byte("body{color:blue}")}
	second := newAssets(t, static.Config{FS: changed})

	if first.Build() == second.Build() {
		t.Fatal("the build tag did not change with the content")
	}
	if first.Build() != newAssets(t, static.Config{FS: assetFS()}).Build() {
		t.Fatal("the build tag of one file set is not stable")
	}
}

func TestAnExplicitBuildTagWins(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Build: "v3"})

	if a.Build() != "v3" {
		t.Fatalf("Build = %q, want v3", a.Build())
	}
	if rec := get(a, "/v3/js/app.js"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestIndexAnswersTheRoot(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/")
	if rec.Code != http.StatusOK || rec.Body.String() != rootIndex {
		t.Fatalf("status = %d, body = %q, want the index", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("cache control = %q; the index names the versioned assets", got)
	}
}

func TestIndexAnswersADirectory(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/sub")
	if rec.Body.String() != subIndex {
		t.Fatalf("body = %q, want the index of the directory", rec.Body.String())
	}
}

func TestADirectoryWithoutAnIndexIsNotFound(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	if rec := get(a, "/css"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; the package lists no directory", rec.Code)
	}
}

func TestADirectoryValuedNestedIndexIsNotFound(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":             {Data: []byte(rootIndex)},
		"sub/index.html/file.js": {Data: []byte(appJS)},
	}
	a := newAssets(t, static.Config{FS: fsys})

	if rec := get(a, "/sub"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a directory-valued index", rec.Code)
	}
}

func TestAMissingFileIsNotFound(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	if rec := get(a, "/css/absent.css"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestConfigNotFoundAnswersAMiss(t *testing.T) {
	a := newAssets(t, static.Config{
		FS: assetFS(),
		NotFound: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusGone)
			//nolint:errcheck // The recorder never fails.
			w.Write([]byte("gone"))
		}),
	})

	rec := get(a, "/absent.css")
	if rec.Code != http.StatusGone || rec.Body.String() != "gone" {
		t.Fatalf("status = %d, body = %q, want the configured handler", rec.Code, rec.Body.String())
	}
}

func TestOnlyGETAndHEADAnswer(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/css/app.css", method(http.MethodPost))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("allow = %q, want GET, HEAD", got)
	}
}

func TestHEADWritesNoBody(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/css/app.css", method(http.MethodHead))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want none", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != "15" {
		t.Errorf("content length = %q, want the size of the file", got)
	}
}

func TestConditionalRequestAnswers304(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	tag := get(a, "/css/app.css").Header().Get("Etag")
	rec := get(a, "/css/app.css", header("If-None-Match", tag))
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 for the ETag of the answer", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got == "" {
		t.Error("the 304 carries no Cache-Control")
	}
}

func TestRangeRequest(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/css/app.css", header("Range", "bytes=0-3"))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != appCSS[:4] {
		t.Errorf("body = %q, want the first four bytes", rec.Body.String())
	}
}

func TestPathTraversalStaysInside(t *testing.T) {
	fsys := assetFS()
	fsys["secret.txt"] = &fstest.MapFile{Data: []byte("secret")}
	a := newAssets(t, static.Config{FS: fsys, Root: "css"})

	for _, target := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/a/../../secret.txt"} {
		rec := get(a, target)
		if rec.Body.String() == "secret" {
			t.Fatalf("%s escaped the asset root", target)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
		}
	}
}

func TestMaxAgeCachesAnUnversionedAnswer(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), MaxAge: time.Hour})

	if got := get(a, "/css/app.css").Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("cache control = %q, want public, max-age=3600", got)
	}
	if got := get(a, "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("cache control of the index = %q, want no-cache", got)
	}
}

func TestSPAAnswersADeepLink(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})

	rec := get(a, "/orders/7/edit", header("Accept", "text/html,application/xhtml+xml"))
	if rec.Code != http.StatusOK || rec.Body.String() != rootIndex {
		t.Fatalf("status = %d, body = %q, want the index", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("cache control = %q, want no-cache", got)
	}
}

func TestSPAAnswersAPathWithoutAnExtension(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})

	if rec := get(a, "/dashboard"); rec.Body.String() != rootIndex {
		t.Fatalf("body = %q, want the index", rec.Body.String())
	}
}

func TestSPAKeepsThe404OfAMissingScript(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})

	rec := get(a, "/js/absent.js", header("Accept", "*/*"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; an HTML body would break the page", rec.Code)
	}
}

func TestSPAIsOffByDefault(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/dashboard", header("Accept", "text/html"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without Config.SPA", rec.Code)
	}
}

func TestSPAWithoutAnIndexIsNotFound(t *testing.T) {
	a := newAssets(t, static.Config{
		FS:  fstest.MapFS{"css/app.css": {Data: []byte(appCSS)}},
		SPA: true,
	})

	if rec := get(a, "/dashboard"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when the set holds no index", rec.Code)
	}
}

func TestDirModeReadsTheNextEdit(t *testing.T) {
	dir := assetDir(t)
	a := newAssets(t, static.Config{Dir: dir})

	if got := get(a, "/css/app.css").Body.String(); got != appCSS {
		t.Fatalf("body = %q, want %q", got, appCSS)
	}

	changed := "body{color:blue}"
	p := filepath.Join(dir, "css", "app.css")
	if err := os.WriteFile(p, []byte(changed), 0o644); err != nil {
		t.Fatalf("rewrite the file: %v", err)
	}
	if got := get(a, "/css/app.css").Body.String(); got != changed {
		t.Fatalf("body = %q, want the edit %q", got, changed)
	}
}

func TestDirModeAddsNoBuildTag(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t), Prefix: "/static"})

	if a.Build() != "" {
		t.Fatalf("Build = %q, want none in Dir mode", a.Build())
	}
	if got := a.URL("css/app.css"); got != "/static/css/app.css" {
		t.Fatalf("URL = %q, want /static/css/app.css", got)
	}
}

func TestDirModeAlwaysRevalidates(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t), MaxAge: time.Hour})

	rec := get(a, "/css/app.css")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control = %q, want no-cache", got)
	}
	if tag := rec.Header().Get("Etag"); !strings.HasPrefix(tag, `W/"`) {
		t.Errorf("etag = %q, want a weak one from the size and the time", tag)
	}
}

func TestDirModeAnswersAConditionalRequest(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t)})

	tag := get(a, "/css/app.css").Header().Get("Etag")
	if rec := get(a, "/css/app.css", header("If-None-Match", tag)); rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
}

func TestDirModeRefusesASymlinkOutOfTheDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "assets")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("make the asset directory: %v", err)
	}
	secret := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write the secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("this file system has no symlinks: %v", err)
	}

	a := newAssets(t, static.Config{Dir: dir})
	if rec := get(a, "/link.txt"); rec.Body.String() == "secret" {
		t.Fatal("a symlink read a file outside the asset directory")
	}
}

func TestDirModeRootSelectsASubdirectory(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t), Root: "css"})

	if got := get(a, "/app.css").Body.String(); got != appCSS {
		t.Fatalf("body = %q, want the file under the root", got)
	}
}

func TestPrefixIsNormalized(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"static", "/static"},
		{"/static/", "/static"},
	}
	for _, tc := range tests {
		a := newAssets(t, static.Config{Dir: assetDir(t), Prefix: tc.in})
		if got := a.Prefix(); got != tc.want {
			t.Errorf("Prefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestURLAtTheRootPrefix(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t)})

	if got := a.URL("css/app.css"); got != "/css/app.css" {
		t.Fatalf("URL = %q, want /css/app.css", got)
	}
	if got := a.URL(""); got != "/" {
		t.Fatalf("URL(\"\") = %q, want /", got)
	}
}

func TestURLCleansTheName(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t), Prefix: "/static"})

	if got := a.URL("/css/../css/app.css"); got != "/static/css/app.css" {
		t.Fatalf("URL = %q, want /static/css/app.css", got)
	}
}

func TestHasReportsTheFiles(t *testing.T) {
	for name, a := range map[string]*static.Assets{
		"fs":  newAssets(t, static.Config{FS: assetFS()}),
		"dir": newAssets(t, static.Config{Dir: assetDir(t)}),
	} {
		if !a.Has("css/app.css") {
			t.Errorf("%s: Has(css/app.css) = false", name)
		}
		if a.Has("css/absent.css") {
			t.Errorf("%s: Has(css/absent.css) = true", name)
		}
		if a.Has("css") {
			t.Errorf("%s: Has reported a directory", name)
		}
	}
}

func TestFuncMapWritesTheURL(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static"})

	fn, ok := a.FuncMap()["asset"].(func(string) string)
	if !ok {
		t.Fatal("FuncMap holds no asset function")
	}
	if got := fn("css/app.css"); got != a.URL("css/app.css") {
		t.Fatalf("asset = %q, want %q", got, a.URL("css/app.css"))
	}
}

func TestMustPanicsOnABrokenConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Must did not panic on a config with no source")
		}
	}()
	static.Must(static.Config{})
}

type plainFile struct {
	io.Reader
	info     fs.FileInfo
	closer   io.Closer
	reads    *atomic.Int64
	cancel   context.CancelFunc
	cancelAt int64
}

func (f plainFile) Read(p []byte) (int, error) {
	var reads int64
	if f.reads != nil {
		reads = f.reads.Add(1)
	}
	n, err := f.Reader.Read(p)
	if f.cancel != nil && reads >= f.cancelAt {
		f.cancel()
	}
	return n, err
}

func (f plainFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f plainFile) Close() error {
	if f.closer != nil {
		return f.closer.Close()
	}
	return nil
}

type plainFS struct {
	fs.FS
	broken   atomic.Bool
	reads    atomic.Int64
	cancel   context.CancelFunc
	cancelAt int64
}

func (p *plainFS) Open(name string) (fs.File, error) {
	f, err := p.FS.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return f, err
	}
	if p.broken.Load() {
		return plainFile{
			Reader: errReader{}, info: info, closer: f, reads: &p.reads, cancel: p.cancel, cancelAt: p.cancelAt,
		}, nil
	}
	return plainFile{
		Reader: f, info: info, closer: f, reads: &p.reads, cancel: p.cancel, cancelAt: p.cancelAt,
	}, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type faultFS struct {
	fs.FS
	err      error
	openFail atomic.Bool
	statFail atomic.Bool
}

func (f *faultFS) Open(name string) (fs.File, error) {
	if f.openFail.Load() {
		return nil, &fs.PathError{Op: "open", Path: name, Err: f.err}
	}
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if f.statFail.Load() {
		return statErrorFile{File: file, err: f.err}, nil
	}
	return file, nil
}

type statErrorFile struct {
	fs.File
	err error
}

func (f statErrorFile) Stat() (fs.FileInfo, error) { return nil, f.err }

func TestAFileSystemWithoutSeekStillAnswers(t *testing.T) {
	fsys := &plainFS{FS: assetFS()}
	a := newAssets(t, static.Config{FS: fsys})
	fsys.reads.Store(0)

	rec := get(a, "/css/app.css")
	if rec.Code != http.StatusOK || rec.Body.String() != appCSS {
		t.Fatalf("status = %d, body = %q, want the file", rec.Code, rec.Body.String())
	}

	fsys.reads.Store(0)
	rec = get(a, "/css/app.css", method(http.MethodHead))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := fsys.reads.Load(); got != 0 {
		t.Errorf("HEAD read the non-seekable file %d times", got)
	}

	fsys.reads.Store(0)
	rec = get(a, "/css/app.css", header("Range", "bytes=5-9"))
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "color" {
		t.Fatalf("range status = %d, body = %q", rec.Code, rec.Body.String())
	}

	rec = get(a, "/css/app.css", header("Range", "bytes=0-1,5-6"))
	if rec.Code != http.StatusOK || rec.Body.String() != appCSS {
		t.Fatalf("multipart range status = %d, body = %q; non-seekable files should ignore it", rec.Code, rec.Body.String())
	}
}

func TestNonSeekableFilesHandleRangeSyntax(t *testing.T) {
	fsys := &plainFS{FS: assetFS()}
	a := newAssets(t, static.Config{FS: fsys})
	tests := []struct {
		name        string
		rangeHeader string
		status      int
		body        string
	}{
		{name: "suffix longer than the file", rangeHeader: "bytes=-999", status: http.StatusPartialContent, body: appCSS},
		{name: "missing dash", rangeHeader: "bytes=4", status: http.StatusRequestedRangeNotSatisfiable},
		{name: "double dash", rangeHeader: "bytes=--1", status: http.StatusRequestedRangeNotSatisfiable},
		{name: "non-numeric suffix", rangeHeader: "bytes=-many", status: http.StatusRequestedRangeNotSatisfiable},
		{name: "non-numeric start", rangeHeader: "bytes=x-2", status: http.StatusRequestedRangeNotSatisfiable},
		{name: "reversed", rangeHeader: "bytes=8-4", status: http.StatusRequestedRangeNotSatisfiable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(a, "/css/app.css", header("Range", tt.rangeHeader))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tt.status, rec.Body.String())
			}
			if tt.body != "" && rec.Body.String() != tt.body {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.body)
			}
		})
	}
}

func TestAConditionalRequestDoesNotReadANonSeekableFile(t *testing.T) {
	fsys := &plainFS{FS: assetFS()}
	a := newAssets(t, static.Config{FS: fsys})
	fsys.reads.Store(0)
	tag := get(a, "/css/app.css", method(http.MethodHead)).Header().Get("Etag")
	if tag == "" {
		t.Fatal("HEAD returned no ETag")
	}
	if got := fsys.reads.Load(); got != 0 {
		t.Fatalf("HEAD read the non-seekable file %d times", got)
	}

	rec := get(a, "/css/app.css", header("If-None-Match", tag))
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if got := fsys.reads.Load(); got != 0 {
		t.Errorf("conditional request read the non-seekable file %d times", got)
	}
}

func TestExtensionlessHEADOmitsUnknowableTypeWithoutReading(t *testing.T) {
	fsys := &plainFS{FS: fstest.MapFS{
		"index.html": {Data: []byte(rootIndex)},
		"LICENSE":    {Data: []byte("plain text")},
	}}
	a := newAssets(t, static.Config{FS: fsys})
	fsys.reads.Store(0)

	getRec := get(a, "/LICENSE")
	if got := getRec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("GET Content-Type = %q, want text/plain; charset=utf-8", got)
	}
	fsys.reads.Store(0)

	headRec := get(a, "/LICENSE", method(http.MethodHead))
	if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
		t.Fatalf("HEAD status = %d, body = %q", headRec.Code, headRec.Body.String())
	}
	if got := headRec.Header().Get("Content-Type"); got != "" {
		t.Errorf("HEAD Content-Type = %q, want omitted", got)
	}
	for _, name := range []string{"Content-Length", "Accept-Ranges", "Etag", "Cache-Control", "Last-Modified"} {
		if got, want := headRec.Header().Get(name), getRec.Header().Get(name); got != want {
			t.Errorf("HEAD %s = %q, want GET value %q", name, got, want)
		}
	}
	if got := fsys.reads.Load(); got != 0 {
		t.Errorf("HEAD read the non-seekable file %d times", got)
	}
}

func TestALateRangeOnANonSeekableFileIsIgnored(t *testing.T) {
	data := strings.Repeat("x", 1<<20+32)
	fsys := &plainFS{FS: fstest.MapFS{
		"index.html": {Data: []byte(rootIndex)},
		"large.bin":  {Data: []byte(data)},
	}}
	a := newAssets(t, static.Config{FS: fsys})
	fsys.reads.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name        string
		rangeHeader string
		status      int
	}{
		{name: "late start", rangeHeader: "bytes=1048580-1048589", status: http.StatusOK},
		{name: "late suffix", rangeHeader: "bytes=-10", status: http.StatusOK},
		{name: "unsatisfiable", rangeHeader: "bytes=1048608-1048617", status: http.StatusRequestedRangeNotSatisfiable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys.reads.Store(0)
			req := httptest.NewRequest(http.MethodGet, "/large.bin", nil).WithContext(ctx)
			req.Header.Set("Range", tt.rangeHeader)
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := fsys.reads.Load(); got != 0 {
				t.Errorf("Read called %d times after cancellation, want zero", got)
			}
		})
	}
}

func TestRangeDiscardStopsAtRequestCancellation(t *testing.T) {
	data := strings.Repeat("x", 1<<17)
	fsys := &plainFS{FS: fstest.MapFS{
		"index.html": {Data: []byte(rootIndex)},
		"large.bin":  {Data: []byte(data)},
	}}
	a := newAssets(t, static.Config{FS: fsys})
	ctx, cancel := context.WithCancel(context.Background())
	fsys.cancel = cancel
	fsys.cancelAt = 2
	fsys.reads.Store(0)
	req := httptest.NewRequest(http.MethodGet, "/large.bin", nil).WithContext(ctx)
	req.Header.Set("Range", "bytes=65536-65545")
	rec := httptest.NewRecorder()

	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := fsys.reads.Load(); got != 2 {
		t.Errorf("Read called %d times, want two before cancellation", got)
	}
}

func TestAnUnreadableFileAnswers500(t *testing.T) {
	fsys := &plainFS{FS: assetFS()}
	a := newAssets(t, static.Config{FS: fsys})
	fsys.broken.Store(true)

	rec := get(a, "/css/app.css")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("cache control = %q; a proxy would keep the failure", got)
	}
}

func TestOperationalFileSystemFailuresNeverBecomeSPAFallbacks(t *testing.T) {
	boom := errors.New("storage failed")
	tests := []struct {
		name string
		err  error
		stat bool
		spa  bool
	}{
		{name: "permission on open", err: fs.ErrPermission},
		{name: "invalid from backend", err: fs.ErrInvalid},
		{name: "arbitrary open failure", err: boom},
		{name: "permission on open with SPA", err: fs.ErrPermission, spa: true},
		{name: "arbitrary open failure with SPA", err: boom, spa: true},
		{name: "stat failure", err: boom, stat: true},
		{name: "stat failure during SPA fallback", err: boom, stat: true, spa: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := &faultFS{FS: assetFS(), err: tt.err}
			a := newAssets(t, static.Config{FS: fsys, SPA: tt.spa})
			if tt.stat {
				fsys.statFail.Store(true)
			} else {
				fsys.openFail.Store(true)
			}
			target := "/css/app.css"
			if tt.spa {
				target = "/dashboard"
			}
			rec := get(a, target, header("Accept", "text/html"))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body = %q", rec.Code, rec.Body.String())
			}
			if rec.Body.String() == rootIndex {
				t.Fatal("operational failure was replaced by the SPA index")
			}
		})
	}
}

func TestNewReportsAnUnreadableAssetSet(t *testing.T) {
	fsys := &plainFS{FS: assetFS()}
	fsys.broken.Store(true)

	if _, err := static.New(static.Config{FS: fsys}); err == nil {
		t.Fatal("New accepted an asset set that it cannot read")
	}
}

func TestTheBareBuildTagAnswersTheIndex(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Build: "v1"})

	rec := get(a, "/v1")
	if got := rec.Body.String(); got != rootIndex {
		t.Fatalf("body = %q, want the index", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("cache control = %q, want no-cache", got)
	}
}

func TestSPAWithoutAnIndexAnswersTheRootWith404(t *testing.T) {
	a := newAssets(t, static.Config{
		FS:  fstest.MapFS{"css/app.css": {Data: []byte(appCSS)}},
		SPA: true,
	})

	if rec := get(a, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; the fallback cannot answer with itself", rec.Code)
	}
}

func TestHasRejectsAnEmptyName(t *testing.T) {
	if newAssets(t, static.Config{FS: assetFS()}).Has("") {
		t.Error("Has(\"\") = true")
	}
	if newAssets(t, static.Config{Dir: assetDir(t)}).Has("") {
		t.Error("Has(\"\") = true in Dir mode")
	}
}

func TestSPAAnswersADeepLinkThatLooksLikeAFile(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})

	rec := get(a, "/reports/2026.01", header("Accept", "text/html;q=0.9,*/*;q=0.8"))
	if rec.Body.String() != rootIndex {
		t.Fatalf("body = %q, want the index", rec.Body.String())
	}
}

func TestSPAAnswersADirectoryWithoutAnIndex(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})

	if got := get(a, "/css").Body.String(); got != rootIndex {
		t.Fatalf("body = %q, want the index", got)
	}
}

func TestVersionedIndexStillRevalidates(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static", Build: "v1"})

	for _, target := range []string{"/v1/index.html", "/v1/sub"} {
		if got := get(a, target).Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: cache control = %q, want no-cache", target, got)
		}
	}
	if got := get(a, "/v1/css/app.css").Header().Get("Cache-Control"); got != immutableCache {
		t.Errorf("cache control = %q, want an immutable one", got)
	}
}

func TestNewRefusesAnIndexOutsideTheSet(t *testing.T) {
	for _, index := range []string{"/index.html", "../index.html", "./", "."} {
		if _, err := static.New(static.Config{FS: assetFS(), Index: index}); err == nil {
			t.Errorf("New accepted the index %q", index)
		}
	}
}

func TestNewRefusesADirectoryAsTheIndex(t *testing.T) {
	if _, err := static.New(static.Config{FS: assetFS(), Index: "css"}); err == nil {
		t.Fatal("New accepted a directory as the index")
	}
}

func TestMaxAgeUnderASecondKeepsNoCache(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), MaxAge: 500 * time.Millisecond})

	if got := get(a, "/css/app.css").Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("cache control = %q, want no-cache", got)
	}
}

func TestFallbackReplacesTheNavigationTest(t *testing.T) {
	a := newAssets(t, static.Config{
		FS:       assetFS(),
		SPA:      true,
		Fallback: func(r *http.Request) bool { return !strings.HasPrefix(r.URL.Path, "/api/") },
	})

	if got := get(a, "/reports/2026.01").Body.String(); got != rootIndex {
		t.Errorf("body = %q, want the index", got)
	}
	if rec := get(a, "/api/absent", header("Accept", "text/html")); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a path the fallback refuses", rec.Code)
	}
	if got := get(a, "/reports/2026.01").Header().Get("Vary"); got != "*" {
		t.Errorf("Vary = %q, want * for a fallback that may inspect any header", got)
	}
}

func TestSPAFallbackNegotiatesHTMLAndVariesOnAccept(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})
	tests := []struct {
		name   string
		target string
		accept string
		code   int
	}{
		{name: "HTML", target: "/report.csv", accept: "text/html", code: http.StatusOK},
		{name: "mixed case", target: "/report.csv", accept: "Text/HTML;Q=0.8", code: http.StatusOK},
		{name: "refused HTML", target: "/report.csv", accept: "text/html;q=0, */*;q=1", code: http.StatusNotFound},
		{name: "JSON navigation", target: "/dashboard", accept: "application/json", code: http.StatusNotFound},
		{name: "wildcard navigation", target: "/dashboard", accept: "*/*", code: http.StatusOK},
		{name: "wildcard asset", target: "/app.js", accept: "*/*", code: http.StatusNotFound},
		{name: "malformed then HTML", target: "/report.csv", accept: "garbage, text/html", code: http.StatusOK},
		{name: "type wildcard navigation", target: "/dashboard", accept: "text/*", code: http.StatusOK},
		{name: "invalid quality", target: "/dashboard", accept: "text/html;q=2", code: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(a, tt.target, header("Accept", tt.accept))
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tt.code, rec.Body.String())
			}
			if got := rec.Header().Get("Vary"); got != "Accept" {
				t.Errorf("Vary = %q, want Accept", got)
			}
		})
	}
}

func TestSPAFallbackCombinesRepeatedAcceptFields(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})
	tests := []struct {
		name    string
		target  string
		accepts []string
		code    int
	}{
		{name: "JSON then HTML", target: "/report.csv", accepts: []string{"application/json", "text/html;q=0.4"}, code: http.StatusOK},
		{name: "HTML then JSON", target: "/report.csv", accepts: []string{"text/html;q=0.4", "application/json"}, code: http.StatusOK},
		{name: "wildcard then refused HTML", target: "/dashboard", accepts: []string{"*/*;q=1", "text/html;q=0"}, code: http.StatusNotFound},
		{name: "refused HTML then wildcard", target: "/dashboard", accepts: []string{"text/html;q=0", "*/*;q=1"}, code: http.StatusNotFound},
		{name: "JSON then wildcard navigation", target: "/dashboard", accepts: []string{"application/json", "*/*;q=0.2"}, code: http.StatusOK},
		{name: "specific HTML after preferred wildcard", target: "/report.csv", accepts: []string{"*/*;q=1", "text/html;q=0.2"}, code: http.StatusOK},
		{name: "wildcard does not answer an asset", target: "/report.csv", accepts: []string{"application/json", "*/*;q=1"}, code: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(a, tt.target, acceptHeaders(tt.accepts...))
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tt.code, rec.Body.String())
			}
			if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept" {
				t.Errorf("Vary = %q, want [Accept]", got)
			}
		})
	}
}

func TestURLEscapesTheName(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static", Build: "v1"})

	if got := a.URL("my file.css"); got != "/static/v1/my%20file.css" {
		t.Errorf("URL = %q, want the space escaped", got)
	}
	if got := a.URL("a#b.css"); got != "/static/v1/a%23b.css" {
		t.Errorf("URL = %q, want the fragment marker escaped", got)
	}
	if got := a.URL("css/app.css"); got != "/static/v1/css/app.css" {
		t.Errorf("URL = %q, want the plain name untouched", got)
	}
}

func resolve(tb testing.TB, target, location string) string {
	tb.Helper()
	base, err := url.Parse(target)
	if err != nil {
		tb.Fatalf("parse the target: %v", err)
	}
	ref, err := url.Parse(location)
	if err != nil {
		tb.Fatalf("parse the Location header: %v", err)
	}
	return base.ResolveReference(ref).String()
}

func TestRedirectDirAnswersADirectoryWithoutASlash(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), RedirectDir: true})

	rec := get(a, "/sub")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "./sub/" {
		t.Errorf("location = %q, want a relative one", got)
	}
	if got := resolve(t, "/sub", rec.Header().Get("Location")); got != "/sub/" {
		t.Errorf("the browser reads the location as %q, want /sub/", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want none", rec.Body.String())
	}
}

func TestRedirectDirAnswersOnlyADirectoryThatHoldsAnIndex(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		opts     []reqOption
		wantCode int
		wantBody string
	}{
		{"a directory", "/sub", nil, http.StatusMovedPermanently, ""},
		{"a HEAD of a directory", "/sub", []reqOption{method(http.MethodHead)}, http.StatusMovedPermanently, ""},
		{"the slashed form", "/sub/", nil, http.StatusOK, subIndex},
		{"a directory without an index", "/css", nil, http.StatusNotFound, ""},
		{"a file", "/css/app.css", nil, http.StatusOK, appCSS},
		{"the root", "/", nil, http.StatusOK, rootIndex},
		{"a name that nothing holds", "/absent", nil, http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAssets(t, static.Config{FS: assetFS(), RedirectDir: true})

			rec := get(a, tt.target, tt.opts...)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestRedirectDirIsOffByDefault(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS()})

	rec := get(a, "/sub")
	if rec.Code != http.StatusOK || rec.Body.String() != subIndex {
		t.Fatalf("status = %d, body = %q, want the index of the directory", rec.Code, rec.Body.String())
	}
}

func TestRedirectDirKeepsTheQuery(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), RedirectDir: true})

	rec := get(a, "/sub?page=2&sort=name")
	if got := rec.Header().Get("Location"); got != "./sub/?page=2&sort=name" {
		t.Errorf("location = %q, want the query with it", got)
	}
}

func TestRedirectDirKeepsThePrefix(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Prefix: "/static", RedirectDir: true})

	r := newRouter()
	static.Mount(r, a)

	for _, h := range []http.Handler{r, http.StripPrefix("/static", a)} {
		rec := get(h, "/static/sub")
		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("status = %d, want 301", rec.Code)
		}
		if got := resolve(t, "/static/sub", rec.Header().Get("Location")); got != "/static/sub/" {
			t.Errorf("the browser reads the location as %q, want /static/sub/", got)
		}
	}
}

func TestRedirectDirNeverAnswersTheSPAFallback(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true, RedirectDir: true})

	for _, target := range []string{"/dashboard", "/orders/7/edit", "/css"} {
		rec := get(a, target)
		if rec.Code != http.StatusOK || rec.Body.String() != rootIndex {
			t.Errorf("%s: status = %d, body = %q, want the index", target, rec.Code, rec.Body.String())
		}
	}
	if rec := get(a, "/sub"); rec.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301 for a directory that holds an index", rec.Code)
	}
}

func TestDirModeRedirectsADirectory(t *testing.T) {
	a := newAssets(t, static.Config{Dir: assetDir(t), RedirectDir: true})

	rec := get(a, "/sub")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "./sub/" {
		t.Errorf("location = %q", got)
	}
	if rec := get(a, "/css"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; the directory holds no index", rec.Code)
	}
}
