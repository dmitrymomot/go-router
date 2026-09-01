package router

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dmitrymomot/go-router/internal/nonseek"
)

const (
	dispositionAttachment = "attachment"
	dispositionInline     = "inline"
)

func (b *Base) File(name string) error {
	return b.serveFile(name, nil, "", "")
}

func (b *Base) FileFS(name string, fsys fs.FS) error {
	if fsys == nil {
		return ErrInternalServerError.WithError(errors.New("router: FileFS needs a file system"))
	}
	return b.serveFile(name, fsys, "", "")
}

func (b *Base) AttachmentFile(name, filename string) error {
	return b.serveFile(name, nil, dispositionAttachment, filename)
}

func (b *Base) InlineFile(name, filename string) error {
	return b.serveFile(name, nil, dispositionInline, filename)
}

func (b *Base) Inline(status int, contentType, filename string, data []byte) error {
	b.res.Header().Set(HeaderContentDisposition, contentDisposition(dispositionInline, filename))
	return b.Blob(status, contentType, data)
}

func (b *Base) serveFile(name string, fsys fs.FS, kind, filename string) error {
	if !safeFileName(name) {
		return ErrNotFound
	}
	clean := cleanFileName(name)
	if clean == "." || !fs.ValidPath(clean) {
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
	if info.IsDir() {
		return ErrNotFound
	}

	if kind != "" {
		if filename == "" {
			filename = path.Base(clean)
		}
		b.res.Header().Set(HeaderContentDisposition, contentDisposition(kind, filename))
	}
	return b.sendFile(clean, f, info)
}

// os.Root refuses a path that leaves the directory, symbolic links included.
func openFileIn(name string, fsys fs.FS) (fs.File, error) {
	if fsys != nil {
		return fsys.Open(name)
	}
	return os.OpenInRoot(".", filepath.FromSlash(name))
}

func fileNotFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return ErrNotFound.WithError(fmt.Errorf("router: serve the file: %w", err))
}

func (b *Base) sendFile(name string, f fs.File, info fs.FileInfo) error {
	req := b.req
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		req = nonseek.Request(req, info.Size())
		var err error
		rs, err = nonseek.ReadSeeker("", b.res.Header(), req, name, f, info.Size())
		if err != nil {
			return ErrInternalServerError.WithError(fmt.Errorf("router: read the file: %w", err))
		}
	}

	http.ServeContent(b.res, req, path.Base(name), info.ModTime(), rs)
	return nil
}

func safeFileName(name string) bool {
	if name == "" || name == "." || path.IsAbs(name) || filepath.IsAbs(name) ||
		filepath.VolumeName(name) != "" || strings.ContainsRune(name, '\\') {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func cleanFileName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

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

func lastSegment(filename string) string {
	if i := strings.LastIndexAny(filename, `/\`); i >= 0 {
		filename = filename[i+1:]
	}
	if filename == "." || filename == ".." {
		return ""
	}
	return filename
}

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

const hexDigits = "0123456789ABCDEF"

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

func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
	}
}
