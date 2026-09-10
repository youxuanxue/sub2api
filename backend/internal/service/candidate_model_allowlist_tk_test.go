//go:build unit

package service

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCandidateModelAllowlistPrunesOriginAndMatchesDiscovery(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 1, false)}
	groups[0].ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"other-model"}}
	accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 10, 20)}
	r, _, key := globalCandidateFixture(groups, accounts)
	_, state := prepareGlobalCandidate(t, r, key)
	require.Equal(t, int64(2), state.current.account.ID)
	svc, discoveryKey := candidateDiscoveryFixture(groups, accounts)
	models, candidates, err := svc.DiscoverCandidates(context.Background(), discoveryKey, UniversalProtocolOpenAI)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Len(t, candidates, 1)
	require.Equal(t, int64(2), candidates[0].ID)
	key.RoutingMode, key.GroupID, key.Group = RoutingModeDirect, &groups[0].ID, &groups[0]
	_, _, err = r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`), "", "")
	require.Error(t, err)
}

func TestCandidateModelAllowlistChecksAllClientFields(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	group.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}
	for _, body := range []string{
		`{"model":"gpt-5.4","model":"denied"}`,
		`{"model":"gpt-5.4","Model":"denied"}`,
	} {
		r, _, key := globalCandidateFixture([]Group{group}, []Account{globalCandidateAccount(1, 1, 10)})
		_, _, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", []byte(body), "", "")
		require.Error(t, err, body)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("model", "gpt-5.4"))
	require.NoError(t, w.WriteField("model", "denied"))
	require.NoError(t, w.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	r, _, key := globalCandidateFixture([]Group{group}, []Account{globalCandidateAccount(1, 1, 10)})
	_, err := r.PrepareCandidateIngress(c, key, ShapeOpenAIChat, c.Request.URL.Path, "gpt-5.4", body.Bytes(), "")
	require.Error(t, err)
}

func TestCandidateModelAllowlistRevalidatesWebSocketAndKeepsDisplayOnlyConfig(t *testing.T) {
	group := grp(10, PlatformOpenAI, 1, false)
	group.ModelsListConfig.Enabled = true
	group.ModelsListConfig.Models = []string{"other-model"}
	account := globalCandidateAccount(1, 1, 10)
	account.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
	r, _, key := globalCandidateFixture([]Group{group}, []Account{account})
	r.candidateOpenAI.cfg = newSchedulerTestOpenAIWSV2Config()
	body := []byte(`{"type":"response.create","model":"gpt-5.4"}`)
	ctx, state, err := r.PrepareCandidateWebSocket(context.Background(), key, "/v1/responses", "gpt-5.4", body, "", "")
	require.NoError(t, err, "legacy display preferences must not deny execution")
	r.lister.(*stubSpanLister).groups[0].ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"other-model"}}
	require.Error(t, state.RevalidateTurn(ctx, 1, "gpt-5.4", body))
}
