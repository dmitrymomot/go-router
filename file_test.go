package router

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func doReq(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func serveDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	t.Chdir(dir)
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fileRouter(name string) *Router[*tctx] {
	r := newTestRouter()
	r.GET("/f", func(c *tctx) error { return c.File(name) })
	return r
}

func TestFileServesTheContent(t *testing.T) {
	serveDir(t, map[string]string{"docs/readme.txt": "hello file"})

	rec := do(fileRouter("docs/readme.txt"), http.MethodGet, "/f")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "hello file" {
		t.Errorf("body = %q, want %q", got, "hello file")
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMETextPlainCharsetUTF8 {
		t.Errorf("Content-Type = %q, want %q", got, MIMETextPlainCharsetUTF8)
	}
	if got := rec.Header().Get(HeaderContentLength); got != "10" {
		t.Errorf("Content-Length = %q, want %q", got, "10")
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
	}
	if rec.Header().Get(HeaderContentDisposition) != "" {
		t.Errorf("Content-Disposition = %q, want none", rec.Header().Get(HeaderContentDisposition))
	}
}

func TestFileAnswersARangeRequest(t *testing.T) {
	serveDir(t, map[string]string{"readme.txt": "hello file"})

	req := httptest.NewRequest(http.MethodGet, "/f", nil)
	req.Header.Set("Range", "bytes=6-9")
	rec := doReq(fileRouter("readme.txt"), req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "file" {
		t.Errorf("body = %q, want %q", got, "file")
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 6-9/10" {
		t.Errorf("Content-Range = %q, want %q", got, "bytes 6-9/10")
	}
}

func TestFileAnswersAConditionalRequest(t *testing.T) {
	dir := serveDir(t, map[string]string{"readme.txt": "hello file"})
	info, err := os.Stat(filepath.Join(dir, "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/f", nil)
	req.Header.Set("If-Modified-Since", info.ModTime().UTC().Format(http.TimeFormat))
	rec := doReq(fileRouter("readme.txt"), req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

func TestFileHEADWritesTheLengthOnly(t *testing.T) {
	serveDir(t, map[string]string{"readme.txt": "hello file"})

	rec := do(fileRouter("readme.txt"), http.MethodHead, "/f")
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get(HeaderContentLength); got != "10" {
		t.Errorf("Content-Length = %q, want %q", got, "10")
	}
}

func TestFileNameOutsideTheRootAnswersNotFound(t *testing.T) {
	captureLogs(t)

	root := t.TempDir()
	writeFiles(t, root, map[string]string{"secret.txt": "top secret"})
	writeFiles(t, filepath.Join(root, "public"), map[string]string{
		"ok.txt":       "public",
		"sub/deep.txt": "deeper",
	})
	t.Chdir(filepath.Join(root, "public"))

	tests := []struct {
		name string
		file string
	}{
		{"a parent directory", "../secret.txt"},
		{"a deep escape", "../../../../etc/passwd"},
		{"an absolute path", "/etc/passwd"},
		{"a dot segment in the middle", "sub/../../secret.txt"},
		{"an empty name", ""},
		{"the root itself", "."},
		{"a directory", "sub"},
		{"a file that nothing holds", "nope.txt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(fileRouter(tc.file), http.MethodGet, "/f")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "top secret") {
				t.Errorf("body = %q, want no file outside the root", rec.Body.String())
			}
		})
	}
}

func TestFileSymlinkOutOfTheRootAnswersNotFound(t *testing.T) {
	logs := captureLogs(t)

	root := t.TempDir()
	writeFiles(t, root, map[string]string{"secret.txt": "top secret"})
	public := filepath.Join(root, "public")
	writeFiles(t, public, map[string]string{"ok.txt": "public"})
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(public, "link.txt")); err != nil {
		t.Skipf("the file system takes no symbolic link: %v", err)
	}
	t.Chdir(public)

	rec := do(fileRouter("link.txt"), http.MethodGet, "/f")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); strings.Contains(got, "top secret") {
		t.Errorf("body = %q, want no file from outside the root", got)
	}
	if got := rec.Body.String(); strings.Contains(got, root) || strings.Contains(got, "link.txt") {
		t.Errorf("body = %q, want no path from the disk", got)
	}
	if len(logs.records) != 1 {
		t.Fatalf("logged %d records, want 1; the reason for the refusal belongs in the log", len(logs.records))
	}
}

func TestFileFS(t *testing.T) {
	fsys := fstest.MapFS{
		"docs/readme.txt": &fstest.MapFile{Data: []byte("from the tree")},
	}
	r := newTestRouter()
	r.GET("/f", func(c *tctx) error { return c.FileFS("docs/readme.txt", fsys) })
	r.GET("/miss", func(c *tctx) error { return c.FileFS("nope.txt", fsys) })
	r.GET("/escape", func(c *tctx) error { return c.FileFS("../secret.txt", fsys) })
	r.GET("/nil", func(c *tctx) error { return c.FileFS("docs/readme.txt", nil) })

	rec := do(r, http.MethodGet, "/f")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "from the tree" {
		t.Errorf("body = %q, want %q", got, "from the tree")
	}
	if got := rec.Header().Get(HeaderContentType); got != MIMETextPlainCharsetUTF8 {
		t.Errorf("Content-Type = %q, want %q", got, MIMETextPlainCharsetUTF8)
	}

	if got := do(r, http.MethodGet, "/miss").Code; got != http.StatusNotFound {
		t.Errorf("a missing file answers %d, want 404", got)
	}
	if got := do(r, http.MethodGet, "/escape").Code; got != http.StatusNotFound {
		t.Errorf("a name that tries to leave the tree answers %d, want 404", got)
	}

	captureLogs(t)
	if got := do(r, http.MethodGet, "/nil").Code; got != http.StatusInternalServerError {
		t.Errorf("a nil file system answers %d, want 500", got)
	}
}

type plainFS struct {
	files   map[string]string
	failing bool
}

func (f plainFS) Open(name string) (fs.File, error) {
	body, ok := f.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &plainFile{name: name, body: strings.NewReader(body), failing: f.failing}, nil
}

type plainFile struct {
	name    string
	body    *strings.Reader
	failing bool
}

func (f *plainFile) Read(p []byte) (int, error) {
	if f.failing {
		return 0, io.ErrUnexpectedEOF
	}
	return f.body.Read(p)
}

func (f *plainFile) Close() error               { return nil }
func (f *plainFile) Stat() (fs.FileInfo, error) { return plainInfo{f.name, f.body.Size()}, nil }

type plainInfo struct {
	name string
	size int64
}

func (i plainInfo) Name() string       { return i.name }
func (i plainInfo) Size() int64        { return i.size }
func (i plainInfo) Mode() fs.FileMode  { return 0o444 }
func (i plainInfo) ModTime() time.Time { return time.Time{} }
func (i plainInfo) IsDir() bool        { return false }
func (i plainInfo) Sys() any           { return nil }

func TestFileFSReadsAFileThatCannotSeek(t *testing.T) {
	fsys := plainFS{files: map[string]string{"page.html": "<h1>hi</h1>"}}
	r := newTestRouter()
	r.GET("/f", func(c *tctx) error { return c.FileFS("page.html", fsys) })

	req := httptest.NewRequest(http.MethodGet, "/f", nil)
	req.Header.Set("Range", "bytes=4-8")
	rec := doReq(r, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "hi</h" {
		t.Errorf("body = %q, want %q", got, "hi</h")
	}
}

func TestFileFSUnreadableFileWritesNoBody(t *testing.T) {
	captureLogs(t)

	fsys := plainFS{files: map[string]string{"page.html": "<h1>hi</h1>"}, failing: true}
	r := newTestRouter()
	r.GET("/f", func(c *tctx) error { return c.FileFS("page.html", fsys) })

	rec := do(r, http.MethodGet, "/f")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<h1>") {
		t.Errorf("body = %q, want no part of the file", rec.Body.String())
	}
}

func TestAttachmentFileStreamsTheFile(t *testing.T) {
	serveDir(t, map[string]string{"exports/report.csv": "a,b\n1,2\n"})

	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"the name that the caller gives", "orders.csv", `attachment; filename="orders.csv"`},
		{"an empty name uses the file", "", `attachment; filename="report.csv"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			r.GET("/f", func(c *tctx) error { return c.AttachmentFile("exports/report.csv", tc.filename) })

			rec := do(r, http.MethodGet, "/f")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != "a,b\n1,2\n" {
				t.Errorf("body = %q", got)
			}
			if got := rec.Header().Get(HeaderContentDisposition); got != tc.want {
				t.Errorf("Content-Disposition = %q, want %q", got, tc.want)
			}
			if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
			}
		})
	}
}

func TestInlineFile(t *testing.T) {
	serveDir(t, map[string]string{"invoice.pdf": "%PDF-1.7"})

	r := newTestRouter()
	r.GET("/f", func(c *tctx) error { return c.InlineFile("invoice.pdf", "") })

	rec := do(r, http.MethodGet, "/f")
	if got := rec.Header().Get(HeaderContentDisposition); got != `inline; filename="invoice.pdf"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := rec.Body.String(); got != "%PDF-1.7" {
		t.Errorf("body = %q", got)
	}
}

func TestAttachmentFileMissWritesNoDisposition(t *testing.T) {
	serveDir(t, map[string]string{"exports/report.csv": "a,b\n"})

	r := newTestRouter()
	r.GET("/f", func(c *tctx) error { return c.AttachmentFile("exports/nope.csv", "orders.csv") })

	rec := do(r, http.MethodGet, "/f")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get(HeaderContentDisposition); got != "" {
		t.Errorf("Content-Disposition = %q, want none", got)
	}
}

func TestInlineWritesTheBytes(t *testing.T) {
	r := newTestRouter()
	r.GET("/f", func(c *tctx) error {
		return c.Inline(http.StatusOK, "image/png", "avatar.png", []byte("PNG"))
	})

	rec := do(r, http.MethodGet, "/f")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "PNG" {
		t.Errorf("body = %q, want %q", got, "PNG")
	}
	if got := rec.Header().Get(HeaderContentType); got != "image/png" {
		t.Errorf("Content-Type = %q, want %q", got, "image/png")
	}
	if got := rec.Header().Get(HeaderContentDisposition); got != `inline; filename="avatar.png"` {
		t.Errorf("Content-Disposition = %q", got)
	}
}

func TestAttachmentWritesTheDisposition(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"a plain name", "report.pdf", `attachment; filename="report.pdf"`},
		{"a name with spaces", "q3 report.pdf", `attachment; filename="q3 report.pdf"`},
		{
			"a name outside ASCII",
			"naïve.pdf",
			`attachment; filename="na__ve.pdf"; filename*=UTF-8''na%C3%AFve.pdf`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRouter()
			r.GET("/f", func(c *tctx) error {
				return c.Attachment(http.StatusOK, MIMEOctetStream, tc.filename, []byte("data"))
			})

			rec := do(r, http.MethodGet, "/f")
			if got := rec.Header().Get(HeaderContentDisposition); got != tc.want {
				t.Errorf("Content-Disposition = %q, want %q", got, tc.want)
			}
			if got := rec.Body.String(); got != "data" {
				t.Errorf("body = %q, want %q", got, "data")
			}
		})
	}
}

