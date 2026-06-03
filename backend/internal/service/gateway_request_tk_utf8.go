package service

import (
	"bytes"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey-only request-shape self-heal for invalid UTF-8 / lone JSON surrogate
// escapes in an outbound Anthropic Messages request body.
//
// Why this lives in TokenKey (gateway) and not in the client
// -----------------------------------------------------------
// Claude Code repeatedly emits request bodies whose string content contains an
// UNPAIRED UTF-16 surrogate — most visibly when a macOS screenshot is dragged
// in, or when a pasted blob carries a truncated multi-byte rune. The upstream
// Anthropic API rejects these verbatim with a hard 400:
//
//	"The request body is not valid JSON: str is not valid UTF-8: surrogate
//	 code point ... is not allowed"
//
// which bricks the user's session. Tracked upstream as anthropics/claude-code
// #60168 ("Please finally fix the JSON low surrogate issue"), #63885
// ("surrogates not allowed when dragging macOS screenshots") and #64777
// ("request body is not valid JSON: str is not valid UTF-8: surrogate").
//
// A relay gateway is the single point that sees every request and can repair
// the byte stream before it reaches Anthropic — exactly the posture TokenKey
// already takes for thinking.type=adaptive (#514), empty text blocks, and
// thinking-block signature errors. Here we make the repair PRE-FLIGHT rather
// than retry-on-400: an unpaired surrogate is NEVER valid in a request sent to
// Anthropic, so there is no legitimate body to misclassify, and repairing it
// up front saves the otherwise-guaranteed failed round trip.
//
// Safety contract
// ---------------
//   - A request with no unpaired surrogate and no invalid UTF-8 byte is
//     returned byte-for-byte unchanged with changed=false (zero allocation on
//     the common path). 99.99% of traffic is untouched.
//   - VALID surrogate PAIRS (a high+low escape pair, e.g. an emoji) are
//     preserved verbatim — only LONE surrogates are rewritten to U+FFFD.
//   - Escaped backslashes ("\\udead" — a literal backslash followed by the
//     text "udead", not a unicode escape) are left intact via escape-pair
//     consumption, so we never mis-read them as a surrogate escape.
//
// This file is a `*_tk_*.go` companion to the upstream-owned gateway_request.go
// (TokenKey rule §5: keep new fork-only request-shape logic out of the upstream
// file so `git merge upstream/main` stays conflict-free).

// tkReplacementEscape is the JSON escape for U+FFFD (the Unicode REPLACEMENT
// CHARACTER), substituted for a lone surrogate escape. It is exactly 6 bytes,
// the same width as the "\uXXXX" it replaces.
var tkReplacementEscape = []byte{'\\', 'u', 'f', 'f', 'f', 'd'}

// tkReplacementRune is U+FFFD encoded as raw UTF-8 bytes (0xEF 0xBF 0xBD),
// substituted for each maximal run of invalid UTF-8 bytes.
var tkReplacementRune = []byte(string(utf8.RuneError))

// tkUnicodeEscapePrefix is the 2-byte fast-path needle: a body with no `\u`
// substring cannot contain a JSON surrogate escape at all.
var tkUnicodeEscapePrefix = []byte(`\u`)

// TkSanitizeRequestBodyUTF8 returns body with every lone JSON surrogate escape
// and every invalid raw UTF-8 byte run replaced by U+FFFD. The second return
// value reports whether any replacement was made; when false, the first return
// value is the input slice unchanged.
//
// This is the pure, side-effect-free core (exhaustively unit-tested in
// gateway_request_tk_utf8_test.go). Call TkSanitizeRequestBody for the
// logging-wrapped form used at the forward call sites.
func TkSanitizeRequestBodyUTF8(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}

	// Stage 1: lone surrogate ESCAPES inside JSON string literals (\uXXXX).
	// These are valid UTF-8 at the byte level (ASCII), so utf8.Valid below
	// will NOT catch them — they must be handled by scanning the escapes.
	out, changed := tkReplaceLoneSurrogateEscapes(body)

	// Stage 2: raw invalid UTF-8 byte sequences (e.g. a truncated multi-byte
	// rune in a pasted blob). bytes.ToValidUTF8 replaces each maximal invalid
	// run with the replacement; we gate on utf8.Valid so a well-formed body
	// stays allocation-free and changed stays false.
	if !utf8.Valid(out) {
		out = bytes.ToValidUTF8(out, tkReplacementRune)
		changed = true
	}

	return out, changed
}

