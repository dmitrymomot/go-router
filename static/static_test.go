package static_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dmitrymomot/go-router/static"
)

// immutableCache is the Cache-Control of a versioned answer.
const immutableCache = "public, max-age=31536000, immutable"

const (
	rootIndex = "<html>root</html>"
	subIndex  = "<html>sub</html>"
	appCSS    = "body{color:red}"
	appJS     = "console.log(1)"
)

// assetFS is the file set that the embedded mode tests answer from.
func assetFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte(rootIndex)},
		"css/app.css":    {Data: []byte(appCSS)},
		"js/app.js":      {Data: []byte(appJS)},
		"sub/index.html": {Data: []byte(subIndex)},
	}
}

// assetDir writes the same file set into a temporary directory.
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

// reqOption changes a request that get builds.
type reqOption func(*http.Request)

func header(key, value string) reqOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
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

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Embedded mode
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Single page application
// ---------------------------------------------------------------------------

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

	// curl sends no Accept header, and a deep link still has to work.
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

// ---------------------------------------------------------------------------
// Live mode
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// URLs
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Unusual file systems
// ---------------------------------------------------------------------------

// plainFile is a file that implements Read and nothing else, which is what a
// hand written fs.FS often returns.
type plainFile struct {
	io.Reader
	info fs.FileInfo
}

func (f plainFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f plainFile) Close() error               { return nil }

// plainFS strips the Seek method off every file of the set, and breaks the
// read once broken is set.
type plainFS struct {
	fs.FS
	broken atomic.Bool
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
	data, err := io.ReadAll(f)
	//nolint:errcheck // The file is read only.
	f.Close()
	if err != nil {
		return nil, err
	}
	if p.broken.Load() {
		return plainFile{Reader: errReader{}, info: info}, nil
	}
	return plainFile{Reader: bytes.NewReader(data), info: info}, nil
}

// errReader fails every read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestAFileSystemWithoutSeekStillAnswers(t *testing.T) {
	a := newAssets(t, static.Config{FS: &plainFS{FS: assetFS()}})

	rec := get(a, "/css/app.css")
	if rec.Code != http.StatusOK || rec.Body.String() != appCSS {
		t.Fatalf("status = %d, body = %q, want the file", rec.Code, rec.Body.String())
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

func TestNewReportsAnUnreadableAssetSet(t *testing.T) {
	fsys := &plainFS{FS: assetFS()}
	fsys.broken.Store(true)

	if _, err := static.New(static.Config{FS: fsys}); err == nil {
		t.Fatal("New accepted an asset set that it cannot read")
	}
}

// ---------------------------------------------------------------------------
// Remaining paths
// ---------------------------------------------------------------------------

func TestTheBareBuildTagAnswersTheIndex(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), Build: "v1"})

	rec := get(a, "/v1")
	if got := rec.Body.String(); got != rootIndex {
		t.Fatalf("body = %q, want the index", got)
	}
	// The index names the versioned assets, so the build tag in the path does
	// not make it immutable.
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

	// The path carries an extension, so only the Accept header tells the
	// fallback that this is a navigation.
	rec := get(a, "/reports/2026.01", header("Accept", "text/html;q=0.9,*/*;q=0.8"))
	if rec.Body.String() != rootIndex {
		t.Fatalf("body = %q, want the index", rec.Body.String())
	}
}

func TestSPAAnswersADirectoryWithoutAnIndex(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), SPA: true})

	// "/css" is a directory of the set and holds no index. It still reads as a
	// route of the application.
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
	// Every other file under the tag keeps the immutable answer.
	if got := get(a, "/v1/css/app.css").Header().Get("Cache-Control"); got != immutableCache {
		t.Errorf("cache control = %q, want an immutable one", got)
	}
}

func TestNewRefusesAnIndexOutsideTheSet(t *testing.T) {
	for _, index := range []string{"/index.html", "../index.html", "./"} {
		if _, err := static.New(static.Config{FS: assetFS(), Index: index}); err == nil {
			t.Errorf("New accepted the index %q", index)
		}
	}
}

func TestMaxAgeUnderASecondKeepsNoCache(t *testing.T) {
	a := newAssets(t, static.Config{FS: assetFS(), MaxAge: 500 * time.Millisecond})

	// "public, max-age=0" lets a shared cache keep the answer, so the shorter
	// request has to leave the stricter default in place.
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

	// The default rule would answer both from the extension and the Accept
	// header; the replacement reads the path alone.
	if got := get(a, "/reports/2026.01").Body.String(); got != rootIndex {
		t.Errorf("body = %q, want the index", got)
	}
	if rec := get(a, "/api/absent", header("Accept", "text/html")); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a path the fallback refuses", rec.Code)
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
