package logger

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestRedactCore_RedactsDefaultSensitiveFields(t *testing.T) {
	origStdout := os.Stdout
	origStderr := os.Stderr
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	_, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stdout = stdoutW
	os.Stderr = stderrW
	t.Cleanup(func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrW.Close()
	})

	if err := Init(InitOptions{
		Level:       "info",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Redact: RedactOptions{
			Enabled: true,
		},
		Output: OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: SamplingOptions{Enabled: false},
	}); err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	L().Info("redact-check",
		zap.String("token", "sk-fake-1234"),
		zap.String("email", "user@example.com"),
		zap.String("api_key", "abc123"),
	)

	os.Stdout = origStdout
	os.Stderr = origStderr
	_ = stdoutW.Close()

	logBytes, _ := io.ReadAll(stdoutR)
	var payload map[string]any
	if err := json.Unmarshal(logBytes, &payload); err != nil {
		t.Fatalf("parse log json failed: %v, raw=%s", err, string(logBytes))
	}

	for _, key := range []string{"token", "email", "api_key"} {
		if got, _ := payload[key].(string); got != "[REDACTED]" {
			t.Fatalf("%s = %q, want [REDACTED]", key, got)
		}
	}
}
