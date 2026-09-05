//go:build unit

package service

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestForwardCountTokens_EstimateBeforeUAGateOrderPin pins the control-flow
// order that companion extraction must preserve: shouldEstimateCountTokensLocally
// short-circuits BEFORE the canonical-OAuth UA gate. Moving the gate into the
// pre-estimate companion would change estimate-platform behavior (P0 ProbeEnabled
// order-drift class).
func TestForwardCountTokens_EstimateBeforeUAGateOrderPin(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	srcPath := filepath.Join(filepath.Dir(thisFile), "gateway_count_tokens.go")
	f, err := os.Open(srcPath)
	require.NoError(t, err)
	defer f.Close()

	var estimateLine, uaGateLine int
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if strings.Contains(text, "shouldEstimateCountTokensLocally(account)") {
			estimateLine = line
		}
		if strings.Contains(text, "checkCanonicalIngressUAStrict(") {
			uaGateLine = line
		}
	}
	require.NoError(t, sc.Err())
	require.NotZero(t, estimateLine, "estimate short-circuit must remain in gateway_count_tokens.go")
	require.NotZero(t, uaGateLine, "UA gate must remain in gateway_count_tokens.go after estimate")
	require.Less(t, estimateLine, uaGateLine,
		"shouldEstimateCountTokensLocally must precede checkCanonicalIngressUAStrict")
}

// TestTkPrepareCountTokensAnthropicBody_DoesNotContainUAGate pins that the
// companion prepare helper does not swallow the UA gate (which must stay after
// estimate in ForwardCountTokens).
func TestTkPrepareCountTokensAnthropicBody_DoesNotContainUAGate(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	srcPath := filepath.Join(filepath.Dir(thisFile), "gateway_count_tokens_tk.go")
	body, err := os.ReadFile(srcPath)
	require.NoError(t, err)
	require.NotContains(t, string(body), "checkCanonicalIngressUAStrict",
		"UA gate must not live in the pre-estimate companion")
	require.NotContains(t, string(body), "IsAnthropicCanonicalIngressStrictEnabled",
		"UA gate setting check must not live in the pre-estimate companion")
}
