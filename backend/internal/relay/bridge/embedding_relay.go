package bridge

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	newapirelay "github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// RunEmbeddingRelay mirrors relay.EmbeddingHelper without New API billing.
func RunEmbeddingRelay(c *gin.Context, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	info.InitChannelMeta(c)

	embeddingReq, ok := info.Request.(*dto.EmbeddingRequest)
	if !ok {
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected *dto.EmbeddingRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(embeddingReq)
	if err != nil {
		return nil, types.NewError(fmt.Errorf("failed to copy request to EmbeddingRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	if info.ChannelType == newapiconstant.ChannelTypeVertexAi {
		return runVertexEmbeddingRelay(c, info, request)
	}
	if info.ChannelType == newapiconstant.ChannelTypeBaiduV2 {
		return runQianfanEmbeddingRelay(c, info, request)
	}

	adaptor := newapirelay.GetAdaptor(info.ApiType)
	if adaptor == nil {
		return nil, types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	convertedRequest, err := adaptor.ConvertEmbeddingRequest(c, info, *request)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}
	return runAdaptorEmbeddingRelay(c, info, adaptor, convertedRequest)
}

func runAdaptorEmbeddingRelay(c *gin.Context, info *relaycommon.RelayInfo, adaptor channel.Adaptor, convertedRequest any) (*dto.Usage, *types.NewAPIError) {
	relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)
	jsonData, err := common.Marshal(convertedRequest)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
	}

	if len(info.ParamOverride) > 0 {
		jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
		if err != nil {
			return nil, newAPIErrorFromParamOverride(err)
		}
	}

	requestBody := bytes.NewBuffer(jsonData)
	statusCodeMappingStr := c.GetString("status_code_mapping")
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	var httpResp *http.Response
	if resp != nil {
		var ok bool
		httpResp, ok = resp.(*http.Response)
		if !ok || httpResp == nil {
			return nil, types.NewError(fmt.Errorf("unexpected DoRequest response type %T", resp), types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		if httpResp.StatusCode != http.StatusOK {
			newAPIError := service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return nil, newAPIError
		}
	}

	usageAny, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return nil, newAPIError
	}
	u, ok := usageAny.(*dto.Usage)
	if !ok || u == nil {
		return nil, types.NewError(fmt.Errorf("unexpected usage type %T", usageAny), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	return u, nil
}
