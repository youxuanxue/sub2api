//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityGatewayRejectsUnsafeSchemasBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const recursive = `{"$ref":"#/$defs/Node","$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}}}`
	deep := map[string]any{"type": "string"}
	for i := 0; i < 140; i++ {
		deep = map[string]any{"type": "array", "items": deep}
	}
	deepJSON, err := json.Marshal(deep)
	require.NoError(t, err)
	for name, schema := range map[string]string{"recursive": recursive, "over budget": string(deepJSON)} {
		t.Run(name, func(t *testing.T) {
			cases := map[string]string{
				"claude tools":    `{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"walk","input_schema":` + schema + `}]}`,
				"native response": `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":` + schema + `}}`,
			}
			for _, key := range []string{"parameters", "parametersJsonSchema", "parameters_json_schema", "response", "responseJsonSchema", "response_json_schema"} {
				cases["native "+key] = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":[{"functionDeclarations":[{"name":"walk","` + key + `":` + schema + `}]}]}`
			}
			for ingress, raw := range cases {
				t.Run(ingress, func(t *testing.T) {
					upstream := &queuedHTTPUpstreamStub{}
					svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
					account := newAntigravityCompatAccount(AccountTypeOAuth)
					body := []byte(raw)
					c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/messages", body)
					var result *ForwardResult
					var err error
					if ingress == "claude tools" {
						result, err = svc.Forward(context.Background(), c, account, body, false)
					} else {
						result, err = svc.ForwardGemini(context.Background(), c, account, "gemini-3.1-pro-high", "generateContent", false, body, false)
					}
					require.Error(t, err)
					require.Nil(t, result)
					require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), "unsupported JSON schema")
					require.Empty(t, upstream.requestBodies, "invalid schemas must never call model upstream or produce billable usage")
				})
			}
		})
	}
}
