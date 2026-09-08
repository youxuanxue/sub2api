package tokenestimate

import (
	"github.com/tiktoken-go/tokenizer"
	"sync"
)

var (
	estCodec     tokenizer.Codec
	estCodecOnce sync.Once
)

// codec lazily initializes and caches the cl100k_base tokenizer. A nil return
// means initialization failed; callers fall back to the rune heuristic.
func codec() tokenizer.Codec {
	estCodecOnce.Do(func() {
		if c, err := tokenizer.Get(tokenizer.Cl100kBase); err == nil {
			estCodec = c
		}
	})
	return estCodec
}

// countTokens returns an estimated token count for s. It uses cl100k_base when
// available and falls back to a len([]rune)/4 heuristic on any encode failure.
// It never panics and never returns 0 for non-empty input (the fallback floors
// at 1 token for any non-empty string).
func Count(s string) int {
	if s == "" {
		return 0
	}
	if c := codec(); c != nil {
		if n, err := c.Count(s); err == nil {
			return n
		}
	}
	return FallbackCount(s)
}

// fallbackCount approximates token count as ~4 chars/token, flooring at 1 for
// any non-empty string so we never silently bill an interaction as free.
func FallbackCount(s string) int {
	if s == "" {
		return 0
	}
	n := len([]rune(s)) / 4
	if n < 1 {
		n = 1
	}
	return n
}
