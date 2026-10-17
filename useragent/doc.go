// Package useragent parses a User-Agent string, and the UA Client Hints
// beside it, into the browser, the operating system, the device and, for an
// automated client, the bot. It suits the device list of a session and the
// line of an audit log: canonical names, marketing versions of the operating
// system (Windows NT 6.3 reads as "8.1"), the type, brand and model of the
// device, and bots sorted into categories such as search engines, AI
// crawlers, link previews, monitors and HTTP libraries.
//
// Parsing never fails. Input it does not know gives zero values, with
// Device.Type of [DeviceUnknown] and Raw kept. A Chromium browser freezes its
// User-Agent, so read a request through [Of], or [ParseHeaders], which take
// the real values from the Sec-CH-UA headers.
//
//	ua := useragent.Of(c)
//	if ua.IsBot() {
//		c.Logger().Info("crawler", "bot", ua.Bot.Name, "category", ua.Bot.Category)
//	}
//	session.Device = ua.String() // "Chrome 138 on macOS 14 (Desktop)"
//
// [Parse] reads a string kept elsewhere, such as a row of the sessions table.
//
// Bot detection checks a table curated by hand, with an accurate name,
// category and operator, then a table generated from the crawler-user-agents
// dataset at a pinned commit (see the gen directory; a new table is a
// reviewed change, never a download at run time), then generic signs such as
// a "bot" suffix or a "+http" link in a comment. The curated table wins.
//
// It does not detect the CPU or the screen, it sees no Apple hardware beyond
// "iPhone" and "iPad", and it cannot tell an iPad in desktop mode from a Mac,
// which takes script on the client.
package useragent
