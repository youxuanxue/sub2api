package bridge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/vertex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type vertexEmbeddingPredictResponse struct {
	Predictions []vertexEmbeddingPrediction `json:"predictions"`
}

type vertexEmbeddingPrediction struct {
	Embeddings struct {
		Values []float64 `json:"values"`
	} `json:"embeddings"`
}

var vertexEmbeddingAcquireAccessToken = func(creds vertex.Credentials, proxy string) (string, error) {
	return vertex.AcquireAccessToken(creds, proxy)
}

// runVertexEmbeddingRelay implements Vertex AI text embedding via the :predict
// surface. Upstream new-api's vertex adaptor leaves ConvertEmbeddingRequest
// unimplemented; TokenKey owns this path for channel_type 41 service accounts.
func runVertexEmbeddingRelay(c *gin.Context, info *relaycommon.RelayInfo, request *dto.EmbeddingRequest) (*dto.Usage, *types.NewAPIError) {
	if c == nil || info == nil || request == nil {
		return nil, types.NewError(errors.New("vertex embedding relay missing context"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if info.ChannelMeta == nil {
		info.InitChannelMeta(c)
	}
	if strings.TrimSpace(info.ApiKey) == "" {
		return nil, types.NewError(errors.New("vertex service account credentials missing"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	var creds vertex.Credentials
	if err := common.Unmarshal([]byte(info.ApiKey), &creds); err != nil {
		return nil, types.NewError(fmt.Errorf("decode vertex service account json: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if strings.TrimSpace(creds.ProjectID) == "" || strings.TrimSpace(creds.ClientEmail) == "" || strings.TrimSpace(creds.PrivateKey) == "" {
		return nil, types.NewError(errors.New("vertex service account json missing project_id, client_email, or private_key"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	inputs := request.ParseInput()
	if len(inputs) == 0 {
		return nil, types.NewError(errors.New("input is empty"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	modelName := strings.TrimSpace(info.UpstreamModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(request.Model)
	}
	if modelName == "" {
		return nil, types.NewError(errors.New("model is required"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	region := vertex.GetModelRegion(info.ApiVersion, modelName)
	predictURL := vertex.BuildGoogleModelURL(info.ChannelBaseUrl, vertex.DefaultAPIVersion, creds.ProjectID, region, modelName, "predict")

	payload, err := buildVertexEmbeddingPredictPayload(inputs, request.Dimensions)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	jsonData, err := common.Marshal(payload)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			return nil, newAPIErrorFromParamOverride(err)
		}
	}

	proxy := ""
	if info.ChannelSetting.Proxy != "" {
		proxy = info.ChannelSetting.Proxy
	}
	token, err := vertexEmbeddingAcquireAccessToken(creds, proxy)
	if err != nil {
		return nil, types.NewOpenAIError(fmt.Errorf("vertex access token: %w", err), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, predictURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := service.GetHttpClient().Do(req)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	statusCodeMappingStr := c.GetString("status_code_mapping")
	if resp.StatusCode != http.StatusOK {
		newAPIError := service.RelayErrorHandler(c.Request.Context(), resp, false)
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return nil, newAPIError
	}

	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, types.NewOpenAIError(readErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	promptTokens := vertexEmbeddingPromptTokens(inputs, modelName)
	if estimated := info.GetEstimatePromptTokens(); estimated > promptTokens {
		promptTokens = estimated
	}
	info.SetEstimatePromptTokens(promptTokens)

	openAIResponse, usage, convErr := vertexEmbeddingPredictResponseToOpenAI(c, modelName, responseBody, promptTokens)
	if convErr != nil {
		return nil, types.NewOpenAIError(convErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	jsonResponse, jsonErr := common.Marshal(openAIResponse)
	if jsonErr != nil {
		return nil, types.NewOpenAIError(jsonErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, jsonResponse)
	return usage, nil
}

func vertexEmbeddingPromptTokens(inputs []string, modelName string) int {
	total := 0
	for _, input := range inputs {
		total += service.CountTokenInput(input, modelName)
	}
	return total
}

func buildVertexEmbeddingPredictPayload(inputs []string, dimensions *int) (map[string]any, error) {
	if len(inputs) == 0 {
		return nil, errors.New("input is empty")
	}
	instances := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		text := strings.TrimSpace(input)
		if text == "" {
			return nil, errors.New("input contains empty string")
		}
		instances = append(instances, map[string]any{
			"task_type": "RETRIEVAL_QUERY",
			"content":   text,
		})
	}
	payload := map[string]any{"instances": instances}
	if dims := lo.FromPtrOr(dimensions, 0); dims > 0 {
		payload["parameters"] = map[string]any{"outputDimensionality": dims}
	}
	return payload, nil
}

func vertexEmbeddingPredictResponseToOpenAI(c *gin.Context, modelName string, body []byte, promptTokens int) (*dto.OpenAIEmbeddingResponse, *dto.Usage, error) {
	var parsed vertexEmbeddingPredictResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("decode vertex embedding response: %w", err)
	}
	if len(parsed.Predictions) == 0 {
		return nil, nil, errors.New("vertex embedding response missing predictions")
	}

	openAIResponse := &dto.OpenAIEmbeddingResponse{
		Object: "list",
		Model:  modelName,
		Data:   make([]dto.OpenAIEmbeddingResponseItem, 0, len(parsed.Predictions)),
	}
	for i, prediction := range parsed.Predictions {
		if len(prediction.Embeddings.Values) == 0 {
			return nil, nil, fmt.Errorf("vertex embedding prediction %d missing values", i)
		}
		openAIResponse.Data = append(openAIResponse.Data, dto.OpenAIEmbeddingResponseItem{
			Object:    "embedding",
			Embedding: prediction.Embeddings.Values,
			Index:     i,
		})
	}

	usage := service.ResponseText2Usage(c, "", modelName, promptTokens)
	openAIResponse.Usage = *usage
	return openAIResponse, usage, nil
}
