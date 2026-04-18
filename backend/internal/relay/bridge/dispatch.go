package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

var ensureNewAPIDepsOnce sync.Once

func ensureNewAPIDeps() {
	ensureNewAPIDepsOnce.Do(func() {
		if service.GetHttpClient() == nil {
			service.InitHttpClient()
		}
		if newapiconstant.StreamingTimeout <= 0 {
			newapiconstant.StreamingTimeout = 300
		}
		if newapiconstant.StreamScannerMaxBufferMB <= 0 {
			newapiconstant.StreamScannerMaxBufferMB = 128
		}
	})
}

// DispatchOutcome is the result of a bridge dispatch (response bytes are written to c).
type DispatchOutcome struct {
	Usage *dto.Usage

	Model           string
	UpstreamModel   string
	Stream          bool
	Duration        time.Duration
	AdaptorRelayFmt types.RelayFormat
	AdaptorAPIType  int
}

func installBodyStorage(c *gin.Context, body []byte) error {
	stor, err := common.CreateBodyStorage(body)
	if err != nil {
		return err
	}
	c.Set(common.KeyBodyStorage, stor)
	return nil
}

// DispatchChatCompletions runs the New API adaptor for OpenAI Chat Completions.
func DispatchChatCompletions(_ context.Context, c *gin.Context, in ChannelContextInput, body []byte) (*DispatchOutcome, *types.NewAPIError) {
	ensureNewAPIDeps()
	if err := installBodyStorage(c, body); err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}

	var req dto.GeneralOpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	PopulateContextKeys(c, in)
	SetOriginalModel(c, req.Model)
	if c.GetString(common.RequestIdKey) == "" {
		SetRequestID(c, NewRequestID())
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAI, &req, nil)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithSkipRetry())
	}

	start := time.Now()
	usage, apiErr := RunOpenAITextRelay(c, relayInfo)
	dur := time.Since(start)
	if apiErr != nil {
		return nil, apiErr
	}

	upstream := relayInfo.OriginModelName
	if relayInfo.ChannelMeta != nil && relayInfo.UpstreamModelName != "" {
		upstream = relayInfo.UpstreamModelName
	}

	apiType := 0
	if relayInfo.ChannelMeta != nil {
		apiType = relayInfo.ApiType
	}

	return &DispatchOutcome{
		Usage:           usage,
		Model:           req.Model,
		UpstreamModel:   upstream,
		Stream:          lo.FromPtrOr(req.Stream, false),
		Duration:        dur,
		AdaptorRelayFmt: types.RelayFormatOpenAI,
		AdaptorAPIType:  apiType,
	}, nil
}

// DispatchResponses runs the New API adaptor for OpenAI Responses API.
func DispatchResponses(_ context.Context, c *gin.Context, in ChannelContextInput, body []byte) (*DispatchOutcome, *types.NewAPIError) {
	ensureNewAPIDeps()
	if err := installBodyStorage(c, body); err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}

	var req dto.OpenAIResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	PopulateContextKeys(c, in)
	SetOriginalModel(c, req.Model)
	if c.GetString(common.RequestIdKey) == "" {
		SetRequestID(c, NewRequestID())
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIResponses, &req, nil)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithSkipRetry())
	}

	start := time.Now()
	usage, apiErr := RunOpenAIResponsesRelay(c, relayInfo)
	dur := time.Since(start)
	if apiErr != nil {
		return nil, apiErr
	}

	upstream := relayInfo.OriginModelName
	if relayInfo.ChannelMeta != nil && relayInfo.UpstreamModelName != "" {
		upstream = relayInfo.UpstreamModelName
	}
	apiType := 0
	if relayInfo.ChannelMeta != nil {
		apiType = relayInfo.ApiType
	}

	return &DispatchOutcome{
		Usage:           usage,
		Model:           req.Model,
		UpstreamModel:   upstream,
		Stream:          lo.FromPtrOr(req.Stream, false),
		Duration:        dur,
		AdaptorRelayFmt: types.RelayFormatOpenAIResponses,
		AdaptorAPIType:  apiType,
	}, nil
}

// DispatchEmbeddings runs the New API adaptor for /v1/embeddings.
func DispatchEmbeddings(_ context.Context, c *gin.Context, in ChannelContextInput, body []byte) (*DispatchOutcome, *types.NewAPIError) {
	ensureNewAPIDeps()
	if err := installBodyStorage(c, body); err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}

	var req dto.EmbeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	PopulateContextKeys(c, in)
	SetOriginalModel(c, req.Model)
	if c.GetString(common.RequestIdKey) == "" {
		SetRequestID(c, NewRequestID())
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatEmbedding, &req, nil)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithSkipRetry())
	}

	start := time.Now()
	usage, apiErr := RunEmbeddingRelay(c, relayInfo)
	dur := time.Since(start)
	if apiErr != nil {
		return nil, apiErr
	}

	upstream := relayInfo.OriginModelName
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelMeta.UpstreamModelName != "" {
		upstream = relayInfo.ChannelMeta.UpstreamModelName
	}
	apiType := 0
	if relayInfo.ChannelMeta != nil {
		apiType = relayInfo.ChannelMeta.ApiType
	}

	return &DispatchOutcome{
		Usage:           usage,
		Model:           req.Model,
		UpstreamModel:   upstream,
		Stream:          false,
		Duration:        dur,
		AdaptorRelayFmt: types.RelayFormatEmbedding,
		AdaptorAPIType:  apiType,
	}, nil
}

// DispatchImageGenerations runs the New API adaptor for /v1/images/generations.
func DispatchImageGenerations(_ context.Context, c *gin.Context, in ChannelContextInput, body []byte) (*DispatchOutcome, *types.NewAPIError) {
	ensureNewAPIDeps()
	if err := installBodyStorage(c, body); err != nil {
		return nil, types.NewError(err, types.ErrorCodeReadRequestBodyFailed, types.ErrOptionWithSkipRetry())
	}

	var req dto.ImageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	PopulateContextKeys(c, in)
	SetOriginalModel(c, req.Model)
	if c.GetString(common.RequestIdKey) == "" {
		SetRequestID(c, NewRequestID())
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatOpenAIImage, &req, nil)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeGenRelayInfoFailed, types.ErrOptionWithSkipRetry())
	}

	start := time.Now()
	usage, apiErr := RunImageRelay(c, relayInfo)
	dur := time.Since(start)
	if apiErr != nil {
		return nil, apiErr
	}

	upstream := relayInfo.OriginModelName
	if relayInfo.ChannelMeta != nil && relayInfo.ChannelMeta.UpstreamModelName != "" {
		upstream = relayInfo.ChannelMeta.UpstreamModelName
	}
	apiType := 0
	if relayInfo.ChannelMeta != nil {
		apiType = relayInfo.ChannelMeta.ApiType
	}

	return &DispatchOutcome{
		Usage:           usage,
		Model:           req.Model,
		UpstreamModel:   upstream,
		Stream:          false,
		Duration:        dur,
		AdaptorRelayFmt: types.RelayFormatOpenAIImage,
		AdaptorAPIType:  apiType,
	}, nil
}

// DescribeRelayFormat returns a short metric label for logs.
func DescribeRelayFormat(f types.RelayFormat) string {
	if f == "" {
		return "unknown"
	}
	return string(f)
}

// DescribeAPIType returns api_type as string for logs.
func DescribeAPIType(apiType int) string {
	return fmt.Sprintf("%d", apiType)
}
