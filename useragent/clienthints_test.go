package useragent_test

import (
	"net/http"
	"testing"

	"github.com/dmitrymomot/go-router/useragent"
)

const frozenChromeMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"

const frozenChromeWin = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"

func headers(ua string, kv map[string]string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", ua)
	for k, v := range kv {
		h.Set(k, v)
	}
	return h
}

func TestClientHintsBrave(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA": `"Brave";v="138", "Chromium";v="138", "Not?A_Brand";v="99"`,
	}))
	equal(t, "Brave", got.Browser.Name)
	equal(t, 138, got.Browser.Version.Major)
}

func TestClientHintsEdgeAndArc(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA": `"Microsoft Edge";v="138", "Chromium";v="138", "Not/A)Brand";v="24"`,
	}))
	equal(t, "Edge", got.Browser.Name)

	got = useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA": `"Arc";v="1", "Chromium";v="138", "Not A;Brand";v="99"`,
	}))
	equal(t, "Arc", got.Browser.Name)
}

func TestClientHintsFullVersionUnfreezes(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA-Full-Version-List": `"Chromium";v="138.0.7204.97", "Google Chrome";v="138.0.7204.97", "Not/A)Brand";v="99.0.0.0"`,
	}))
	equal(t, "Chrome", got.Browser.Name)
	equal(t, useragent.Version{Major: 138, Minor: 0, Patch: 7204, Full: "138.0.7204.97"}, got.Browser.Version)
}

func TestClientHintsFullVersionListNameChangeEmptyVersion(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA-Full-Version-List": `"Brave";v="", "Chromium";v="138.0.7204.97"`,
	}))
	equal(t, "Brave", got.Browser.Name)
	equal(t, 0, got.Browser.Version.Major)
}

func TestClientHintsWindows11(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA-Platform":         `"Windows"`,
		"Sec-CH-UA-Platform-Version": `"15.0.0"`,
	}))
	equal(t, "Windows", got.OS.Name)
	equal(t, "11", got.OS.Version.Full)

	got = useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA-Platform":         `"Windows"`,
		"Sec-CH-UA-Platform-Version": `"10.0.0"`,
	}))
	equal(t, "10", got.OS.Version.Full)
}

func TestClientHintsMacRealVersion(t *testing.T) {
	frozenMac := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
	got := useragent.ParseHeaders(headers(frozenMac, map[string]string{
		"Sec-CH-UA-Platform":         `"macOS"`,
		"Sec-CH-UA-Platform-Version": `"14.5.0"`,
	}))
	equal(t, "macOS", got.OS.Name)
	equal(t, 14, got.OS.Version.Major)
}

func TestClientHintsModelAndMobile(t *testing.T) {
	reduced := "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Mobile Safari/537.36"
	got := useragent.ParseHeaders(headers(reduced, map[string]string{
		"Sec-CH-UA-Model": `"Pixel 8 Pro"`,
	}))
	equal(t, "Pixel 8 Pro", got.Device.Model)
	equal(t, "Google", got.Device.Brand)

	got = useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA-Mobile": "?1",
	}))
	equal(t, useragent.DeviceMobile, got.Device.Type)
}

func TestClientHintsMissingHeadersLeaveParseUntouched(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, nil))
	equal(t, useragent.Parse(frozenChromeWin), got)
}

func TestClientHintsMalformedIgnored(t *testing.T) {
	got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
		"Sec-CH-UA":                  `;;;"""not even close`,
		"Sec-CH-UA-Platform":         `"`,
		"Sec-CH-UA-Platform-Version": `"garbage"`,
		"Sec-CH-UA-Model":            `""`,
	}))
	equal(t, "Chrome", got.Browser.Name)
	equal(t, "Windows", got.OS.Name)
}

func TestClientHintsSkippedForBots(t *testing.T) {
	got := useragent.ParseHeaders(headers("Googlebot/2.1 (+http://www.google.com/bot.html)", map[string]string{
		"Sec-CH-UA": `"Brave";v="138"`,
	}))
	isTrue(t, got.IsBot())
	equal(t, "", got.Browser.Name)
}

func TestClientHintsPlatforms(t *testing.T) {
	tests := []struct {
		platform, version string
		os, osVersion     string
	}{
		{`"Windows"`, `"15.0.0"`, "Windows", "11"},
		{`"Windows"`, `"10.0.0"`, "Windows", "10"},
		{`"macOS"`, `"14.5.0"`, "macOS", "14.5.0"},
		{`"Linux"`, `"6.1"`, "Linux", "6.1"},
		{`"Chrome OS"`, `"16093.68.0"`, "ChromeOS", "16093.68.0"},
		{`"Chromium OS"`, `""`, "ChromeOS", "10.15.7"},
		{`"Fuchsia"`, `"1"`, "macOS", "10.15.7"},
	}
	for _, tt := range tests {
		t.Run(tt.platform+" "+tt.version, func(t *testing.T) {
			got := useragent.ParseHeaders(headers(frozenChromeMac, map[string]string{
				"Sec-CH-UA-Platform":         tt.platform,
				"Sec-CH-UA-Platform-Version": tt.version,
			}))
			equal(t, tt.os, got.OS.Name)
			equal(t, tt.osVersion, got.OS.Version.Full)
		})
	}
}

func TestClientHintsCanonicalBrands(t *testing.T) {
	for brand, want := range map[string]string{
		"Microsoft Edge":   "Edge",
		"Samsung Internet": "Samsung Internet",
		"Opera GX":         "Opera",
		"YaNdEx":           "Yandex",
		"Vivaldi":          "Vivaldi",
	} {
		got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{
			"Sec-CH-UA": `"Chromium";v="138", "` + brand + `";v="138", "Not?A_Brand";v="99"`,
		}))
		equal(t, want, got.Browser.Name, brand)
	}
}

func TestClientHintsMalformedBrandLists(t *testing.T) {
	for _, list := range []string{`Brave;v="1"`, `"Brave`, `"Not A;Brand";v="99"`, `"Chromium"`} {
		got := useragent.ParseHeaders(headers(frozenChromeWin, map[string]string{"Sec-CH-UA": list}))
		if list == `"Chromium"` {
			equal(t, "Chromium", got.Browser.Name, list)
			continue
		}
		equal(t, "Chrome", got.Browser.Name, list)
	}
}
