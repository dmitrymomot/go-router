# tenants

A multi-tenant web app in about 700 lines: a landing page and a signup form on the base domain, and one route table that serves every workspace on a subdomain of its own, its login page included.

```bash
go run .
```

Then open <http://lvh.me:8080> and create a workspace. The app sends you to its address, such as <http://acme.lvh.me:8080>, and that address is where you sign in from then on.

`lvh.me` and every name under it resolve to 127.0.0.1, so the subdomains work with no entry in `/etc/hosts` and no DNS of your own.

Nothing is stored. The workspaces and the accounts live in maps that go away with the process, and so does the key that signs the session.

## What happens

| Step | Request | Answer |
| --- | --- | --- |
| Read the pitch | `GET lvh.me/` | the landing page, and your workspaces when you are signed in |
| Open the signup form | `GET lvh.me/signup` | the form, with a CSRF token |
| Create an account | `POST lvh.me/signup` | `303` to `acme.lvh.me`, or the form again with the reason |
| Add another workspace | `POST lvh.me/workspaces` | `303` to the new subdomain, or `303` to `/` when signed out |
| Open a workspace | `GET acme.lvh.me/` | the dashboard, or `404` when no workspace owns that subdomain |
| Come back | `GET acme.lvh.me/login` | the login form of that workspace |
| Sign in | `POST acme.lvh.me/login` | `303` to the dashboard, or the form again |
| Ask an unknown host | `GET 127.0.0.1:8080/` | `404` and a page naming the hosts that do answer |

## The five pieces worth copying

**A host is a route.** The apex and every workspace are separate scopes of one router, and the subdomain is a parameter like any path segment:

```go
r.Hosts([]string{baseDomain, "www." + baseDomain}, apexRoutes)
r.Host("{tenant}."+baseDomain, workspaceRoutes)
r.Host("*", func(h *router.Router[Ctx]) { h.GET("/", unknownHost) })
```

An exact host wins over a pattern, so `www.lvh.me` never reads as a workspace named `www`. The port never appears in a pattern: the router strips it before it matches, so the same three lines serve `lvh.me:8080` here and `lvh.me` in production.

**Each door on its own host.** The apex creates workspaces and nothing else: `GET lvh.me/login` is a `404`. An owner signs in at the address of the workspace, so the form can name it and refuse anybody else:

```go
err = c.Store.Authenticate(email, in.Password)
if err != nil || email != c.Workspace.Owner {
	return refuse(c, "login", page, "That email and password do not match.")
}
```

One sentence covers three failures: an unknown address, a wrong password, and an account that owns some other workspace. The form gives away neither who has an account nor who owns what.

**The subdomain reaches the handler as a parameter.** One middleware turns it into the workspace, and every handler under it reads `c.Workspace`:

```go
slug := c.Param("tenant")
w, ok := c.Store.Get(slug)
if !ok {
	return router.ErrNotFound.WithMessage("no workspace at %s", c.Host())
}
```

**A session that crosses the subdomains.** `SetSignedCookie` signs the email with an HMAC, and the `Domain` attribute puts the cookie on the base domain, so the workspace host reads the session the landing page wrote:

```go
c.SetSignedCookie(c.Codec, &http.Cookie{
	Name:   sessionCookie,
	Value:  email,
	Domain: baseDomain,
	// ...
})
```

Without `Domain`, a cookie belongs to the host that set it alone, and the redirect would arrive signed out. With it, every subdomain shares one session, which is what a multi-tenant app wants and also what it must be careful about.

`NewCookieCodec` panics on a key under 32 bytes, so `SESSION_KEY` is either long enough or ignored for one this run generates.

**A password kept as a derived key.** `crypto/pbkdf2` is in the standard library, so the example stores a salt and 600 000 rounds of PBKDF2-HMAC-SHA256 rather than the password, and compares in constant time:

```go
func passwordMatches(password string, salt, want []byte) bool {
	return subtle.ConstantTimeCompare(derive(password, salt), want) == 1
}
```

An unknown address derives against a throwaway salt before it is refused, so it cannot answer faster than a known one with the wrong password, and both get the same sentence. A signup form has to say that an email is taken; a login form must not.

A service that can take a dependency should prefer argon2id from `golang.org/x/crypto`. This example takes none.

## The files

| File | Holds |
| --- | --- |
| `main.go` | the context type, the server, the host scopes and the error page |
| `landing.go` | the apex: the landing page, the second workspace and the session guard |
| `auth.go` | the signup form of the apex, the login form of a workspace, and what each refuses |
| `password.go` | the salt, the derivation and the constant-time compare |
| `workspace.go` | the workspace host: the lookup middleware, the dashboard and the login routes |
| `store.go` | the workspaces and the accounts, the slug rules and the reserved names |
| `view.go` | the templates, the signed session and the absolute URLs |
| `templates/` | the layout and the five pages |

## What a real app would add

A database, a mail link to prove the address, a rate limit on the login form, a custom domain per workspace beside the subdomain, and a member list so a workspace has readers who do not own it. Wildcard TLS is the other half of this in production: one certificate for `*.example.com`, which `serve.Run` takes through `Config.TLSConfig`.
