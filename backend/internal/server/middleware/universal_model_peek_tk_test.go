//go:build unit

package middleware

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUniversalJSONModelReadsLeadingField(t *testing.T) {
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"` + strings.Repeat("a", 8192) + `"}]}`)
	require.Equal(t, "kimi-k3", universalJSONModel(body))
	require.Equal(t, "kimi-k3", universalJSONModel([]byte(`{"Model":"kimi-k3"}`)))
	require.Equal(t, "first", universalJSONModel([]byte(`{"model":"first","model":"second"}`)))
	require.Equal(t, "kimi-k3", universalJSONModel([]byte(`{"model":"","Model":"kimi-k3"}`)))
	require.Equal(t, "", universalJSONModel(nil))
}
