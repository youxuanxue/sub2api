//go:build unit

package service

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestUsageEstimateSSOT_NoBillingPathBypassesOwner fails when a usage
// submission site is added or changed without being classified. Every
// production file that calls RecordUsage / RecordUsageWithLongContext must be
// either a text billing choke point that calls the owner hook, or a declared
// non-text site (media/search/audio/embeddings/batch, priced by its own
// units). Contract: docs/approved/usage-estimate-fallback-ssot.md §Owners.
func TestUsageEstimateSSOT_NoBillingPathBypassesOwner(t *testing.T) {
	// file -> owner hook it must call (text choke points).
	textChokePoints := map[string]string{
		"../handler/gateway_handler.go":                         ".gatewayService.ApplyUsageEstimateFallback(",
		"../handler/gateway_handler_tk_usage.go":                ".gatewayService.ApplyUsageEstimateFallback(",
		"../handler/gateway_handler_tk_messages_gemini.go":      ".gatewayService.ApplyUsageEstimateFallback(",
		"../handler/gemini_v1beta_handler.go":                   ".gatewayService.ApplyUsageEstimateFallback(",
		"../handler/openai_gateway_handler_tk_forward_usage.go": ".gatewayService.ApplyOpenAIUsageEstimateFallback(",
	}
	// Declared exceptions with the reason they are not estimated.
	nonText := map[string]string{
		"../handler/openai_gateway_handler_tk_responses_ws.go": "Responses WebSocket turns: no HTTP response writer to capture; WS usage arrives in response.completed and is billed as reported",
		"../handler/openai_images.go":                          "image generation, priced per image",
		"../handler/gateway_web_search.go":                     "web search, priced per 1k searches",
		"../handler/openai_alpha_search.go":                    "alpha search, priced per search",
		"../handler/gemini_cached_contents_handler.go":         "explicit context cache storage",
		"../handler/grok_audio.go":                             "audio, priced per minute/char",
		"../handler/gateway_systemone.go":                      "System One JSON protocol, usage decoded from its own envelope",
		"../handler/grok_media.go":                             "image/video media",
		"../handler/openai_embeddings.go":                      "embeddings, priced on reported input",
		"../handler/openai_gateway_tk_video.go":                "video, priced per second/resolution",
		"./openai_gateway_usage.go":                            "RecordCyberPolicyUsageLog: upstream cyber_policy rejection, not a delivered response",
		"./managed_batch_settlement.go":                        "managed batch settlement, usage comes from the batch output file",
	}

	callRe := regexp.MustCompile(`\.RecordUsage(WithLongContext)?\(`)
	var unclassified, missingHook []string
	seen := map[string]bool{}
	for _, dir := range []string{".", "../handler"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := dir + "/" + name
			if dir == "." {
				path = "./" + name
			}
			raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
			if err != nil {
				t.Fatal(err)
			}
			src := stripGoLineComments(string(raw))
			if !callRe.MatchString(src) {
				continue
			}
			seen[path] = true
			hook, isText := textChokePoints[path]
			_, declared := nonText[path]
			switch {
			case isText && hook != "":
				if !strings.Contains(src, hook) {
					missingHook = append(missingHook, path+" must call "+hook)
				}
			case declared:
			default:
				unclassified = append(unclassified, path)
			}
		}
	}
	for path, hook := range textChokePoints {
		if hook != "" && !seen[path] {
			missingHook = append(missingHook, path+" no longer submits usage; update the owner list and doc")
		}
	}
	sort.Strings(unclassified)
	sort.Strings(missingHook)
	if len(unclassified) > 0 || len(missingHook) > 0 {
		t.Fatalf("usage submission sites must route through the zero-usage estimate owner (usage_estimate_fallback_tk.go) or be declared non-text:\nunclassified:\n  %s\nmissing hook:\n  %s",
			strings.Join(unclassified, "\n  "), strings.Join(missingHook, "\n  "))
	}

	// Each text submit closure calls the hook before RecordUsage, in the
	// same function, so the estimate reaches the billed result.
	for path, hook := range textChokePoints {
		if hook == "" {
			continue
		}
		raw, _ := os.ReadFile(filepath.Clean(path))
		src := stripGoLineComments(string(raw))
		if strings.Index(src, hook) > lastIndexRe(src, callRe) {
			t.Fatalf("%s: %s must run before the last RecordUsage call", path, hook)
		}
	}

	// Private estimators must not come back next to the owner.
	for _, dir := range []string{".", "../handler"} {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "usage_estimate_") {
				continue
			}
			raw, _ := os.ReadFile(filepath.Join(dir, name))
			src := stripGoLineComments(string(raw))
			for _, banned := range []string{"tokenestimate.Count(", `"usage_estimated"`, "UsageEstimatedBillingTier ="} {
				if strings.Contains(src, banned) {
					t.Errorf("%s/%s: %q belongs to the usage-estimate owner", dir, name, banned)
				}
			}
		}
	}
}

func stripGoLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

func lastIndexRe(src string, re *regexp.Regexp) int {
	locs := re.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return -1
	}
	return locs[len(locs)-1][0]
}
