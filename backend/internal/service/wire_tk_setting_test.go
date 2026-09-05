//go:build unit

package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkWireSettingServiceExtras_NilSafe(t *testing.T) {
	require.NotPanics(t, func() {
		tkWireSettingServiceExtras(nil, nil)
		tkWireClaudeCodeResolvers(nil)
	})
}

// TestProvideSettingService_TKHookOrderInSource pins the relative order
// companion extraction must preserve: pubsub extras before migrations, Claude
// resolvers after antigravity wiring.
func TestProvideSettingService_TKHookOrderInSource(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "wire.go"))
	require.NoError(t, err)
	body := string(src)

	fnStart := strings.Index(body, "func ProvideSettingService(")
	require.Greater(t, fnStart, 0)
	fnEnd := strings.Index(body[fnStart:], "\nfunc ")
	require.Greater(t, fnEnd, 0)
	fn := body[fnStart : fnStart+fnEnd]

	pubsubIdx := strings.Index(fn, "tkWireSettingServiceExtras(")
	migrateIdx := strings.Index(fn, "MigrateOpenAIAllowClaudeCodeCodexPluginSetting(")
	antigravityIdx := strings.Index(fn, "antigravity.SetUserAgentVersionResolver(")
	claudeIdx := strings.Index(fn, "tkWireClaudeCodeResolvers(")

	require.Greater(t, pubsubIdx, 0, "ProvideSettingService must call tkWireSettingServiceExtras")
	require.Greater(t, migrateIdx, 0, "ProvideSettingService must run migrations")
	require.Greater(t, antigravityIdx, 0, "ProvideSettingService must wire antigravity resolver")
	require.Greater(t, claudeIdx, 0, "ProvideSettingService must call tkWireClaudeCodeResolvers")

	require.Less(t, pubsubIdx, migrateIdx, "pubsub extras must run before migrations")
	require.Less(t, antigravityIdx, claudeIdx, "Claude resolvers must install after antigravity")
}
