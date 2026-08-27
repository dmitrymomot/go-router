// Command chat is a chat room built from html/template, htmx and server-sent
// events. It shows the htmx support of the router:
//
//   - the form posts with htmx and the answer redirects the browser with
//     HX-Redirect, which a 303 cannot do inside an htmx request;
//   - a message posts with htmx and the answer swaps nothing, because every
//     reader receives the message over one stream instead;
//   - the stream carries rendered HTML, not JSON, which is what the sse
//     extension of htmx swaps into the page.
//
// Nothing is stored. A message reaches whoever is connected at that moment and
// is then gone, so a page that opens late starts empty.
//
// Run it and open two browser windows:
//
//	go run .
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// Context is the request context of this application. Every handler receives
// it, so nothing needs a type assertion.
type Context struct {
	router.Base

	// Room is the chat room that every request shares.
	Room *room

	// User is the name that the visitor typed, which requireUser fills.
	User string
}

// Ctx spares the *Context at every middleware call.
type Ctx = *Context

const addr = "localhost:8080"

func main() {
	rm := newRoom()
	r := newRouter(rm)

	srv := &http.Server{
		Addr:    addr,
		Handler: r,
		// A stream lives for as long as its reader does, so the server sets no
		// write timeout. The router clears the per-request deadline of a
		// stream anyway; a timeout here would still cut an idle connection.
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		// Closing the room ends every stream. Without it Shutdown waits for
		// readers that never stop by themselves.
		rm.close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		//nolint:errcheck // The process is going away either way.
		srv.Shutdown(shutdown)
	}()

	log.Printf("chat on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// newRouter registers every route of the application.
func newRouter(rm *room) *router.Router[Ctx] {
	r := router.New(func(http.ResponseWriter, *http.Request) Ctx {
		return &Context{Room: rm}
	})

	r.Use(middleware.Recover[Ctx], middleware.Logger[Ctx])

	r.GET("/", index)
	r.POST("/join", join)
	r.GET("/leave", leave)

	r.Group(func(g *router.Router[Ctx]) {
		g.Use(requireUser)

		g.GET("/chat", chat)
		g.POST("/messages", postMessage)
		g.GET("/events", events)
	})

	return r
}

// index shows the form that asks for a name.
func index(c Ctx) error {
	if _, ok := readUser(c); ok {
		return c.Redirect(http.StatusSeeOther, "/chat")
	}
	return c.Render(http.StatusOK, tmpl("index", joinForm{}))
}

// joinInput is the body that the form posts.
type joinInput struct {
	Name string `form:"name"`
}

// joinForm is the state of the form, which the answer to a rejected name
// carries back.
type joinForm struct {
	Name  string
	Error string
}

// join takes the name and sends the visitor to the room.
func join(c Ctx) error {
	in, err := c.Bind[joinInput]()
	if err != nil {
		return err
	}

	name := cleanName(in.Name)
	if name == "" {
		// htmx swaps the answer of a 2xx and ignores the body of a 4xx, so the
		// form comes back with the reason and the status stays 200.
		return c.Render(http.StatusOK, tmpl("join", joinForm{
			Name:  in.Name,
			Error: "Type a name of 1 to " + maxNameRunesText + " characters.",
		}))
	}

	writeUser(c, name)

	// The browser cannot follow a 303 here: htmx would follow it inside the
	// request that it made and swap the whole chat page into the form. HX
	// asks the browser to go there instead, and answers a client without
	// JavaScript with the 303 that it does understand.
	return c.HX().Redirect("/chat")
}

// leave forgets the name and sends the visitor back to the form. The room
// learns of it when the stream of that reader ends.
func leave(c Ctx) error {
	clearUser(c)
	return c.HX().Redirect("/")
}

// chatPage is the state of the chat page.
type chatPage struct {
	Name string
}

// chat shows the room.
func chat(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("chat", chatPage{Name: c.User}))
}

// messageInput is the body that the composer posts.
type messageInput struct {
	Text string `form:"text"`
}

// postMessage sends one message to everyone in the room.
func postMessage(c Ctx) error {
	in, err := c.Bind[messageInput]()
	if err != nil {
		return err
	}

	text := cleanText(in.Text)
	if text == "" {
		return c.HX().NoSwap()
	}
	c.Room.broadcast(message{
		Kind:   kindMessage,
		Author: c.User,
		Text:   text,
		At:     time.Now(),
	})

	// The message reaches this page over the stream, like every other page, so
	// the answer carries no HTML: 204 tells htmx to swap nothing. The trigger
	// is what empties the input again.
	return c.HX().Trigger("message-sent").NoSwap()
}

// events streams the room to one reader.
func events(c Ctx) error {
	ch, unsubscribe := c.Room.join()
	defer unsubscribe()

	// The defer that follows runs before unsubscribe, so the room still holds
	// every other reader when the notice goes out.
	c.Room.broadcast(notice(c.User, "joined the chat"))
	defer c.Room.broadcast(notice(c.User, "left the chat"))

	// Each connection renders the room for itself, so a message can say
	// whether the reader wrote it.
	return router.ServeSSE(c, ch, sendTo(c.User),
		router.SSEHeartbeat(20*time.Second),
		router.SSERetry(2*time.Second),
	)
}

// requireUser stops a request that carries no name.
func requireUser(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		name, ok := readUser(c)
		if !ok {
			// An EventSource follows a redirect by itself and then fails on
			// the HTML that it finds, so tell it plainly instead.
			if strings.Contains(c.Header().Get(router.HeaderAccept), router.MIMETextEventStream) {
				return router.ErrUnauthorized
			}
			return c.HX().Redirect("/")
		}
		c.User = name
		return next(c)
	}
}
