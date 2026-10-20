// Package pagination holds the paging values of a list request, as fields
// that router.Base.Bind fills from the query.
//
// Embed [Page] for numbered pages, read from page and per_page, or [Cursor]
// for keyset pages, read from after and limit, in the request struct of the
// handler:
//
//	type listUsers struct {
//		pagination.Page
//		Role string `query:"role"`
//	}
//
//	func list(c *Context) error {
//		req, err := c.Bind[listUsers]()
//		if err != nil {
//			return err
//		}
//		users, err := c.Queries.ListUsers(c, db.ListUsersParams{
//			Role:   req.Role,
//			Limit:  req.Limit(50),
//			Offset: req.Offset(50),
//		})
//		...
//	}
//
// The default tags fill a value the query leaves out. The Validate method of
// the embedded struct is promoted to the request struct, so Bind refuses a
// value out of range with a 422 that names the field, and a value that is not
// a number with a 400. A request struct with a Validate of its own calls the
// one of Page or Cursor itself, and so does one that embeds both.
//
// Limit and Offset take the cap of the endpoint and report values ready for a
// LIMIT and an OFFSET clause, as the int32 that sqlc generates for them. Both
// keep to their range for any value, bound or not, so a zero Page reads as the
// first page.
//
// A request struct with query keys of its own, such as cursor in place of
// after, declares its own fields and calls [CheckLimit] from its Validate, for
// the same 422.
//
// The package does not count rows, build SQL, or encode cursors: an After
// value is whatever the previous answer handed out.
package pagination

import (
	"errors"
	"math"
	"strconv"

	"github.com/dmitrymomot/go-router"
)

// DefaultPerPage is the page size of a request that names none. The default
// tags of [Page] and [Cursor] write it out.
const DefaultPerPage = 20

// MaxPerPage is the largest page size that Validate takes.
const MaxPerPage = 100

// Page is a numbered page: Number, read from page, counts from 1, and
// PerPage, read from per_page, is its size.
type Page struct {
	Number  int `query:"page" default:"1"`
	PerPage int `query:"per_page" default:"20"`
}

// Validate refuses a page below 1, a per_page outside 1 to [MaxPerPage], and a
// page so far on that its offset does not fit an int32, each as a
// [router.FieldError] named after its query key.
func (p Page) Validate() error {
	var errs []error
	if p.Number < 1 {
		errs = append(errs, router.FieldError{Field: "page", Message: "must be 1 or more"})
	}
	if err := CheckLimit("per_page", p.PerPage, MaxPerPage); err != nil {
		errs = append(errs, err)
	}
	if errs == nil && int64(p.Number-1) > math.MaxInt32/int64(p.PerPage) {
		errs = append(errs, router.FieldError{Field: "page", Message: "is too far on"})
	}
	return errors.Join(errs...)
}

// Limit reports the page size, capped at maxPerPage, which a value below 1
// leaves at [MaxPerPage]. A PerPage below 1 reads as [DefaultPerPage].
func (p Page) Limit(maxPerPage int) int32 { return limit(p.PerPage, maxPerPage) }

// Offset reports how many rows the pages before this one hold, at the size
// that [Page.Limit] reports for the same maxPerPage. A Number below 1 reads
// as the first, and an offset past the int32 range reads as [math.MaxInt32].
func (p Page) Offset(maxPerPage int) int32 {
	// A cap at MaxInt32 pages keeps the product inside an int64 on any
	// platform.
	before := int64(min(max(p.Number, 1)-1, math.MaxInt32))
	off := min(before*int64(p.Limit(maxPerPage)), math.MaxInt32)
	return int32(off) //nolint:gosec // off is between 0 and math.MaxInt32.
}

// Cursor is a keyset page: after is the opaque position that the previous
// page handed out, empty for the first page, and limit is its size.
type Cursor struct {
	After   string `query:"after"`
	PerPage int    `query:"limit" default:"20"`
}

// Validate refuses a limit outside 1 to [MaxPerPage], as a
// [router.FieldError] named "limit".
func (c Cursor) Validate() error { return CheckLimit("limit", c.PerPage, MaxPerPage) }

// Limit reports the page size, capped at maxPerPage, which a value below 1
// leaves at [MaxPerPage]. A PerPage below 1 reads as [DefaultPerPage]. Ask the
// database for one row more to learn whether a next page exists.
func (c Cursor) Limit(maxPerPage int) int32 { return limit(c.PerPage, maxPerPage) }

// CheckLimit refuses a page size n outside 1 to maxPerPage as a
// [router.FieldError] named field, as Validate of [Page] and [Cursor] does. A
// maxPerPage below 1 or above [MaxPerPage] reads as MaxPerPage, the cap that
// Limit applies too.
//
// It serves a request struct with keys of its own, such as cursor in place of
// after:
//
//	type listUsers struct {
//		Cursor string `query:"cursor"`
//		Size   int    `query:"size" default:"20"`
//	}
//
//	func (l listUsers) Validate() error { return pagination.CheckLimit("size", l.Size, 50) }
func CheckLimit(field string, n, maxPerPage int) error {
	maxPerPage = capOf(maxPerPage)
	switch {
	case n < 1:
		return router.FieldError{Field: field, Message: "must be 1 or more"}
	case n > maxPerPage:
		return router.FieldError{Field: field, Message: "must be " + strconv.Itoa(maxPerPage) + " or less"}
	}
	return nil
}

// capOf reports the cap of an endpoint: maxPerPage, or [MaxPerPage] for a
// value below 1 or above it.
func capOf(maxPerPage int) int {
	if maxPerPage < 1 || maxPerPage > MaxPerPage {
		return MaxPerPage
	}
	return maxPerPage
}

func limit(perPage, maxPerPage int) int32 {
	// The bounds stay in sight here, so the conversion below is known safe.
	if maxPerPage < 1 || maxPerPage > MaxPerPage {
		maxPerPage = MaxPerPage
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	return int32(min(perPage, maxPerPage))
}
