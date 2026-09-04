package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestUS048_SupplierModelMatchKeyNormalizesCaseAndSpaces(t *testing.T) {
	require.Equal(t, "glm-5.3", supplierModelMatchKey("GLM 5.3"))
	require.Equal(t, "deepseek-v4-pro", supplierModelMatchKey("DeepSeek-V4-Pro"))
	require.Equal(t, "glm-5.1", supplierModelMatchKey("glm-5.1"))
}

func TestUS048_MatchSupplierUpstreamModelIDPrefersCanonicalID(t *testing.T) {
	upstream := []SupplierUpstreamModelEntry{
		{ID: "deepseek-v4-pro", Type: "chat"},
		{ID: "glm-5.3", Type: "chat"},
	}
	got, ok := matchSupplierUpstreamModelID("DeepSeek-V4-Pro", upstream)
	require.True(t, ok)
	require.Equal(t, "deepseek-v4-pro", got)
	got, ok = matchSupplierUpstreamModelID("GLM 5.3", upstream)
	require.True(t, ok)
	require.Equal(t, "glm-5.3", got)
	_, ok = matchSupplierUpstreamModelID("MiniMax-M2.7", upstream)
	require.False(t, ok)
}

func TestUS048_BuildSupplierModelsListURLUsesBaiduV2Path(t *testing.T) {
	transport, err := resolveSupplierManagedTransport("https://qianfan.baidubce.com", newapiconstant.ChannelTypeBaiduV2)
	require.NoError(t, err)
	require.Equal(t, newapiconstant.ChannelTypeBaiduV2, transport.ChannelType)
	require.Equal(t, newapiintegration.QianfanBaseURL+"/v2/models", buildSupplierModelsListURL(transport))

	openAI, err := resolveSupplierManagedTransport("https://token.vstecscloud.com/v1", newapiconstant.ChannelTypeOpenAI)
	require.NoError(t, err)
	require.Equal(t, "https://token.vstecscloud.com/v1/models", buildSupplierModelsListURL(openAI))
}

func TestUS048_BuildSupplierModelsListURLUsesAliCompatibleModePath(t *testing.T) {
	transport, err := resolveSupplierManagedTransport("https://dashscope.aliyuncs.com", newapiconstant.ChannelTypeAli)
	require.NoError(t, err)
	require.Equal(t, newapiconstant.ChannelTypeAli, transport.ChannelType)
	require.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1/models", buildSupplierModelsListURL(transport))
}

func TestUS048_ProbeUntilCompleteNormalizesAndSuggestsOnlyProbePassed(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 3, SupplierName: "baidu", SupplierLane: "default",
		ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "DeepSeek-V4-Pro", UpstreamModelID: "DeepSeek-V4-Pro", PurchaseRatio: &ratio},
			{ClientModelID: "MiniMax-M2.7", UpstreamModelID: "MiniMax-M2.7", PurchaseRatio: &ratio},
		},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "deepseek-v4-pro", Type: "chat"},
			{ID: "glm-5.1", Type: "chat"},
			{ID: "embedding-v1", Type: "embeddings"},
			{ID: "broken-chat", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{
			"glm-5.1":     SupplierProbeStatusPassed,
			"broken-chat": SupplierProbeStatusModelUnsupported,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.ProbeUntilComplete(context.Background(), 3, SupplierDiscoverOptions{})
	require.NoError(t, err)
	require.True(t, result.NeedsConfirmation)
	require.Len(t, result.NormalizedChanges, 1)
	require.Equal(t, "deepseek-v4-pro", result.NormalizedChanges[0].ToUpstreamModelID)
	require.Equal(t, "deepseek-v4-pro", result.NormalizedModels[0].UpstreamModelID)
	require.Equal(t, "MiniMax-M2.7", result.NormalizedModels[1].UpstreamModelID)
	require.Equal(t, []string{"MiniMax-M2.7"}, probeConfiguredIssueUpstreamIDs(result.ConfiguredIssues))
	require.Len(t, result.SuggestedAppends, 1)
	require.Equal(t, "glm-5.1", result.SuggestedAppends[0].UpstreamModelID)
	require.NotNil(t, result.SuggestedAppends[0].PurchaseRatio)
	require.Equal(t, 1.0, *result.SuggestedAppends[0].PurchaseRatio)

	rejected := map[string]string{}
	for _, item := range result.RejectedCandidates {
		rejected[item.UpstreamModelID] = item.Reason
	}
	require.Equal(t, "non_chat_type", rejected["embedding-v1"])
	require.Equal(t, "model_unsupported", rejected["broken-chat"])
	require.NotContains(t, rejected, "glm-5.1")
}

func TestUS048_ValidateProbesConfiguredRowsWithoutWritingAccounts(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 11, SupplierName: "baidu", SupplierLane: "default",
		ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com",
		EncryptedCredential: "enc:secret", BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
		},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{{ID: "deepseek-v4-pro", Type: "chat"}},
		probeStatus: map[string]SupplierProbeStatus{
			"deepseek-v4-pro": SupplierProbeStatusPassed,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.Validate(context.Background(), 11)
	require.NoError(t, err)
	require.NotEmpty(t, result.ProbeResults)
	require.Equal(t, "deepseek-v4-pro", result.ProbeResults[0].UpstreamModelID)
	require.Equal(t, SupplierProbeStatusPassed, result.ProbeResults[0].Status)
}

func TestUS048_ProbeUntilCompleteSuggestionsAloneDoNotBlockProjection(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 5, SupplierName: "baidu", SupplierLane: "default",
		ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
		},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "deepseek-v4-pro", Type: "chat"},
			{ID: "glm-5.1", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{"glm-5.1": SupplierProbeStatusPassed},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	result, err := svc.ProbeUntilComplete(context.Background(), 5, SupplierDiscoverOptions{})
	require.NoError(t, err)
	require.True(t, result.NeedsConfirmation)
	require.Empty(t, result.NormalizedChanges)
	require.Len(t, result.SuggestedAppends, 1)
	require.Equal(t, "glm-5.1", result.SuggestedAppends[0].UpstreamModelID)
}

