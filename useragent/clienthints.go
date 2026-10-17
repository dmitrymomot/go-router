package useragent

import (
	"net/http"
	"strings"

	"github.com/dmitrymomot/go-router"
)

// ParseHeaders parses the User-Agent header, then takes the brand and
// version of the browser, the platform, the model and the mobile flag from
// the Sec-CH-UA headers that h carries. A Chromium browser freezes its
// User-Agent: the version is capped, Windows always reads "10" and macOS
// always "10.15.7", and the real values come only as Client Hints. A missing
// header leaves the value of the string alone, and a malformed one is
// ignored. A bot keeps what its string says.
//
// A browser sends the platform version, the model and the full version list
// only to a server that asked for them in Accept-CH.
func ParseHeaders(h http.Header) UserAgent {
	res := Parse(h.Get(router.HeaderUserAgent))
	if res.IsBot() {
		return res
	}
	applyClientHints(&res, h)
	return res
}

func applyClientHints(res *UserAgent, h http.Header) {
	if name, ver, ok := pickBrand(h.Get("Sec-CH-UA-Full-Version-List")); ok {
		if name != res.Browser.Name {
			res.Browser.Name = name
			res.Browser.Version = Version{}
		}
		if v := parseVersion(ver); v.Major > 0 {
			res.Browser.Version = v
		}
	} else if name, ver, ok := pickBrand(h.Get("Sec-CH-UA")); ok {
		if name != res.Browser.Name {
			res.Browser.Name = name
			res.Browser.Version = Version{}
		}
		// Sec-CH-UA carries major-only versions; the string-parsed full
		// version is richer, so only override on disagreement or absence.
		if v := parseVersion(ver); v.Major > 0 && v.Major != res.Browser.Version.Major {
			res.Browser.Version = v
		}
	}
	if p := unquote(h.Get("Sec-CH-UA-Platform")); p != "" {
		applyPlatform(res, p, parseVersion(unquote(h.Get("Sec-CH-UA-Platform-Version"))))
	}
	if m := unquote(h.Get("Sec-CH-UA-Model")); m != "" && !strings.EqualFold(m, "k") {
		res.Device.Model = m
		if b := brandFromModel(m); b != "" {
			res.Device.Brand = b
		}
	}
	if h.Get("Sec-CH-UA-Mobile") == "?1" &&
		(res.Device.Type == DeviceUnknown || res.Device.Type == DeviceDesktop) {
		res.Device.Type = DeviceMobile
	}
}

// pickBrand parses a structured-header brand list like
//
//	"Chromium";v="138", "Brave";v="138.0.7204.97", "Not?A_Brand";v="99"
//
// preferring the most specific brand: any non-GREASE, non-Chromium,
// non-"Google Chrome" brand first (that is how Brave, Arc, Edge and Opera
// surface), then Google Chrome, then Chromium.
func pickBrand(list string) (name, version string, ok bool) {
	var chromium, chrome, other [2]string
	for item := range strings.SplitSeq(list, ",") {
		brand, ver := parseBrandItem(item)
		if brand == "" || isGrease(brand) {
			continue
		}
		switch {
		case strings.EqualFold(brand, "chromium"):
			chromium = [2]string{"Chromium", ver}
		case strings.EqualFold(brand, "google chrome"):
			chrome = [2]string{"Chrome", ver}
		case other[0] == "":
			other = [2]string{canonicalBrand(brand), ver}
		}
	}
	switch {
	case other[0] != "":
		return other[0], other[1], true
	case chrome[0] != "":
		return chrome[0], chrome[1], true
	case chromium[0] != "":
		return chromium[0], chromium[1], true
	}
	return "", "", false
}

// parseBrandItem reads one `"Brand";v="1.2.3"` structured-header item. The
// brand has to be the quoted string the item starts with: an item such as
// `Brave;v="1"` would otherwise name the browser "1".
func parseBrandItem(item string) (brand, ver string) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(item), `"`)
	if !ok {
		return "", ""
	}
	brand, rest, ok = strings.Cut(rest, `"`)
	if !ok {
		return "", ""
	}
	if k := strings.Index(rest, `;v="`); k >= 0 {
		rest := rest[k+4:]
		if before, _, found := strings.Cut(rest, `"`); found {
			ver = before
		}
	}
	return brand, ver
}

// isGrease detects GREASE brands ("Not?A_Brand", "Not/A)Brand",
// "Not A;Brand", ...) by comparing letters only.
func isGrease(brand string) bool {
	var b strings.Builder
	for i := range len(brand) {
		c := brand[i]
		if c >= 'A' && c <= 'Z' {
			c |= 0x20
		}
		if c >= 'a' && c <= 'z' {
			b.WriteByte(c)
		}
	}
	return b.String() == "notabrand"
}

// canonicalBrand maps CH brand strings onto the same canonical names the
// string parser produces.
func canonicalBrand(brand string) string {
	switch strings.ToLower(brand) {
	case "microsoft edge":
		return "Edge"
	case "samsung internet":
		return "Samsung Internet"
	case "opera gx":
		return "Opera"
	case "yandex":
		return "Yandex"
	default:
		return brand // "Brave", "Arc", "Opera", "Vivaldi", ... already canonical
	}
}

// applyPlatform maps Sec-CH-UA-Platform (+ Platform-Version) onto OS.
// Chromium reports platformVersion >= 13 on Windows 11 and 1..12 on
// Windows 10.
func applyPlatform(res *UserAgent, platform string, pv Version) {
	switch platform {
	case "Windows":
		res.OS.Name = "Windows"
		switch {
		case pv.Major >= 13:
			res.OS.Version = Version{Major: 11, Full: "11"}
		case pv.Major > 0:
			res.OS.Version = Version{Major: 10, Full: "10"}
		}
	case "macOS":
		res.OS.Name = "macOS"
		if pv.Major > 0 {
			res.OS.Version = pv
		}
	case "Android", "Linux", "iOS":
		res.OS.Name = platform
		if pv.Major > 0 {
			res.OS.Version = pv
		}
	case "Chrome OS", "Chromium OS":
		res.OS.Name = "ChromeOS"
		if pv.Major > 0 {
			res.OS.Version = pv
		}
	}
	// Unknown platform values leave the string-parsed OS untouched.
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
