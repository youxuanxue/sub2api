package bridge

import (
	"fmt"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	newapirelay "github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// runQianfanEmbeddingRelay forwards OpenAI-shaped embedding requests to Baidu
// Qianfan v2. Upstream new-api's baidu_v2 adaptor leaves ConvertEmbeddingRequest
// unimplemented even though GetRequestURL targets /v2/embeddings.
func runQianfanEmbeddingRelay(c *gin.Context, info *relaycommon.RelayInfo, request *dto.EmbeddingRequest) (*dto.Usage, *types.NewAPIError) {
	if c == nil || info == nil || request == nil {
		return nil, types.NewError(fmt.Errorf("qianfan embedding relay missing context"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}
	if info.ChannelType != newapiconstant.ChannelTypeBaiduV2 {
		return nil, types.NewError(fmt.Errorf("qianfan embedding relay expected channel_type %d, got %d", newapiconstant.ChannelTypeBaiduV2, info.ChannelType), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	adaptor := newapirelay.GetAdaptor(info.ApiType)
	if adaptor == nil {
		return nil, types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	return runAdaptorEmbeddingRelay(c, info, adaptor, request)
}