// tkReplaceLoneSurrogateEscapes rewrites every UNPAIRED \uXXXX surrogate escape
// in body to the U+FFFD replacement escape, preserving valid high+low pairs and all other
// escapes verbatim. Output is allocated lazily: a body with no lone surrogate
// is returned unchanged with changed=false.
func tkReplaceLoneSurrogateEscapes(body []byte) ([]byte, bool) {
	// Fast path: no JSON unicode escape anywhere → cannot contain a lone
	// surrogate escape.
	if !bytes.Contains(body, tkUnicodeEscapePrefix) {
		return body, false
	}

	var out []byte // nil until the first replacement (lazy alloc)
	n := len(body)
	i := 0
	for i < n {
		c := body[i]
		if c != '\\' {
			if out != nil {
				out = append(out, c)
			}
			i++
			continue
		}

		// c == '\\': start of an escape sequence; need at least one more byte.
		if i+1 >= n {
			if out != nil {
				out = append(out, c)
			}
			i++
			continue
		}

		if body[i+1] != 'u' {
			// Two-char escape (\\ \" \/ \n \t \r \b \f). Consume BOTH bytes so
			// an escaped backslash ("\\") never leaves its second byte to be
			// mis-read as the start of a unicode escape.
			if out != nil {
				out = append(out, c, body[i+1])
			}
			i += 2
			continue
		}

		// "\uXXXX" — need 4 hex digits.
		if i+6 > n {
			// Malformed trailing escape; copy the remainder verbatim.
			if out != nil {
				out = append(out, body[i:]...)
			}
			i = n
			continue
		}
		hi, ok := tkParseHex4(body[i+2 : i+6])
		if !ok {
			if out != nil {
				out = append(out, body[i:i+6]...)
			}
			i += 6
			continue
		}

		switch {
		case hi >= 0xD800 && hi <= 0xDBFF:
			// High surrogate: valid ONLY when immediately followed by a
			// \uDC00–\uDFFF low surrogate. Otherwise it is lone → replace.
			if i+12 <= n && body[i+6] == '\\' && body[i+7] == 'u' {
				if lo, ok2 := tkParseHex4(body[i+8 : i+12]); ok2 && lo >= 0xDC00 && lo <= 0xDFFF {
					// Valid surrogate pair: keep both escapes verbatim.
					if out != nil {
						out = append(out, body[i:i+12]...)
					}
					i += 12
					continue
				}
			}
			out = tkEnsureLazyCopy(out, body, i)
			out = append(out, tkReplacementEscape...)
			i += 6
		case hi >= 0xDC00 && hi <= 0xDFFF:
			// Lone low surrogate (no preceding high surrogate) → replace.
			out = tkEnsureLazyCopy(out, body, i)
			out = append(out, tkReplacementEscape...)
			i += 6
		default:
			// Ordinary BMP escape → keep verbatim.
			if out != nil {
				out = append(out, body[i:i+6]...)
			}
			i += 6
		}
	}

	if out == nil {
		return body, false
	}
	return out, true
}

// tkEnsureLazyCopy materializes the lazily-allocated output buffer on the first
// replacement: it copies body[:i] (the verbatim prefix processed so far) into a
// fresh buffer. A no-op once out is non-nil.
func tkEnsureLazyCopy(out, body []byte, i int) []byte {
	if out != nil {
		return out
	}
	out = make([]byte, 0, len(body)+len(tkReplacementEscape))
	out = append(out, body[:i]...)
	return out
}

// tkParseHex4 parses exactly 4 hexadecimal digits into a uint16 code unit.
// Returns ok=false on any non-hex byte (the caller then keeps the escape
// verbatim rather than risk corrupting an unexpected shape).
func tkParseHex4(b []byte) (uint16, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var v uint16
	for k := 0; k < 4; k++ {
		var d uint16
		switch c := b[k]; {
		case c >= '0' && c <= '9':
			d = uint16(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint16(c-'A') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}

// TkSanitizeRequestBody is the logging-wrapped form of TkSanitizeRequestBodyUTF8
// used at the Forward / API-Key passthrough / count_tokens pre-filter sites.
// It returns the (possibly repaired) body and emits a single structured log
// line when a repair was applied, so production can measure how often this
// surfaces per account without an upstream 400 ever being incurred.
func TkSanitizeRequestBody(body []byte, account *Account) []byte {
	sanitized, changed := TkSanitizeRequestBodyUTF8(body)
	if !changed {
		return body
	}
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	logger.LegacyPrintf("service.gateway",
		"[Forward] sanitized invalid UTF-8 / lone surrogate escape in request body before upstream forward (prevents Anthropic 'str is not valid UTF-8' 400): account=%d original_bytes=%d sanitized_bytes=%d",
		accountID, len(body), len(sanitized))
	return sanitized
}
