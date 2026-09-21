package logredact

import (
	"strings"
	"testing"
	"unicode"
)

func TestRedactUnicodeAssignmentsMatchOriginal(t *testing.T) {
	for _, p := range []*textRedactPatterns{defaultTextRedactPatterns, getTextRedactPatterns([]string{"key", "custom-key"}), getTextRedactPatterns([]string{"ключ", "x.custom key"})} {
		for _, key := range []string{"token", "password", "secret", "key", "custom-key", "ключ", "x.custom key"} {
			for _, spelling := range []string{key, strings.ReplaceAll(strings.ReplaceAll(key, "s", "ſ"), "k", "K")} {
				for _, prefix := range []string{"中文", "é", "😀", "\xff", "x", "x-", "ſ", "K"} {
					for _, assignment := range []string{spelling + "=秘密&safe=yes", spelling + ": 秘密,后文", `"` + spelling + `":"秘密 空格"`, spelling + "=\u00a0秘密", spelling + "\u00a0=秘密", spelling + "=\xff秘密"} {
						input := prefix + assignment + " 后文 token=abc,\"password\":\"a b\""
						if got, want := redactUnstructuredText(input, p), redactUnstructuredReference(input, p); got != want {
							t.Fatalf("input %q: got %q, want %q", input, got, want)
						}
					}
				}
			}
		}
	}
}

// Exercise every Unicode simple-fold equivalent of an ASCII key character.
// This also catches Unicode table changes that would invalidate the fast guard.
func TestRedactAssignmentUnicodeFoldCoverage(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		key := "key" + string(c)
		p := getTextRedactPatterns([]string{key})
		for folded := unicode.SimpleFold(c); folded != c; folded = unicode.SimpleFold(folded) {
			for _, spelling := range []string{"key" + string(folded), string(folded) + "key"} {
				p2 := p
				if strings.HasSuffix(spelling, "key") {
					p2 = getTextRedactPatterns([]string{string(c) + "key"})
				}
				for _, input := range []string{spelling + "=秘密", `"` + spelling + `":"秘密"`, "x" + spelling + ": 秘密", "中文" + spelling + "=秘密"} {
					if got, want := redactUnstructuredText(input, p2), redactUnstructuredReference(input, p2); got != want {
						t.Fatalf("input %q: got %q, want %q", input, got, want)
					}
				}
			}
		}
	}
}

func BenchmarkRedactUnicodeAssignment(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"chinese_sparse", strings.Repeat("说明: 构建成功，结果 = 正常；继续验证应用。 ", 256) + "token=synthetic"},
		{"chinese_dense", strings.Repeat("说明 token=synthetic&safe=yes 结果 password: synthetic 正常。 ", 256)},
		{"ascii_sparse", strings.Repeat("Example: build the application; result = successful. ", 256) + "token=synthetic"},
		{"unicode_fold", strings.Repeat("说明: 结果正常。 ", 256) + "toKen=synthetic"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.text)))
			b.ReportAllocs()
			for b.Loop() {
				redactUnstructuredText(tc.text, defaultTextRedactPatterns)
			}
		})
	}
}