func TestUS048_ProbeUntilCompletePreservesIntentionalClientUpstreamRemap(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 6, SupplierName: "FMGo", SupplierLane: "seedance",
		ChannelType: 1, Endpoint: "https://token.vstecscloud.com/v1", EncryptedCredential: "enc:secret",
		BasePriority: 100,
		Models: []SupplierSourceModel{{
			ClientModelID:   "doubao-seedance-2-0-260128",
			UpstreamModelID: "Feimiao-Seedance-2-0-260128",
			PurchaseRatio:   &ratio,
		}},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "feimiao-seedance-2-0-260128", Type: "chat"},
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	result, err := svc.ProbeUntilComplete(context.Background(), 6, SupplierDiscoverOptions{})
	require.NoError(t, err)
	require.True(t, result.NeedsConfirmation)
	require.Len(t, result.NormalizedModels, 1)
	require.Equal(t, "doubao-seedance-2-0-260128", result.NormalizedModels[0].ClientModelID)
	require.Equal(t, "feimiao-seedance-2-0-260128", result.NormalizedModels[0].UpstreamModelID)
	require.Equal(t, "doubao-seedance-2-0-260128", result.NormalizedChanges[0].ToClientModelID)
	require.Equal(t, "feimiao-seedance-2-0-260128", result.NormalizedChanges[0].ToUpstreamModelID)
}

