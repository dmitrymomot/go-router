package router

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type createUser struct {
	Name    string    `json:"name"`
	Age     int       `json:"age"`
	Admin   bool      `json:"admin"`
	Tags    []string  `json:"tags"`
	Since   time.Time `json:"since"`
	Ignored string    `json:"-"`
}

// formUser adds a duration, which encoding/json/v2 refuses to encode without
// an explicit format tag but which the form decoder reads directly.
type formUser struct {
	createUser
	TTL time.Duration `form:"ttl"`
}

func TestBindJSON(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.Bind[createUser]()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, in)
	})

	req := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"ann","age":30,"tags":["a","b"]}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"name":"ann"`) {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestBindJSONRejectsMalformedBody(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestBindRejectsAnUnknownMediaType(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader("x"))
	req.Header.Set(HeaderContentType, "application/xml")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestBindBodyLimit(t *testing.T) {
	r := newTestRouter()
	r.MaxBodyBytes(16)
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users",
		strings.NewReader(`{"name":"`+strings.Repeat("x", 100)+`"}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestBindForm(t *testing.T) {
	r := newTestRouter()
	r.POST("/users", func(c *tctx) error {
		in, err := c.BindForm[formUser]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%s/%d/%v/%v", in.Name, in.Age, in.Admin, in.TTL)
	})

	body := url.Values{"name": {"bo"}, "age": {"7"}, "admin": {"true"}, "ttl": {"90s"}}
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body.Encode()))
	req.Header.Set(HeaderContentType, MIMEApplicationForm)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got, want := rec.Body.String(), "bo/7/true/1m30s"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBindQuery(t *testing.T) {
	type filter struct {
		Page  int      `query:"page"`
		Limit int      `query:"limit"`
		Sort  []string `query:"sort"`
		Since time.Time
	}

	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		in, err := c.Bind[filter]()
		if err != nil {
			return err
		}
		return c.Stringf(http.StatusOK, "%d/%d/%v/%s",
			in.Page, in.Limit, in.Sort, in.Since.Format(time.RFC3339))
	})

	rec := do(r, http.MethodGet, "/search?page=2&limit=&sort=a&sort=b&since=2026-01-02T03:04:05Z")
	if got, want := rec.Body.String(), "2/0/[a b]/2026-01-02T03:04:05Z"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestBindQueryReportsAParseError(t *testing.T) {
	type filter struct {
		Page int `query:"page"`
	}
	r := newTestRouter()
	r.GET("/search", func(c *tctx) error {
		_, err := c.Bind[filter]()
		return err
	})

	rec := do(r, http.MethodGet, "/search?page=abc")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Page") {
		t.Errorf("body does not name the field: %q", rec.Body.String())
	}
}

func TestParamAsAndQueryAs(t *testing.T) {
	r := newTestRouter()
	r.GET("/users/{id}", func(c *tctx) error {
		id, err := c.ParamAs[int]("id")
		if err != nil {
			return err
		}
		limit := c.QueryAsDefault("limit", 25)
		return c.Stringf(http.StatusOK, "%d/%d", id, limit)
	})

	if got, want := do(r, http.MethodGet, "/users/9").Body.String(), "9/25"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := do(r, http.MethodGet, "/users/9?limit=5").Body.String(), "9/5"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if code := do(r, http.MethodGet, "/users/abc").Code; code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestDecodeValuesFlattensEmbeddedStructs(t *testing.T) {
	type page struct {
		Offset int `query:"offset"`
	}
	type query struct {
		page
		Term string `query:"q"`
	}

	var got query
	err := decodeValues(url.Values{"offset": {"40"}, "q": {"go"}}, &got, "query")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Offset != 40 || got.Term != "go" {
		t.Errorf("got %+v", got)
	}
}

func TestJSONOptionsApplyToBind(t *testing.T) {
	r := newTestRouter()
	r.JSONOptions(json.RejectUnknownMembers(true))
	r.POST("/users", func(c *tctx) error {
		_, err := c.Bind[createUser]()
		return err
	})

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"nope":1}`))
	req.Header.Set(HeaderContentType, MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
