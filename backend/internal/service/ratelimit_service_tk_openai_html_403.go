package service

import "bytes"

// openAIHTMLBodyMarkers are the document-shape tags that identify an OpenAI
// 403 response body as HTML rather than the structured JSON error envelope
// (`{"error":{"code":"...","message":"..."}}`) that legitimate account-level
// 403s carry. Any 403 whose body looks like an HTML document — Cloudflare
// challenge, Arkose FunCaptcha, OpenAI's own UA / bot-detect access-denied
// page (issue Wei-Shaw/sub2api#2413), proxy interstitials, etc. — is
// per-request infrastructure noise; the OAuth identity is healthy and must
// not be poisoned with a cooldown.
//
// Match is case-insensitive and runs only against the first openAIHTMLProbe
// bytes of the body to keep the check O(1) for large bodies.
var openAIHTMLBodyMarkers = [][]byte{
	[]byte("<!doctype html"),
	[]byte("<html"),
	[]byte("<head"),
	[]byte("<body"),
	[]byte("<meta"),
	[]byte("<style"),
}

const openAIHTMLProbe = 2048

// openAIIsHTMLBody returns true when body looks like an HTML document. It is
// the shape-based companion to openAICloudflareChallengeKeywords: keywords
// match known challenge platforms by name, while openAIIsHTMLBody catches
// every other HTML 403 shape — most importantly OpenAI's own access-denied
// page, whose body contains none of the CF/Arkose keywords (see issue #2413
// sample: `.logo{color:#8e8ea0}`, `scale-appear`, OpenAI-branded layout).
//
// The invariant we rely on is documented at ratelimit_service.go:128 — a
// real OpenAI account-level 403 returns structured JSON. Any HTML 403 is
// upstream infrastructure rejecting *this request*, not OpenAI rejecting
// *this account*.
func openAIIsHTMLBody(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n\xef\xbb\xbf")
	if len(trimmed) == 0 || trimmed[0] != '<' {
		return false
	}
	head := trimmed
	if len(head) > openAIHTMLProbe {
		head = head[:openAIHTMLProbe]
	}
	lower := bytes.ToLower(head)
	for _, marker := range openAIHTMLBodyMarkers {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	return false
}