func TestUS048_ProbeUntilCompleteAuthFailureDoesNotFailJob(t *testing.T) {
	source := &SupplierSource{
		ID: 4, SupplierName: "baidu", SupplierLane: "default",
		ChannelType: newapiconstant.ChannelTypeBaiduV2, Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100, Models: nil,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "glm-5.1", Type: "chat"},
			{ID: "deepseek-v4-pro", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{
			"glm-5.1":          SupplierProbeStatusAuthFailed,
			"deepseek-v4-pro":  SupplierProbeStatusPassed,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	result, err := svc.ProbeUntilComplete(context.Background(), 4, SupplierDiscoverOptions{})
	require.NoError(t, err)
	require.Empty(t, result.FailedStep)
	require.Equal(t, SupplierProbeJobCompleted, result.ProbeStatus)
	require.True(t, result.NeedsConfirmation)
	require.Len(t, result.SuggestedAppends, 1)
	require.Equal(t, "deepseek-v4-pro", result.SuggestedAppends[0].UpstreamModelID)
	rejected := map[string]string{}
	for _, item := range result.RejectedCandidates {
		rejected[item.UpstreamModelID] = item.Reason
	}
	require.Equal(t, "auth_failed", rejected["glm-5.1"])
}

func TestUS048_GetSupplierProbeJobMissingReturnsFailedSnapshot(t *testing.T) {
	svc := NewSupplierSourceService(
		&supplierSourceRepoFake{}, nil, &supplierProbeFake{},
		supplierSyncEncryptor{}, supplierSourceTestFingerprinter{},
	)
	result, err := svc.GetSupplierProbeJob(context.Background(), 3, "missing-job")
	require.NoError(t, err)
	require.Equal(t, "missing-job", result.JobID)
	require.Equal(t, "job_not_found", result.FailedStep)
	require.Equal(t, SupplierProbeJobFailed, result.ProbeStatus)
}

func TestUS048_StartSupplierProbeJobProbesAllCandidatesAsynchronously(t *testing.T) {
	source := &SupplierSource{
		ID: 8, SupplierName: "baidu", SupplierLane: "default",
		Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100, Models: nil,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	entries := make([]SupplierUpstreamModelEntry, 0, 12)
	probeStatus := make(map[string]SupplierProbeStatus, 12)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("model-%02d", i)
		entries = append(entries, SupplierUpstreamModelEntry{ID: id, Type: "chat"})
		probeStatus[id] = SupplierProbeStatusPassed
	}
	lister := &supplierProbeFake{entries: entries, probeStatus: probeStatus}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	started, err := svc.StartSupplierProbeJob(context.Background(), 8, SupplierDiscoverOptions{})
	require.NoError(t, err)
	require.Equal(t, SupplierProbeJobRunning, started.ProbeStatus)
	require.Equal(t, 12, started.ProbeTotal)
	require.NotEmpty(t, started.JobID)

	deadline := time.Now().Add(2 * time.Second)
	var result *SupplierSourceProbeResult
	for time.Now().Before(deadline) {
		result, err = svc.GetSupplierProbeJob(context.Background(), 8, started.JobID)
		require.NoError(t, err)
		if result.ProbeStatus == SupplierProbeJobCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, SupplierProbeJobCompleted, result.ProbeStatus)
	require.Equal(t, 12, result.ProbeDone)
	require.Equal(t, int64(12), lister.probeCalls.Load())
	require.Len(t, result.SuggestedAppends, 12)
}

func TestUS048_FMGoProbeSkipsNonVideoInventory(t *testing.T) {
	source := &SupplierSource{
		ID: 10, SupplierName: "feimiao", SupplierLane: "default",
		ChannelType:         newapiconstant.ChannelTypeDoubaoVideo,
		Endpoint:            "https://www.fmgo.top",
		EncryptedCredential: "enc:secret",
		BasePriority:        100,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "claude-opus-4-8"},
			{ID: "feimiao-v2-431-720p-15s"},
			{ID: "gpt-5.4"},
			{ID: "veo-3-fast-4k-8s"},
			{ID: "feimiao-v2.5-720p-15s"},
		},
		probeStatus: map[string]SupplierProbeStatus{
			"feimiao-v2-431-720p-15s": SupplierProbeStatusPassed,
			"feimiao-v2.5-720p-15s":   SupplierProbeStatusPassed,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.ProbeUntilComplete(context.Background(), 10, SupplierDiscoverOptions{})
	require.NoError(t, err)
	rejected := map[string]string{}
	for _, item := range result.RejectedCandidates {
		rejected[item.UpstreamModelID] = item.Reason
	}
	require.Equal(t, "non_video_inventory", rejected["claude-opus-4-8"])
	require.Equal(t, "non_video_inventory", rejected["gpt-5.4"])
	require.Equal(t, "non_video_inventory", rejected["veo-3-fast-4k-8s"])
	require.NotContains(t, rejected, "feimiao-v2-431-720p-15s")
	require.NotContains(t, rejected, "feimiao-v2.5-720p-15s")
	require.Equal(t, int64(2), lister.probeCalls.Load())
	require.Len(t, result.SuggestedAppends, 2)
}

func TestUS048_AnthropicChannelScopedDiscoverProbesOnlyClaudeFamily(t *testing.T) {
	source := &SupplierSource{
		ID: 12, SupplierName: "tokensea", SupplierLane: "anthropic",
		ChannelType: newapiconstant.ChannelTypeAnthropic, Endpoint: "https://agent.tokensea.ai/v1",
		EncryptedCredential: "enc:secret", BasePriority: 100, Models: nil,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "claude-sonnet-4-6", Type: "chat"},
			{ID: "us.anthropic.claude-opus-5", Type: "chat"},
			{ID: "gpt-5-mini", Type: "chat"},
			{ID: "deepseek-v4-pro", Type: "chat"},
			{ID: "doubao-seedance-2.0", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{
			"claude-sonnet-4-6":          SupplierProbeStatusPassed,
			"us.anthropic.claude-opus-5": SupplierProbeStatusPassed,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.ProbeUntilComplete(context.Background(), 12, SupplierDiscoverOptions{ChannelScoped: true})
	require.NoError(t, err)
	rejected := map[string]string{}
	for _, item := range result.RejectedCandidates {
		rejected[item.UpstreamModelID] = item.Reason
	}
	require.Equal(t, "non_channel_family", rejected["gpt-5-mini"])
	require.Equal(t, "non_channel_family", rejected["deepseek-v4-pro"])
	require.Equal(t, "non_channel_family", rejected["doubao-seedance-2.0"])
	require.Equal(t, int64(2), lister.probeCalls.Load())
	require.Len(t, result.SuggestedAppends, 2)
	require.Empty(t, result.FailedStep)
	require.True(t, result.NeedsConfirmation)
}

func TestUS048_OpenAIChannelScopedHasNoIDFamilyRule(t *testing.T) {
	source := &SupplierSource{
		ID: 13, SupplierName: "tokensea", SupplierLane: "openai",
		ChannelType: newapiconstant.ChannelTypeOpenAI, Endpoint: "https://agent.tokensea.ai/v1",
		EncryptedCredential: "enc:secret", BasePriority: 100, Models: nil,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "claude-fable-5.1", Type: "chat"},
			{ID: "gpt-5-mini", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{
			"claude-fable-5.1": SupplierProbeStatusPassed,
			"gpt-5-mini":       SupplierProbeStatusPassed,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.ProbeUntilComplete(context.Background(), 13, SupplierDiscoverOptions{ChannelScoped: true})
	require.NoError(t, err)
	require.Equal(t, int64(2), lister.probeCalls.Load())
	require.Len(t, result.SuggestedAppends, 2)
}

func TestUS048_AnthropicWithoutChannelScopedProbesAllChatCandidates(t *testing.T) {
	source := &SupplierSource{
		ID: 14, SupplierName: "tokensea", SupplierLane: "anthropic",
		ChannelType: newapiconstant.ChannelTypeAnthropic, Endpoint: "https://agent.tokensea.ai/v1",
		EncryptedCredential: "enc:secret", BasePriority: 100, Models: nil,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "claude-sonnet-4-6", Type: "chat"},
			{ID: "gpt-5-mini", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{
			"claude-sonnet-4-6": SupplierProbeStatusPassed,
			"gpt-5-mini":        SupplierProbeStatusModelUnsupported,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})

	result, err := svc.ProbeUntilComplete(context.Background(), 14, SupplierDiscoverOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(2), lister.probeCalls.Load())
	require.Len(t, result.SuggestedAppends, 1)
	rejected := map[string]string{}
	for _, item := range result.RejectedCandidates {
		rejected[item.UpstreamModelID] = item.Reason
	}
	require.Equal(t, "model_unsupported", rejected["gpt-5-mini"])
}

func TestUS048_ExtractSupplierUpstreamModelEntriesKeepsType(t *testing.T) {
	entries, err := extractSupplierUpstreamModelEntries([]byte(`{
		"data": [
			{"id": "glm-5.1", "type": "chat"},
			{"id": "embedding-v1", "type": "embeddings"}
		]
	}`))
	require.NoError(t, err)
	require.Equal(t, []SupplierUpstreamModelEntry{
		{ID: "glm-5.1", Type: "chat"},
		{ID: "embedding-v1", Type: "embeddings"},
	}, entries)
}

type supplierProbeFake struct {
	entries     []SupplierUpstreamModelEntry
	probeStatus map[string]SupplierProbeStatus
	listErr     error
	probeCalls  atomic.Int64
}

func (f *supplierProbeFake) ListSupplierUpstreamModels(
	context.Context, string, int, string,
) ([]SupplierUpstreamModelEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]SupplierUpstreamModelEntry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

func (f *supplierProbeFake) ProbeSupplierModel(_ context.Context, input SupplierProbeInput) SupplierProbeResult {
	f.probeCalls.Add(1)
	status := SupplierProbeStatusFailed
	if f.probeStatus != nil {
		if got, ok := f.probeStatus[input.UpstreamModelID]; ok {
			status = got
		}
	}
	return SupplierProbeResult{
		ClientModelID: input.ClientModelID, UpstreamModelID: input.UpstreamModelID, Status: status,
		Detail: supplierProbeSafeDetail(status),
	}
}

func probeConfiguredIssueUpstreamIDs(issues []SupplierProbeConfiguredIssue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue.UpstreamModelID)
	}
	return out
}
