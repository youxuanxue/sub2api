package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS048_SupplierSourceCreateRejectsMalformedPurchaseRatioAtJSONBoundary(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/supplier-sources", bytes.NewBufferString(`{
		"supplier_name":"佳杰",
		"channel_name":"stbl-5",
		"endpoint":"https://supplier.example/v1",
		"credential":"secret",
		"models":[{"client_model_id":"model","upstream_model_id":"model","purchase_ratio":"43折"}]
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&SupplierSourceHandler{}).Create(ctx)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestUS048_SupplierSourceResponsesExposeOnlyManagementFacts(t *testing.T) {
	ratio := 0.5
	result := supplierSourceToResponse(&service.SupplierSource{
		ID: 7, SupplierName: "佳杰", ChannelName: "stbl-5",
		Endpoint: "https://token.vstecscloud.com/v1", EncryptedCredential: "ciphertext",
		CredentialFingerprint: "hmac:secret", BasePriority: 100, Notes: "lowest ratio only",
		Models: []service.SupplierSourceModel{{
			ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio,
		}},
	})

	payload, err := json.Marshal(result)
	require.NoError(t, err)
	body := strings.ToLower(string(payload))
	require.Contains(t, body, `"base_priority":100`)
	require.Contains(t, body, `"client_model_id":"deepseek-v4-pro"`)
	require.NotContains(t, body, "encrypted_credential")
	require.NotContains(t, body, "credential_fingerprint")
	require.NotContains(t, body, "ciphertext")
	require.NotContains(t, body, "state")
	require.NotContains(t, body, "revision")
	require.NotContains(t, body, "probe_status")
	require.NotContains(t, body, "group_ids")
}

func TestUS048_SupplierSourceRequestAllowsEmptyModelsAndCarriesBasePriority(t *testing.T) {
	basePriority := 120
	req := supplierSourceRequest{
		SupplierName: "FMGo", ChannelName: "seedance", Endpoint: "https://fmgo.example/v1",
		Credential: "secret", BasePriority: &basePriority, Models: []supplierSourceModelRequest{},
	}

	input := req.toInput()
	require.Equal(t, &basePriority, input.BasePriority)
	require.NotNil(t, input.Models)
	require.Empty(t, input.Models)
}

func TestUS048_SupplierSourceErrorsUseStableHTTPClasses(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{service.ErrSupplierSourceInvalidInput, http.StatusBadRequest},
		{service.ErrSupplierSourceNotFound, http.StatusNotFound},
		{service.ErrSupplierSourceIdentityConflict, http.StatusConflict},
		{service.ErrSupplierSourceProbeFailed, http.StatusUnprocessableEntity},
		{service.ErrSupplierProjectionProtocolNotReady, http.StatusUnprocessableEntity},
		{&service.UpstreamModelSyncError{
			Kind: service.UpstreamModelSyncErrorConfiguration, Message: "No supplier API key is available",
		}, http.StatusBadRequest},
		{&service.UpstreamModelSyncError{
			Kind: service.UpstreamModelSyncErrorUpstream, Message: "Supplier model list request failed with HTTP 401",
		}, http.StatusBadGateway},
	}
	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		writeSupplierSourceError(ctx, tt.err)
		require.Equal(t, tt.want, recorder.Code, tt.err.Error())
	}
}

func TestUS048_DiscoverUpstreamListFailureReturnsSafeMessageAndFailedStep(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	result := &service.SupplierModelsDiscoverResult{
		SourceID:           3,
		UpstreamModels:     []service.SupplierUpstreamModelEntry{},
		NormalizedModels:   []service.SupplierSourceModel{},
		NormalizedChanges:  []service.SupplierModelNormalizeChange{},
		SuggestedAppends:   []service.SupplierSourceModel{},
		RejectedCandidates: []service.SupplierModelDiscoverRejection{},
		ConfiguredIssues:   []service.SupplierModelDiscoverIssue{},
		ProbeResults:       []service.SupplierProbeResult{},
		FailedStep:         "list_upstream_models",
	}
	writeSupplierSourceDiscoverError(ctx, result, &service.UpstreamModelSyncError{
		Kind:    service.UpstreamModelSyncErrorUpstream,
		Message: "Supplier model list request failed with HTTP 401",
		Err:     errors.New("supplier model list returned HTTP 401"),
	})

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "Supplier model list request failed with HTTP 401", payload["message"])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "list_upstream_models", data["failed_step"])
}

func TestUS048_SupplierSourceProbeFailureReturns422WithCompleteResult(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	result := &service.SupplierSourceSyncResult{
		SourceID: 7,
		ProbeResults: []service.SupplierProbeResult{
			{ClientModelID: "model-ok", UpstreamModelID: "model-ok", Status: service.SupplierProbeStatusPassed},
			{
				ClientModelID: "doubao-seedance-2-0-260128", UpstreamModelID: "feimiao-seedance-2-0-260128",
				Status: service.SupplierProbeStatusProtocolUnsupported, Detail: "supplier protocol unsupported",
			},
		},
		Changes: []service.SupplierSourceAccountChange{}, FailedStep: "probe",
	}

	writeSupplierSourceSyncError(ctx, result, service.ErrSupplierSourceProbeFailed)

	require.Equal(t, http.StatusUnprocessableEntity, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, float64(http.StatusUnprocessableEntity), payload["code"])
	require.Equal(t, "SUPPLIER_SOURCE_PROBE_FAILED", payload["reason"])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	probeResults, ok := data["probe_results"].([]any)
	require.True(t, ok)
	require.Len(t, probeResults, 2)
	require.Equal(t, "probe", data["failed_step"])
	require.NotContains(t, recorder.Body.String(), "credential")
}

func TestUS048_SupplierSourceWriteFailureReturnsCompletedChanges(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	result := &service.SupplierSourceSyncResult{
		SourceID: 7,
		ProbeResults: []service.SupplierProbeResult{{
			ClientModelID: "model", UpstreamModelID: "model", Status: service.SupplierProbeStatusPassed,
		}},
		Changes: []service.SupplierSourceAccountChange{{
			AccountID: 101, DiscountBand: 3, Action: "created",
			AddedModels: []string{"model"}, RemovedModels: []string{},
			PriorityAfter: 103, SchedulableAfter: false,
		}},
		FailedStep: "add_band_3",
	}

	writeSupplierSourceSyncError(ctx, result, errors.New("injected account write failure"))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "supplier source operation failed", payload["message"])
	data, ok := payload["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "add_band_3", data["failed_step"])
	changes, ok := data["changes"].([]any)
	require.True(t, ok)
	require.Len(t, changes, 1)
	require.NotContains(t, recorder.Body.String(), "injected account write failure")
}
