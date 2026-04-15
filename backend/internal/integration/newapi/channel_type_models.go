package newapi

import (
	"strconv"

	newapicommon "github.com/QuantumNous/new-api/common"
	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// ChannelTypeModels returns default model IDs per New API channel type, matching
// new-api's controller.channelId2Models (used by GET /api/models and the
// "填入相关模型" button via getChannelModels(type)).
func ChannelTypeModels() map[int][]string {
	out := make(map[int][]string)
	for i := 1; i <= newapiconstant.ChannelTypeDummy; i++ {
		apiType, ok := newapicommon.ChannelType2APIType(i)
		if !ok || apiType == newapiconstant.APITypeAIProxyLibrary {
			continue
		}
		meta := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelType: i,
			},
		}
		adaptor := relay.GetAdaptor(apiType)
		if adaptor == nil {
			continue
		}
		adaptor.Init(meta)
		out[i] = adaptor.GetModelList()
	}
	return out
}

// ChannelTypeModelsJSON returns the same data with string keys for JSON objects
// (JSON object keys are always strings; new-api frontend indexes by type string).
func ChannelTypeModelsJSON() map[string][]string {
	raw := ChannelTypeModels()
	out := make(map[string][]string, len(raw))
	for k, v := range raw {
		out[strconv.Itoa(k)] = v
	}
	return out
}
