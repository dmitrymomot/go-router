package router

import (
	"net/http"
	"testing"
)

func TestNegotiateRejectsInvalidQualityValues(t *testing.T) {
	for _, accept := range []string{
		"application/json;q=NaN",
		"application/json;q=Inf",
		"application/json;q=-0.1",
		"application/json;q=1.1",
		"application/json;q=broken",
	} {
		t.Run(accept, func(t *testing.T) {
			if got := negotiate(accept, []string{MIMEApplicationJSON}); got != "" {
				t.Errorf("negotiate(%q) = %q, want no representation", accept, got)
			}
		})
	}
}

func TestAcceptQualityUsesTheMostSpecificRange(t *testing.T) {
	accept := "*/*;q=1, application/json;q=0"
	if got := acceptQuality(accept, MIMEApplicationJSON); got != 0 {
		t.Errorf("acceptQuality(%q, JSON) = %v, want 0", accept, got)
	}
	if got := acceptQuality(accept, MIMETextPlain); got != 1 {
		t.Errorf("acceptQuality(%q, text) = %v, want 1", accept, got)
	}
}

func TestNegotiateBreaksEqualQualityByOfferOrder(t *testing.T) {
	offers := []string{MIMETextHTML, MIMEApplicationJSON}
	if got := negotiate("*/*", offers); got != MIMETextHTML {
		t.Errorf("negotiate = %q, want first offer %q", got, MIMETextHTML)
	}
}

func TestHeaderConstantsAreCanonicalOrListed(t *testing.T) {
	// The constants that keep the spelling of their RFC, as the doc lists.
	customary := map[string]bool{HeaderETag: true, HeaderWWWAuthenticate: true, HeaderXCSRFToken: true, HeaderXRealIP: true}
	for _, name := range []string{
		HeaderAccept,
		HeaderAcceptEncoding,
		HeaderAllow,
		HeaderAuthorization,
		HeaderCacheControl,
		HeaderConnection,
		HeaderContentDisposition,
		HeaderContentEncoding,
		HeaderContentLength,
		HeaderContentSecurityPolicy,
		HeaderContentSecurityPolicyReportOnly,
		HeaderContentType,
		HeaderCookie,
		HeaderETag,
		HeaderForwarded,
		HeaderIdempotencyKey,
		HeaderIfNoneMatch,
		HeaderLastModified,
		HeaderLocation,
		HeaderReferer,
		HeaderReferrerPolicy,
		HeaderRetryAfter,
		HeaderSecFetchSite,
		HeaderSetCookie,
		HeaderStrictTransportSecurity,
		HeaderUserAgent,
		HeaderVary,
		HeaderWWWAuthenticate,
		HeaderXCSRFToken,
		HeaderXContentTypeOptions,
		HeaderXForwardedFor,
		HeaderXForwardedProto,
		HeaderXFrameOptions,
		HeaderXRealIP,
		HeaderXRequestID,
		HeaderAccessControlAllowCredentials,
		HeaderAccessControlAllowHeaders,
		HeaderAccessControlAllowMethods,
		HeaderAccessControlAllowOrigin,
		HeaderAccessControlExposeHeaders,
		HeaderAccessControlMaxAge,
		HeaderAccessControlRequestHeaders,
		HeaderAccessControlRequestMethod,
		HeaderOrigin,
	} {
		canonical := http.CanonicalHeaderKey(name) == name
		if canonical == customary[name] {
			t.Errorf("%q: canonical = %v, but the doc of the constants says otherwise", name, canonical)
		}
		h := http.Header{}
		h.Set(name, "v")
		if h.Get(name) != "v" {
			t.Errorf("%q does not round-trip through Set and Get", name)
		}
	}
}
