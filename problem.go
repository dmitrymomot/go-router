package router

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
)

// ProblemError is an error that carries the members of an RFC 9457 problem
// document. Return one from a handler to say more about a failure than its
// status code says:
//
//	return &router.ProblemError{
//		Type:   "https://example.com/probs/insufficient-funds",
//		Title:  "The account holds too little credit",
//		Status: http.StatusConflict,
//		Detail: "the balance is 30 and the transfer asks for 50",
//	}
//
// [ProblemErrorHandler] writes it as application/problem+json. It also
// satisfies [StatusCoder], so a router that keeps [DefaultErrorHandler]
// answers the same status with the wire shape of that handler instead.
type ProblemError struct {
	// Type is the URI that identifies the kind of problem, and that the
	// documentation of the API describes. An empty Type reads as
	// "about:blank", which names a problem that the status code covers on its
	// own.
	Type string `json:"type,omitempty"`

	// Title is the short summary of the kind of problem. It reads the same for
	// every occurrence, so put what changes in Detail.
	Title string `json:"title,omitempty"`

	// Status is the HTTP status code of the answer.
	Status int `json:"status"`

	// Detail explains this one occurrence, in a sentence that a person reads.
	Detail string `json:"detail,omitempty"`

	// Instance is the URI that identifies this one occurrence, such as the
	// path of the request or the id of the log record.
	Instance string `json:"instance,omitempty"`
}

// Error implements the error interface.
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

// StatusCode implements [StatusCoder], which is what carries the status of the
// problem to an error handler that knows nothing about RFC 9457.
func (e *ProblemError) StatusCode() int { return e.Status }

// problemBody is the JSON shape that [ProblemErrorHandler] writes. The first
// five members are the ones that RFC 9457 defines, in the order in which it
// lists them, and the last two are extensions:
//
//   - "errors" carries what [HTTPError.Details] holds, which is the field list
//     that the binder reports. The example of the RFC names its own list that
//     way.
//   - "cause" carries the internal cause, and only the development handler
//     fills it.
type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Errors   any    `json:"errors,omitempty"`
	Cause    string `json:"cause,omitempty"`
}

// withDefaults fills the members that RFC 9457 gives a default. A document
// without a type reads as "about:blank", and one of those carries the standard
// text of its status as the title.
func (p problemBody) withDefaults() problemBody {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	return p
}

// ProblemErrorHandler returns an error handler that writes RFC 9457 problem
// documents, as application/problem+json:
//
//	r.ErrorHandler(router.ProblemErrorHandler[*app.Context](false))
//
// It is opt in. The router documents the {status, error, details} shape of
// [DefaultErrorHandler] as its wire format, and a service that answers
// problem documents says so to its clients first.
//
// It answers every error and not only a [ProblemError]. An [HTTPError] brings
// its status, its message as the detail and its details as an "errors" member.
// Any other error carries the standard text of its status alone, so an internal
// message never reaches the client.
//
// The status is always the one that [StatusOf] reads, which is the status that
// [DefaultErrorHandler] writes for the same error, and the logging is the
// logging of that handler down to the record.
//
// With exposeCause it writes that internal cause into a "cause" member. Use it
// in development only:
//
//	r.ErrorHandler(router.ProblemErrorHandler[*app.Context](dev))
func ProblemErrorHandler[C Context](exposeCause bool) ErrorHandlerFunc[C] {
	return func(c C, err error) { writeProblem(c.base(), err, exposeCause) }
}

// writeProblem is the implementation behind [ProblemErrorHandler].
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
	// The service opted into problem documents, so every failure answers with
	// one, whatever the Accept header of the client offers.
	b.res.Header().Set(HeaderContentType, MIMEApplicationProblemJSON)
	b.res.WriteHeader(p.Status)
	//nolint:errcheck // The connection is already failing; nothing to report.
	json.MarshalWrite(b.res, p)
}

// problemOf turns an error into the document that the client reads, and
// returns the internal cause for the log.
//
// The status is the one that [StatusOf] reads, so a client sees the same code
// whichever error handler renders the failure. The members below it follow that
// same order: an [HTTPError] outranks a [ProblemError] that it carries as its
// internal cause, and the answer keeps the status that the handler asked for.
func problemOf(err error) (problemBody, error) {
	p := problemBody{Status: StatusOf(err)}

	if he, ok := errors.AsType[*HTTPError](err); ok {
		p.Errors = he.Details
		// The message of an HTTPError is the text that the client reads. It is
		// the detail and not the title, because a title names the kind of
		// problem and a message that a handler wrote names this occurrence of
		// it. A message that is only the standard text of the status says
		// nothing that the title does not say already.
		if he.Message != http.StatusText(p.Status) {
			p.Detail = he.Message
		}
		return p.withDefaults(), he.Err
	}

	if pe, ok := errors.AsType[*ProblemError](err); ok {
		p.Type, p.Title = pe.Type, pe.Title
		p.Detail, p.Instance = pe.Detail, pe.Instance
		// A handler that returns a problem has described the failure already,
		// so nothing of it is internal.
		return p.withDefaults(), nil
	}

	// The message of any other error stays internal, so the client reads the
	// standard text of the status alone.
	return p.withDefaults(), err
}
