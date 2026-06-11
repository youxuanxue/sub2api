package service

import (
	"net/http"
	"testing"
)

// The relayed strict-403 recognition is a wire contract between a strict-mode
// edge (writes CanonicalIngressUARejectedError into its 403 envelope) and the
// prod mirror stub (must fail over WITHOUT advancing the 3/3 ladder). These
// tests pin both directions of the boundary: TokenKey's own phrase skips the
// penalty; genuine Anthropic 403s do not.

func TestSkipRelayedCanonicalIngressRejectPenalty_MatchesOwnRejection(t *testing.T) {
	// Round-trip through the real error type so the needle can never drift from
	// what the edge actually emits.
	edgeMsg := (&CanonicalIngressUARejectedError{IngressUA: "python-requests/2.31"}).Error()
	envelope := `{"type":"error","error":{"type":"permission_error","message":"` + edgeMsg + `"}}`

	if !tkSkipRelayedCanonicalIngressRejectPenalty(http.StatusForbidden, "", []byte(envelope)) {
		t.Fatalf("relayed strict-403 body must skip the anthropic ladder penalty")
	}
	if !tkSkipRelayedCanonicalIngressRejectPenalty(http.StatusForbidden, edgeMsg, nil) {
		t.Fatalf("relayed strict-403 upstream message must skip the anthropic ladder penalty")
	}
}

func TestSkipRelayedCanonicalIngressRejectPenalty_GenuineAnthropic403StillCounts(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		body string
	}{
		{"org disabled", "This organization has been disabled.", `{"type":"error","error":{"type":"permission_error","message":"This organization has been disabled."}}`},
		{"bot challenge html", "", `<html>Just a moment... cloudflare challenge</html>`},
		{"empty body", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tkSkipRelayedCanonicalIngressRejectPenalty(http.StatusForbidden, tc.msg, []byte(tc.body)) {
				t.Fatalf("genuine anthropic 403 %q must keep flowing through the cooldown path", tc.name)
			}
		})
	}
}

func TestSkipRelayedCanonicalIngressRejectPenalty_Only403(t *testing.T) {
	edgeMsg := (&CanonicalIngressUARejectedError{IngressUA: ""}).Error()
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway} {
		if tkSkipRelayedCanonicalIngressRejectPenalty(status, edgeMsg, nil) {
			t.Fatalf("status %d must not match the relayed strict-403 skip", status)
		}
	}
}
