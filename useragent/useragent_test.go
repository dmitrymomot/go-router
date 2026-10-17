package useragent_test

import (
	"strings"
	"testing"

	"github.com/dmitrymomot/go-router/useragent"
)

func TestParseEmptyString(t *testing.T) {
	res := useragent.Parse("")
	equal(t, "", res.Raw)
	equal(t, useragent.DeviceUnknown, res.Device.Type)
	isFalse(t, res.IsBot())
	equal(t, "", res.Browser.Name)
	equal(t, "", res.OS.Name)
}

func TestParseGarbage(t *testing.T) {
	for _, ua := range []string{
		"!!!???",
		"Mozilla/5.0",
		"\x00\x01\x02",
		"ÜmlautüberallÄÖ",
		strings.Repeat("x", 10*1024), // forces the heap fallback past the 512-byte stack buffer
	} {
		res := useragent.Parse(ua)
		equal(t, ua, res.Raw, "raw must round-trip")
		equal(t, useragent.DeviceUnknown, res.Device.Type)
		isFalse(t, res.IsBot())
	}
}
