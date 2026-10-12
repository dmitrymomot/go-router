package router_test

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/dmitrymomot/go-router"
)

func ExampleRouter_Decoder() {
	type Order struct {
		Item string `xml:"item"`
		Qty  int    `xml:"qty"`
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.Decoder("application/xml", func(body io.Reader, v any) error {
		return xml.NewDecoder(body).Decode(v)
	})
	r.POST("/orders", func(c *Context) error {
		in, err := c.Bind[Order]()
		if err != nil {
			return err
		}
		return c.String(http.StatusCreated, fmt.Sprintf("%d x %s", in.Qty, in.Item))
	})

	req := httptest.NewRequest(http.MethodPost, "/orders",
		strings.NewReader(`<order><item>tea</item><qty>2</qty></order>`))
	req.Header.Set("Content-Type", "application/xml")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	fmt.Println(rec.Code, rec.Body.String())
	// Output:
	// 201 2 x tea
}

func ExampleBase_BindQuery() {
	type Filter struct {
		IDs   []int  `query:"ids,split"`
		Sort  string `query:"sort" default:"name"`
		Price struct {
			Min int `query:"min"`
			Max int `query:"max" default:"100"`
		} `query:"price"`
		Labels map[string]string `query:"label"`
	}

	r := router.New(func(http.ResponseWriter, *http.Request) *Context { return new(Context) })
	r.GET("/items", func(c *Context) error {
		f, err := c.BindQuery[Filter]()
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, fmt.Sprintf("%v %s %d-%d %v", f.IDs, f.Sort, f.Price.Min, f.Price.Max, f.Labels))
	})

	req := httptest.NewRequest(http.MethodGet, "/items?ids=1,2,3&price.min=10&label[env]=prod", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	fmt.Println(rec.Code, rec.Body.String())
	// Output:
	// 200 [1 2 3] name 10-100 map[env:prod]
}
