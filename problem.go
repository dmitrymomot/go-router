package router

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
)

type ProblemError struct {
	Type string `json:"type,omitempty"`

	Title string `json:"title,omitempty"`

	Status int `json:"status"`

	Detail string `json:"detail,omitempty"`

	Instance string `json:"instance,omitempty"`
}

func (e *ProblemError) Error() string {
	title := e.Title
	if title == "" {
		title = http.StatusText(e.Status)
	}
	if e.Detail != "" {
		return fmt.Sprintf("%d %s: %s", e.Status, title, e.Detail)
	}
	return fmt.Sprintf("%d %s", e.Status, title)
}

func (e *ProblemError) StatusCode() int { return e.Status }

type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Errors   any    `json:"errors,omitempty"`
	Cause    string `json:"cause,omitempty"`
}

func (p problemBody) withDefaults() problemBody {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	return p
}

func ProblemErrorHandler[C Context](exposeCause bool) ErrorHandlerFunc[C] {
	return func(c C, err error) { writeProblem(c.base(), err, exposeCause) }
}

func writeProblem(b *Base, err error, exposeCause bool) {
	if err == nil {
		return
	}

	p, cause := problemOf(err)
	if p.Status >= http.StatusInternalServerError || cause != nil {
		logFailure(b, err, p.Status)
	}

	if !writableFailure(b, err, p.Status) {
		return
	}

	if exposeCause && cause != nil {
		p.Cause = cause.Error()
	}
	b.res.Header().Set(HeaderContentType, MIMEApplicationProblemJSON)
	b.res.WriteHeader(p.Status)
	//nolint:errcheck // The connection is already failing; nothing to report.
	json.MarshalWrite(b.res, p)
}

func problemOf(err error) (problemBody, error) {
	p := problemBody{Status: StatusOf(err)}

	if he, ok := errors.AsType[*HTTPError](err); ok {
		p.Errors = he.Details
		if he.Message != http.StatusText(p.Status) {
			p.Detail = he.Message
		}
		return p.withDefaults(), he.Err
	}

	if pe, ok := errors.AsType[*ProblemError](err); ok {
		p.Type, p.Title = pe.Type, pe.Title
		p.Detail, p.Instance = pe.Detail, pe.Instance
		return p.withDefaults(), nil
	}

	return p.withDefaults(), err
}
