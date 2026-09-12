package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQAReplayRequiresExplicitEnableAndPublicKey(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("QA_CAPTURE_REPLAY_ENABLED", "false")
	t.Setenv("QA_CAPTURE_REPLAY_PUBLIC_KEY_FILE", "")
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.QACapture.Replay.Enabled)
	t.Setenv("QA_CAPTURE_ENABLED", "true")
	t.Setenv("QA_CAPTURE_REPLAY_ENABLED", "true")
	_, err = Load()
	require.ErrorContains(t, err, "qa_capture.replay")
	t.Setenv("QA_CAPTURE_REPLAY_PUBLIC_KEY_FILE", "/app/data/replay-public.pem")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.QACapture.Replay.Enabled)
	require.Equal(t, "/app/data/replay-public.pem", cfg.QACapture.Replay.PublicKeyFile)
	t.Setenv("QA_CAPTURE_ENABLED", "false")
	_, err = Load()
	require.ErrorContains(t, err, "qa_capture.replay")
}
