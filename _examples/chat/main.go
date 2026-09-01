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

type Context struct {
	router.Base
	Room *room
	User string
}

type Ctx = *Context

const addr = "localhost:8080"

const maxBodyBytes = 8 << 10

func main() {
	rm := newRoom()
	r := newRouter(rm)

	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
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

func newRouter(rm *room) *router.Router[Ctx] {
	r := router.New(func(http.ResponseWriter, *http.Request) Ctx {
		return &Context{Room: rm}
	})

	// Logger wraps Recover, so the request line carries the recovered panic.
	r.Use(
		middleware.Logger[Ctx],
		middleware.Recover[Ctx],
		middleware.Secure[Ctx],
		middleware.BodyLimit[Ctx](maxBodyBytes),
		middleware.CSRFWithConfig[Ctx](middleware.CSRFConfig{
			CookieHTTPOnly: true,
			CookieSameSite: http.SameSiteLaxMode,
		}),
	)

	r.GET("/", index)
	r.POST("/join", join)
	r.POST("/leave", leave)

	r.Group(func(g *router.Router[Ctx]) {
		g.Use(requireUser)

		g.GET("/chat", chat)
		g.POST("/messages", postMessage)
		g.GET("/events", events)
	})

	return r
}

func index(c Ctx) error {
	if _, ok := readUser(c); ok {
		return c.Redirect(http.StatusSeeOther, "/chat")
	}
	return c.Render(http.StatusOK, tmpl("index", joinForm{
		CSRFToken: middleware.CSRFTokenFrom(c),
	}))
}

type joinInput struct {
	Name string `form:"name"`
}

type joinForm struct {
	CSRFToken string
	Name      string
	Error     string
}

func join(c Ctx) error {
	in, err := c.Bind[joinInput]()
	if err != nil {
		return err
	}

	name := cleanName(in.Name)
	if name == "" {
		return c.Render(http.StatusOK, tmpl("join", joinForm{
			CSRFToken: middleware.CSRFTokenFrom(c),
			Name:      in.Name,
			Error:     "Type a name of 1 to " + maxNameRunesText + " characters.",
		}))
	}

	writeUser(c, name)

	return c.HX().Redirect("/chat")
}

func leave(c Ctx) error {
	clearUser(c)
	return c.HX().Redirect("/")
}

type chatPage struct {
	CSRFToken string
	Name      string
}

func chat(c Ctx) error {
	return c.Render(http.StatusOK, tmpl("chat", chatPage{
		CSRFToken: middleware.CSRFTokenFrom(c),
		Name:      c.User,
	}))
}

type messageInput struct {
	Text string `form:"text"`
}

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

	return c.HX().Trigger("message-sent").NoSwap()
}

func events(c Ctx) error {
	ch, unsubscribe := c.Room.join()
	defer unsubscribe()

	c.Room.broadcast(notice(c.User, "joined the chat"))
	defer c.Room.broadcast(notice(c.User, "left the chat"))

	return router.ServeSSE(c, ch, sendTo(c.User),
		router.SSEHeartbeat(20*time.Second),
		router.SSERetry(2*time.Second),
	)
}

func requireUser(next router.HandlerFunc[Ctx]) router.HandlerFunc[Ctx] {
	return func(c Ctx) error {
		name, ok := readUser(c)
		if !ok {
			if strings.Contains(c.Header().Get(router.HeaderAccept), router.MIMETextEventStream) {
				return router.ErrUnauthorized
			}
			return c.HX().Redirect("/")
		}
		c.User = name
		return next(c)
	}
}
