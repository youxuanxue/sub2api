package logredact

import (
	"strings"
	"testing"
)

func TestCredentialAliasesAcrossRecognizedFormats(t *testing.T) {
	for _, key := range []string{"api_v3_key", "apiV3Key", "api-v3-key", "appSecret", "app_secret", "app-secret", "accessKeySecret", "AccessKeyId", "secretAccessKey", "clientSecret", "xApiKey"} {
		t.Run(key, func(t *testing.T) {
			const secret = "canary-credential-431"
			if got := RedactMap(map[string]any{key: secret, "model": "normal"}); got[key] != "***" || got["model"] != "normal" {
				t.Fatalf("unexpected structured result: %#v", got)
			}
			for _, input := range []string{key + "=" + secret, key + ": " + secret, `{"` + key + `":"` + secret + `"}`, "data: {\"" + key + "\":\"" + secret + "\"}\n\n"} {
				if got := RedactText(input); strings.Contains(got, secret) {
					t.Fatalf("text leaked for %q: %s", input, got)
				}
				if strings.HasPrefix(input, "data:") && strings.Contains(RedactSSE(input), secret) {
					t.Fatal("SSE leaked credential")
				}
			}
		})
	}
	if got := RedactMap(map[string]any{"maxOutputTokens": 50, "normalText": "ordinary"}); got["maxOutputTokens"] != 50 || got["normalText"] != "ordinary" {
		t.Fatalf("noncredentials changed: %#v", got)
	}
}
