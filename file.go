package router

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The disposition types that the header helpers write.
const (
	dispositionAttachment = "attachment"
	dispositionInline     = "inline"
)

// File writes the named file to the response.
//
// The name is a slash separated path inside the working directory of the
// process. Every ".." comes out of it before the open, and [os.Root] resolves
// what is left, so a name that came out of a request reaches no file outside
// that directory, not even through a symbolic link.
//
// The answer goes out through [http.ServeContent], which sets the media type
// from the extension, answers a range request with 206 and a conditional
// request with 304. A HEAD request receives the headers alone.
//
// A file that the process cannot open answers [ErrNotFound], and so does a
// directory. The reason reaches the log and never the client, because the
// layout of the disk is no answer to a request:
//
//	r.GET("/invoices/{id}", func(c *app.Context) error {
//		if !c.User.Owns(c.Param("id")) {
//			return router.ErrForbidden
//		}
//		return c.File("invoices/" + c.Param("id") + ".pdf")
//	})
func (b *Base) File(name string) error {
	return b.serveFile(name, nil, "", "")
}

// FileFS writes the named file from fsys, which is normally an embed.FS.
// Everything that [Base.File] answers, it answers the same way.
//
// A file that arrives without a Seek method is read whole before the answer
// goes out. An embed.FS and an [os.DirFS] both open one that seeks, so a range
// request costs nothing extra there.
func (b *Base) FileFS(name string, fsys fs.FS) error {
	if fsys == nil {
		return ErrInternalServerError.WithError(errors.New("router: FileFS needs a file system"))
	}
	return b.serveFile(name, fsys, "", "")
}

// AttachmentFile writes the named file with a Content-Disposition header, so
// that the browser saves it under filename. An empty filename uses the base of
// name.
//
// It streams the file. [Base.Attachment] takes the whole body in memory, so a
// large export costs a copy per request there and none here. A download that
// lost the connection resumes, because [http.ServeContent] answers a range
// request.
//
// The header goes out only once the file is open, so a miss answers a plain
// 404 that no browser saves to disk.
func (b *Base) AttachmentFile(name, filename string) error {
	return b.serveFile(name, nil, dispositionAttachment, filename)
}

// InlineFile writes the named file with a Content-Disposition header that
// names it without asking the browser to save it, which is how a PDF opens in
// the tab. An empty filename uses the base of name.
//
// It streams the file the way [Base.AttachmentFile] does, and answers a range
// request, which is what a viewer needs to page through a large document
// without reading all of it.
func (b *Base) InlineFile(name, filename string) error {
	return b.serveFile(name, nil, dispositionInline, filename)
}

// Inline writes the body with a Content-Disposition header that names it
// without asking the browser to save it. Use it for a document that the
// process holds in memory, such as a rendered image or a generated preview.
//
// It writes the whole body, as [Base.Attachment] does. [Base.InlineFile]
// streams a file from disk instead, and answers a range request with it.
func (b *Base) Inline(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionInline, filename))
	return b.Blob(status, contentType, data)
}

// serveFile answers with one file. fsys holds it, or the working directory of
// the process does while fsys is nil.
//
// kind is the disposition type of the answer, empty for one that carries no
// Content-Disposition header, and filename is the name that the header
// carries, empty for the base of the file name itself.
func (b *Base) serveFile(name string, fsys fs.FS, kind, filename string) error {
	clean := cleanFileName(name)
	if !fs.ValidPath(clean) {
		return ErrNotFound
	}

	f, err := openFileIn(clean, fsys)
	if err != nil {
		return fileNotFound(err)
	}
	//nolint:errcheck // The file is read only.
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fileNotFound(err)
	}
	// A directory has no body to send. This package serves the file that a
	// handler names, and the static package serves a tree with an index.
	if info.IsDir() {
		return ErrNotFound
	}

	// The header waits until the file is open, so that a miss answers a 404
	// that the browser renders instead of one that it saves to disk.
	if kind != "" {
		if filename == "" {
			filename = path.Base(clean)
		}
		b.res.Header().Set(HeaderContentDisposition, contentDisposition(kind, filename))
	}
	return b.sendFile(clean, f, info)
}

