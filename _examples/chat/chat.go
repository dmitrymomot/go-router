package main

import (
	"net/http"
	"time"

	"github.com/dmitrymomot/go-router"
	"github.com/dmitrymomot/go-router/middleware"
)

// roomRouter builds the signed-in half as a router of its own. Everything it
// registers sits behind requireUser, so no handler below repeats the check.
func roomRouter() *router.Router[Ctx] {
	// A mounted router never calls this factory: the root builds the context.
	r := router.New(func(http.ResponseWriter, *http.Request) Ctx {
		return new(Context)
	})

	r.Use(requireUser)

	r.GET("/", chat)
	r.POST("/messages", postMessage)
	r.GET("/events", events)

	return r
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