func TestContentDisposition(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		filename string
		want     string
	}{
		{
			"a plain ASCII name",
			dispositionAttachment, "report.pdf",
			`attachment; filename="report.pdf"`,
		},
		{
			"an inline name",
			dispositionInline, "avatar.png",
			`inline; filename="avatar.png"`,
		},
		{
			"a name outside ASCII",
			dispositionAttachment, "文件.txt",
			`attachment; filename="______.txt"; filename*=UTF-8''%E6%96%87%E4%BB%B6.txt`,
		},
		{
			"a quote that would end the string",
			dispositionAttachment, `a"b.pdf`,
			`attachment; filename="a_b.pdf"; filename*=UTF-8''a%22b.pdf`,
		},
		{
			"a line break that would split the header",
			dispositionAttachment, "a\r\nX-Evil: 1.pdf",
			`attachment; filename="a__X-Evil: 1.pdf"; filename*=UTF-8''a%0D%0AX-Evil%3A%201.pdf`,
		},
		{
			"a directory in front of the name",
			dispositionAttachment, "reports/2024/q3.pdf",
			`attachment; filename="q3.pdf"`,
		},
		{
			"a Windows path in front of the name",
			dispositionAttachment, `C:\reports\q3.pdf`,
			`attachment; filename="q3.pdf"`,
		},
		{"an empty name", dispositionAttachment, "", "attachment"},
		{"a name that points at a directory", dispositionAttachment, "..", "attachment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := contentDisposition(tc.kind, tc.filename)
			if got != tc.want {
				t.Errorf("contentDisposition(%q, %q) = %q, want %q", tc.kind, tc.filename, got, tc.want)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Errorf("the header value holds a line break: %q", got)
			}
		})
	}
}

func TestCleanFileName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a plain name", "docs/readme.txt", "docs/readme.txt"},
		{"a leading slash", "/docs/readme.txt", "docs/readme.txt"},
		{"a parent segment", "../../etc/passwd", "etc/passwd"},
		{"a parent segment in the middle", "docs/../../etc/passwd", "etc/passwd"},
		{"a dot segment", "./docs/./readme.txt", "docs/readme.txt"},
		{"a double slash", "docs//readme.txt", "docs/readme.txt"},
		{"an empty name", "", ""},
		{"the root", "/", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanFileName(tc.in); got != tc.want {
				t.Errorf("cleanFileName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
