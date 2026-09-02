# tenants

A multi-tenant web app in about 700 lines: a landing page and a signup form on the base domain, and one route table that serves every workspace on a subdomain of its own.

```bash
go run .
```

Then open <http://lvh.me:8080> and create a workspace. The app sends you to its address, such as <http://acme.lvh.me:8080>.

`lvh.me` and every name under it resolve to 127.0.0.1, so the subdomains work with no entry in `/etc/hosts` and no DNS of your own.

Nothing is stored. The workspaces and their accounts live in maps that go away with the process, and so does the key that signs the session.

## An account belongs to a workspace

The apex sells the product and makes workspaces. It has no login and no session of its own, because there is nobody to be there: an account exists inside one workspace, and that workspace's subdomain is where it signs in. The same address may hold an account in two workspaces, and they are two accounts with two passwords.

## What happens

| Step | Request | Answer |
| --- | --- | --- |
| Read the pitch | `GET lvh.me/` | the landing page |
| Open the signup form | `GET lvh.me/signup` | the form, with a CSRF token |
| Create a workspace | `POST lvh.me/signup` | `303` to `acme.lvh.me/enter?ticket=…`, or the form again with the reason |
| Arrive | `GET acme.lvh.me/enter?ticket=…` | the session cookie, then `303` to the dashboard |
| Read the workspace | `GET acme.lvh.me/` | the dashboard, signed in or as a guest |
| Come back | `GET acme.lvh.me/login` | the door of that workspace |
| Sign in | `POST acme.lvh.me/login` | `303` to the dashboard, or the form again |
| Leave | `POST acme.lvh.me/signout` | `303` to the door |
| Ask an unknown host | `GET 127.0.0.1:8080/` | `404` and a page naming the hosts that do answer |

## The five pieces worth copying

**A host is a route.** The apex and every workspace are separate scopes of one router, and the subdomain is a parameter like any path segment:

```go
r.Hosts([]string{baseDomain, "www." + baseDomain}, apexRoutes)
r.Host("{tenant}."+baseDomain, workspaceRoutes)
r.Host("*", func(h *router.Router[Ctx]) { h.GET("/", unknownHost) })
```

An exact host wins over a pattern, so `www.lvh.me` never reads as a workspace named `www`. The port never appears in a pattern: the router strips it before it matches, so the same three lines serve `lvh.me:8080` here and `lvh.me` in production.

**The subdomain reaches the handler as a parameter.** One middleware turns it into the workspace, and every handler under it reads `c.Workspace`:

```go
slug := c.Param("tenant")
w, ok := c.Store.Get(slug)
if !ok {
	return router.ErrNotFound.WithMessage("no workspace at %s", c.Host())
}
```

**A session that belongs to one host.** `SetSignedCookie` signs the email with an HMAC, and the cookie names no `Domain`, so it belongs to the host that set it and to no other. Signing in at `acme.lvh.me` leaves `beta.lvh.me` signed out.

**A ticket to cross the hosts.** Signup runs on the apex, and no server may set a cookie for a host below its own, so the apex cannot start the session it just earned. It hands over a ticket instead: one random id, good for one minute and one visit.

```go
return c.Redirect(http.StatusSeeOther, enterURL(c, w.Slug, c.Store.NewTicket(w.Slug, email)))
```

`GET /enter` on the workspace host spends it and starts the session there. `Redeem` deletes the ticket before it answers, so a second visit, or one to another workspace, lands on the door instead. The id travels in a URL, where a proxy log or a `Referer` can keep it: one minute and one use is what makes that acceptable, and a bearer token with a long life would not be.

**A password kept as a derived key.** `crypto/pbkdf2` is in the standard library, so the account stores a salt and 600 000 rounds of PBKDF2-HMAC-SHA256 rather than the password, and compares in constant time:

```go
func passwordMatches(password string, salt, want []byte) bool {
	return subtle.ConstantTimeCompare(derive(password, salt), want) == 1
}
```

An address with no account in this workspace derives against a throwaway salt before it is refused, so it cannot answer faster than a known one with the wrong password, and both get the same sentence. A signup form has to say that a subdomain is taken; a login form must not say who has an account.

`NewCookieCodec` panics on a key under 32 bytes, so `SESSION_KEY` is either long enough or ignored for one this run generates. A service that can take a dependency should prefer argon2id from `golang.org/x/crypto`. This example takes none.

## The files

| File | Holds |
| --- | --- |
| `main.go` | the context type, the server, the host scopes and the error page |
| `landing.go` | the apex: the pitch and the signup form |
| `auth.go` | signup, the ticket, the login form and what each refuses |
| `workspace.go` | the workspace host: the lookup middleware and the dashboard |
| `password.go` | the salt, the derivation and the constant-time compare |
| `store.go` | the workspaces, their accounts, the tickets and the slug rules |
| `view.go` | the templates, the signed session and the absolute URLs |
| `templates/` | the layout and the six pages |

## What a real app would add

A database, a mail link to prove the address, a rate limit on each door, more than one account per workspace with roles between them, and a custom domain per workspace beside the subdomain. Wildcard TLS is the other half of this in production: one certificate for `*.example.com`, which `serve.Run` takes through `Config.TLSConfig`.
