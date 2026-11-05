//go:build unit

package service

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestUnsupportedModelSSOT_NoEmitSiteBypassesOwner fails when production code
// mints the unsupported-model response outside the owner
// (gateway_service_tk_unsupported_model.go). Contract:
// docs/approved/unsupported-model-404-ssot.md §Owner.
func TestUnsupportedModelSSOT_NoEmitSiteBypassesOwner(t *testing.T) {
	const owner = "gateway_service_tk_unsupported_model.go"
	rules := []struct {
		name string
		re   *regexp.Regexp
	}{
		// Message + type are only composed by the owner; sites take the tuple
		// from TkUnsupportedModelSelectionResponse / TkWriteUnsupportedModel.
		{"TkUnsupportedModelMessage call", regexp.MustCompile(`\bTkUnsupportedModelMessage\(`)},
		{"TkUnsupportedModelErrType reference", regexp.MustCompile(`\bTkUnsupportedModelErrType\b`)},
		// Wire codes for this case are rendered only by the owner Body().
		{"hardcoded model_not_found wire code", regexp.MustCompile(`"code":\s*"model_not_found"`)},
		{"hardcoded unsupported_model wire code", regexp.MustCompile(`"code":\s*"unsupported_model"`)},
		// Legacy 400 shape for this case.
		{"400 + Unsupported model", regexp.MustCompile(`StatusBadRequest.*Unsupported model|Unsupported model.*StatusBadRequest`)},
	}
	var violations []string
	for _, dir := range []string{".", "../handler", "../server/middleware"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == owner {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, rule := range rules {
					if rule.re.MatchString(line) {
						violations = append(violations, filepath.Join(dir, name)+":"+itoaContract(i+1)+" "+rule.name)
					}
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("unsupported-model emit sites must go through %s:\n%s", owner, strings.Join(violations, "\n"))
	}
}

func itoaContract(n int) string {
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
