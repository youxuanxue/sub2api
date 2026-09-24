//go:build unit

package handler

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Anthropic-group /v1/messages selects MessagesIdentity for Cursor. That path
// must use native AgentService/Run (HTTP/2), not GatewayService.Forward's JSON
// POST to agentn.../v1/messages (HTTP/1.x mis-parse of the h2 preface).
func TestAnthropicGroupCursorMessagesIdentityUsesNativeAgentRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "composer-2.5"
	account := &service.Account{
		ID: 150, Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, ChannelType: 14,
		Status: service.StatusActive, Schedulable: true,
		Extra: map[string]any{service.CursorSourceExtraKey: "cursor"},
		Credentials: map[string]any{
			"base_url": cursor.AgentBaseURL, "api_key": "cursor-test-key",
			"model_mapping":                  map[string]any{model: model},
			service.CursorWireModelsKey:      map[string]any{model: model},
			service.CursorModelParametersKey: map[string]any{model: []cursor.Parameter{{ID: "fast", Value: "false"}}},
		},
	}
	attachHandlerTestProtocolCapability(t, account, protocolrouter.ProtocolMessages)

	body := []byte(`{"model":"composer-2.5","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
	request, err := newCanonicalProtocolRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, body)
	require.NoError(t, err)
	snapshot, err := service.ProtocolAccountSnapshot(account, model)
	require.NoError(t, err)
	router := service.NewProtocolRouter()
	plan, err := router.Plan(request, snapshot)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.AdapterMessagesIdentity, plan.AdapterID())

	var response bytes.Buffer
	raw, err := proto.Marshal(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
		TextDelta: &pb.TextDeltaUpdate{Text: "CURSOR_OK"},
	}})
	require.NoError(t, err)
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	response.Write(header[:])
	response.Write(raw)
	raw, err = proto.Marshal(&pb.AgentServerMessage{InteractionUpdate: &pb.InteractionUpdate{
		TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(3), OutputTokens: proto.Int64(2), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)},
	}})
	require.NoError(t, err)
	binary.BigEndian.PutUint32(header[1:], uint32(len(raw)))
	response.Write(header[:])
	response.Write(raw)

	upstream := &cursorAnthropicGroupUpstream{payload: response.Bytes()}
	repo := &plannedOpenAIShapeAccountRepo{account: account}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	h := &GatewayHandler{
		protocolRouter:       router,
		gatewayService:       service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
		openAIGatewayService: service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil),
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	ctx := service.WithProtocolRouting(c.Request.Context(), router, request)
	c.Request = c.Request.WithContext(ctx)
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef(body), service.PlatformAnthropic)
	require.NoError(t, err)

	result, err := h.executeMessagesSelectedProtocol(c, ctx, &service.AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, &service.APIKey{GroupID: int64Ptr(1)}, parsed, service.ChannelMappingResult{}, false)
	require.NoError(t, err, recorder.Body.String())
	require.NotNil(t, result)
	require.Equal(t, cursor.AgentBaseURL+"/agent.v1.AgentService/Run", upstream.url)
	require.Equal(t, service.HTTPUpstreamProfileCursor, upstream.profile)
	require.NotContains(t, upstream.url, "/v1/messages")
	require.Contains(t, recorder.Body.String(), "CURSOR_OK")
}

type cursorAnthropicGroupUpstream struct {
	service.HTTPUpstream
	payload []byte
	url     string
	profile service.HTTPUpstreamProfile
}

func (u *cursorAnthropicGroupUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.url = req.URL.String()
	u.profile = service.HTTPUpstreamProfileFromContext(req.Context())
	if strings.HasSuffix(req.URL.Path, "/v1/messages") {
		return nil, io.EOF // would surface as the prod HTTP/1.x malformed class if routing regresses
	}
	_, _ = io.Copy(io.Discard, req.Body)
	return &http.Response{StatusCode: http.StatusOK, ProtoMajor: 2, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(u.payload))}, nil
}

func int64Ptr(v int64) *int64 { return &v }
