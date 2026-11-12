package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIClientErrorEnvelopeDropsSafetyDiagnostics(t *testing.T) {
	for _, event := range []string{"response.failed", "error"} {
		payload := []byte(`{"type":"` + event + `","response":{"error":{"type":"server_error","code":"safety_error","message":"Failed","misalignment":{"detailed_explanation":"tenant-canary"},"nested":{"steer":{"message":"tenant-canary"}}}},"error":{"type":"server_error","code":"safety_error","message":"Failed","steer":{"message":"tenant-canary"}}}`)
		got, changed := sanitizeOpenAIResponseFailedEventForClient(payload, event, false)
		if !changed || strings.Contains(string(got), "tenant-canary") {
			t.Fatalf("unsafe envelope: %s", got)
		}
		for _, path := range []string{"response.error", "error"} {
			if gjson.GetBytes(got, path+".code").String() != "safety_error" || gjson.GetBytes(got, path+".type").String() != "server_error" {
				t.Fatalf("lost error identity: %s", got)
			}
		}
	}
}

func TestOpenAIClientErrorEnvelopePreservesRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers string
		want    string
	}{
		{"string", `{"retry-after":"12","authorization":"tenant-canary"}`, "12"},
		{"case insensitive", `{"Retry-After":"17"}`, "17"},
		{"number", `{"retry-after":20}`, "20"},
		{"object rejected", `{"retry-after":{"secret":"tenant-canary"}}`, ""},
		{"array rejected", `{"retry-after":["tenant-canary"]}`, ""},
		{"boolean rejected", `{"retry-after":true}`, ""},
		{"empty rejected", `{"retry-after":" "}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"error", "response.error"} {
				eventType := "error"
				payload := `{"type":"error","error":{"code":"upstream_error","message":"busy","headers":` + tc.headers + `}}`
				if path == "response.error" {
					eventType = "response.failed"
					payload = `{"type":"response.failed","response":{"error":{"code":"upstream_error","message":"busy","headers":` + tc.headers + `}}}`
				}
				got, _ := sanitizeOpenAIResponseFailedEventForClient([]byte(payload), eventType, false)
				require.Equal(t, tc.want, gjson.GetBytes(got, path+".headers.retry-after").String())
				require.NotContains(t, string(got), "tenant-canary")
				if tc.want == "" {
					require.False(t, gjson.GetBytes(got, path+".headers").Exists())
				} else {
					require.Len(t, gjson.GetBytes(got, path+".headers").Map(), 1)
				}
				for name, built := range map[string][]byte{
					"sse": func() []byte {
						frame := buildOpenAIResponseFailedSSE("resp_1", "", 1, []byte(payload), "")
						for _, line := range strings.Split(frame, "\n") {
							if strings.HasPrefix(line, "data: ") {
								return []byte(strings.TrimPrefix(line, "data: "))
							}
						}
						return nil
					}(),
					"ws": buildOpenAIWSHTTPBridgeFailedEvent("resp_1", "", 1, []byte(payload), ""),
				} {
					require.Equal(t, tc.want, gjson.GetBytes(built, "response.error.headers.retry-after").String(), name)
					require.NotContains(t, string(built), "tenant-canary", name)
				}
			}
		})
	}
}

func TestOpenAIClientErrorEnvelopeDropsUnknownNestedMetadata(t *testing.T) {
	payload := []byte(`{"type":"response.failed","message":"tenant-canary","debug":{"detailed_explanation":"tenant-canary"},"response":{"id":{"secret":"tenant-canary"},"model":["tenant-canary"],"unknown":{"secret":"tenant-canary"},"error":{"type":"server_error","code":"upstream_error","message":"tenant-canary","nested":[{"steer":{"message":"tenant-canary"}}]}}}`)
	got, _ := sanitizeOpenAIResponseFailedEventForClient(payload, "response.failed", false)
	if strings.Contains(string(got), "tenant-canary") {
		t.Fatalf("unsafe envelope: %s", got)
	}
	if gjson.GetBytes(got, "response.error.code").String() != "upstream_error" {
		t.Fatalf("lost error code: %s", got)
	}
}