// openFileIn opens a clean path inside fsys, or inside the working directory
// of the process while fsys is nil.
func openFileIn(name string, fsys fs.FS) (fs.File, error) {
	if fsys != nil {
		return fsys.Open(name)
	}
	// os.Root refuses a path that leaves the directory, including one that a
	// symbolic link points out of.
	return os.OpenInRoot(".", filepath.FromSlash(name))
}

// fileNotFound turns a failure to open into the answer that the client reads.
//
// A file that is simply absent answers 404 and logs nothing, because a request
// for a name that nothing holds is the ordinary miss. Any other failure, a
// permission or a path that left the root, answers the same 404 and carries
// its reason to the log, where an operator reads what the client may not.
func fileNotFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return ErrNotFound.WithError(fmt.Errorf("router: serve the file: %w", err))
}

// sendFile writes the headers and the body of one open file.
func (b *Base) sendFile(name string, f fs.File, info fs.FileInfo) error {
	// ServeContent needs a seeker for the size and for a range request. A file
	// that os.Root opens is one; a file system that the caller passes may hand
	// over a file that is not, and that one is read whole instead.
	//
	// The read comes before the headers, so that a failure leaves an untouched
	// response for the error handler to render.
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			return ErrInternalServerError.WithError(fmt.Errorf("router: read the file: %w", err))
		}
		rs = bytes.NewReader(data)
	}

	// A file system that reports no modification time, as an embed.FS does,
	// leaves ServeContent with no Last-Modified header to write.
	http.ServeContent(b.res, b.req, path.Base(name), info.ModTime(), rs)
	return nil
}

// cleanFileName turns a name into a path inside the root. The leading slash
// makes Clean drop every "..", so the result never leaves it.
func cleanFileName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

// contentDisposition returns the value of a Content-Disposition header. kind is
// the disposition type, "attachment" or "inline".
//
// The quoted name carries ASCII alone, because an HTTP quoted-string carries
// nothing else, and every byte that a parser could read as the end of that
// string becomes an underscore. A name that is not already such a string gets
// an RFC 5987 filename* next to it, which carries the real bytes and which
// every current browser prefers.
//
// A name that names no file, an empty one among them, produces the disposition
// type on its own. The header is then valid and the browser falls back to the
// last segment of the URL.
func contentDisposition(kind, filename string) string {
	name := lastSegment(filename)
	if name == "" {
		return kind
	}
	ascii := asciiFileName(name)
	if ascii == name {
		return kind + `; filename="` + ascii + `"`
	}
	return kind + `; filename="` + ascii + `"; filename*=UTF-8''` + encodeExtValue(name)
}

// lastSegment returns the file name at the end of a path, so that a directory
// that the caller left in front of it never reaches the header. A name that
// points at a directory rather than at a file comes back empty.
func lastSegment(filename string) string {
	if i := strings.LastIndexAny(filename, `/\`); i >= 0 {
		filename = filename[i+1:]
	}
	if filename == "." || filename == ".." {
		return ""
	}
	return filename
}

// asciiFileName returns the fallback name of a Content-Disposition header. It
// keeps the printable ASCII that a quoted-string carries and replaces every
// other byte with an underscore, the quote and the backslash that would end
// the string early among them.
func asciiFileName(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for i := range len(name) {
		switch c := name[i]; {
		case c < 0x20, c >= 0x7f, c == '"', c == '\\':
			sb.WriteByte('_')
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// hexDigits are the digits that encodeExtValue writes. RFC 8187 asks for the
// upper case form.
const hexDigits = "0123456789ABCDEF"

// encodeExtValue percent encodes a file name for the filename* parameter, in
// the form that RFC 8187 defines. A byte that does not stand for itself there
// comes out as a percent escape, so the header carries no delimiter of its own
// and no line break.
func encodeExtValue(name string) string {
	var sb strings.Builder
	sb.Grow(len(name))
	for i := range len(name) {
		c := name[i]
		if isAttrChar(c) {
			sb.WriteByte(c)
			continue
		}
		sb.WriteByte('%')
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0x0f])
	}
	return sb.String()
}

// isAttrChar reports whether a byte stands for itself in an RFC 8187 value.
func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
	}
}
