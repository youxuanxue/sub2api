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
	transport, err := resolveSupplierManagedTransport("https://qianfan.baidubce.com")
	require.NoError(t, err)
	require.Equal(t, newapiconstant.ChannelTypeBaiduV2, transport.ChannelType)
	require.Equal(t, newapiintegration.QianfanBaseURL+"/v2/models", buildSupplierModelsListURL(transport))

	openAI, err := resolveSupplierManagedTransport("https://token.vstecscloud.com/v1")
	require.NoError(t, err)
	require.Equal(t, "https://token.vstecscloud.com/v1/models", buildSupplierModelsListURL(openAI))
}

func TestUS048_DiscoverModelsNormalizesAndSuggestsOnlyProbePassed(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 3, SupplierName: "baidu", ChannelName: "default",
		Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "DeepSeek-V4-Pro", UpstreamModelID: "DeepSeek-V4-Pro", PurchaseRatio: &ratio},
			{ClientModelID: "MiniMax-M2.7", UpstreamModelID: "MiniMax-M2.7", PurchaseRatio: &ratio},
		},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierDiscoverProbeFake{
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

	result, err := svc.DiscoverModels(context.Background(), 3)
	require.NoError(t, err)
	require.True(t, result.NeedsConfirmation)
	require.Len(t, result.NormalizedChanges, 1)
	require.Equal(t, "deepseek-v4-pro", result.NormalizedChanges[0].ToUpstreamModelID)
	require.Equal(t, "deepseek-v4-pro", result.NormalizedModels[0].UpstreamModelID)
	require.Equal(t, "MiniMax-M2.7", result.NormalizedModels[1].UpstreamModelID)
	require.Equal(t, []string{"MiniMax-M2.7"}, discoverIssueUpstreamIDs(result.ConfiguredIssues))
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

func TestUS048_DiscoverModelsSuggestionsAloneDoNotBlockProjection(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 5, SupplierName: "baidu", ChannelName: "default",
		Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100,
		Models: []SupplierSourceModel{
			{ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio},
		},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierDiscoverProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "deepseek-v4-pro", Type: "chat"},
			{ID: "glm-5.1", Type: "chat"},
		},
		probeStatus: map[string]SupplierProbeStatus{"glm-5.1": SupplierProbeStatusPassed},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	result, err := svc.DiscoverModels(context.Background(), 5)
	require.NoError(t, err)
	require.False(t, result.NeedsConfirmation)
	require.Empty(t, result.NormalizedChanges)
	require.Len(t, result.SuggestedAppends, 1)
	require.Equal(t, "glm-5.1", result.SuggestedAppends[0].UpstreamModelID)
}

func TestUS048_DiscoverModelsPreservesIntentionalClientUpstreamRemap(t *testing.T) {
	ratio := 0.5
	source := &SupplierSource{
		ID: 6, SupplierName: "FMGo", ChannelName: "seedance",
		Endpoint: "https://token.vstecscloud.com/v1", EncryptedCredential: "enc:secret",
		BasePriority: 100,
		Models: []SupplierSourceModel{{
			ClientModelID:   "doubao-seedance-2-0-260128",
			UpstreamModelID: "Feimiao-Seedance-2-0-260128",
			PurchaseRatio:   &ratio,
		}},
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierDiscoverProbeFake{
		entries: []SupplierUpstreamModelEntry{
			{ID: "feimiao-seedance-2-0-260128", Type: "chat"},
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	result, err := svc.DiscoverModels(context.Background(), 6)
	require.NoError(t, err)
	require.True(t, result.NeedsConfirmation)
	require.Len(t, result.NormalizedModels, 1)
	require.Equal(t, "doubao-seedance-2-0-260128", result.NormalizedModels[0].ClientModelID)
	require.Equal(t, "feimiao-seedance-2-0-260128", result.NormalizedModels[0].UpstreamModelID)
	require.Equal(t, "doubao-seedance-2-0-260128", result.NormalizedChanges[0].ToClientModelID)
	require.Equal(t, "feimiao-seedance-2-0-260128", result.NormalizedChanges[0].ToUpstreamModelID)
}

func TestUS048_DiscoverModelsAuthFailureStopsWithoutSuggesting(t *testing.T) {
	source := &SupplierSource{
		ID: 4, SupplierName: "baidu", ChannelName: "default",
		Endpoint: "https://qianfan.baidubce.com", EncryptedCredential: "enc:secret",
		BasePriority: 100, Models: nil,
	}
	repo := &supplierSourceRepoFake{stored: cloneSupplierSourceForTest(source)}
	lister := &supplierDiscoverProbeFake{
		entries: []SupplierUpstreamModelEntry{{ID: "glm-5.1", Type: "chat"}},
		probeStatus: map[string]SupplierProbeStatus{
			"glm-5.1": SupplierProbeStatusAuthFailed,
		},
	}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	result, err := svc.DiscoverModels(context.Background(), 4)
	require.ErrorIs(t, err, ErrSupplierSourceProbeFailed)
	require.Equal(t, "probe_candidate", result.FailedStep)
	require.Equal(t, SupplierDiscoverProbeFailed, result.ProbeStatus)
	require.Empty(t, result.SuggestedAppends)
}

func TestUS048_StartDiscoverModelsProbesAllCandidatesAsynchronously(t *testing.T) {
	source := &SupplierSource{
		ID: 8, SupplierName: "baidu", ChannelName: "default",
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
	lister := &supplierDiscoverProbeFake{entries: entries, probeStatus: probeStatus}
	svc := NewSupplierSourceService(repo, nil, lister, supplierSyncEncryptor{}, supplierSourceTestFingerprinter{})
	started, err := svc.StartDiscoverModels(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, SupplierDiscoverProbeRunning, started.ProbeStatus)
	require.Equal(t, 12, started.ProbeTotal)
	require.NotEmpty(t, started.JobID)

	deadline := time.Now().Add(2 * time.Second)
	var result *SupplierModelsDiscoverResult
	for time.Now().Before(deadline) {
		result, err = svc.GetDiscoverModelsJob(context.Background(), 8, started.JobID)
		require.NoError(t, err)
		if result.ProbeStatus == SupplierDiscoverProbeCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, SupplierDiscoverProbeCompleted, result.ProbeStatus)
	require.Equal(t, 12, result.ProbeDone)
	require.Equal(t, int64(12), lister.probeCalls.Load())
	require.Len(t, result.SuggestedAppends, 12)
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

type supplierDiscoverProbeFake struct {
	entries     []SupplierUpstreamModelEntry
	probeStatus map[string]SupplierProbeStatus
	listErr     error
	probeCalls  atomic.Int64
}

func (f *supplierDiscoverProbeFake) ListSupplierUpstreamModels(
	context.Context, string, string,
) ([]SupplierUpstreamModelEntry, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]SupplierUpstreamModelEntry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

func (f *supplierDiscoverProbeFake) ProbeSupplierModel(_ context.Context, input SupplierProbeInput) SupplierProbeResult {
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

func discoverIssueUpstreamIDs(issues []SupplierModelDiscoverIssue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue.UpstreamModelID)
	}
	return out
}
