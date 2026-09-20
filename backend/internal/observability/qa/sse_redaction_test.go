package qa

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestBuildBlobRedactsSSEBodyAndChunks(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "thinking opt-in"}[preserve], func(t *testing.T) {
			svc := &Service{bodyMaxBytes: 1 << 20, optInBodyMaxBytes: 1 << 20}
			input := `data: {"to\u006ben":"hidden","text":"\u0042earer hidden","custom_password":"hidden"}` + "\n\n"
			expected := `data: {"custom_password":"***","text":"Bearer ***","token":"***"}` + "\n\n"
			blob, _, _, _, err := svc.buildBlob(CaptureInput{
				Platform: "anthropic", DialogSynth: preserve,
				RequestBody: []byte(input), UpstreamRequestBody: []byte(input), ResponseBody: []byte(input),
				StreamChunks: []RawSSEChunk{{Bytes: []byte(input), RecvAtMs: 17}},
			})
			require.NoError(t, err)
			dec, err := zstd.NewReader(nil)
			require.NoError(t, err)
			defer dec.Close()
			raw, err := dec.DecodeAll(blob, nil)
			require.NoError(t, err)
			var payload struct {
				Request struct {
					Body         string `json:"body"`
					UpstreamBody string `json:"upstream_body"`
				} `json:"request"`
				Response struct {
					Body string `json:"body"`
				} `json:"response"`
				Stream struct {
					Chunks []struct {
						Raw  string `json:"raw_b64"`
						Time int    `json:"t"`
					} `json:"chunks"`
				} `json:"stream"`
			}
			require.NoError(t, json.Unmarshal(raw, &payload))
			require.Equal(t, strings.TrimSpace(expected), payload.Request.Body)
			require.Equal(t, strings.TrimSpace(expected), payload.Request.UpstreamBody)
			require.Equal(t, strings.TrimSpace(expected), payload.Response.Body)
			require.Len(t, payload.Stream.Chunks, 1)
			chunk, err := base64.StdEncoding.DecodeString(payload.Stream.Chunks[0].Raw)
			require.NoError(t, err)
			require.Equal(t, expected, string(chunk))
			require.Equal(t, 17, payload.Stream.Chunks[0].Time)
		})
	}
}
