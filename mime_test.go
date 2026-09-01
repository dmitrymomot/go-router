package router

import "testing"

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
