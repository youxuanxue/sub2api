package newapi

import (
	"sort"

	newapicommon "github.com/QuantumNous/new-api/common"
	newapiconstant "github.com/QuantumNous/new-api/constant"
)

type ChannelTypeInfo struct {
	ChannelType int    `json:"channel_type"`
	Name        string `json:"name"`
	APIType     int    `json:"api_type"`
	HasAdaptor  bool   `json:"has_adaptor"`
	BaseURL     string `json:"base_url"`
}

// ListChannelTypes returns New API channel type catalog for admin UIs.
func ListChannelTypes() []ChannelTypeInfo {
	keys := make([]int, 0, len(newapiconstant.ChannelTypeNames))
	for channelType := range newapiconstant.ChannelTypeNames {
		if channelType <= newapiconstant.ChannelTypeUnknown || channelType >= newapiconstant.ChannelTypeDummy {
			continue
		}
		keys = append(keys, channelType)
	}
	sort.Ints(keys)

	out := make([]ChannelTypeInfo, 0, len(keys))
	for _, channelType := range keys {
		name := newapiconstant.GetChannelTypeName(channelType)
		apiType, ok := newapicommon.ChannelType2APIType(channelType)
		var baseURL string
		if channelType >= 0 && channelType < len(newapiconstant.ChannelBaseURLs) {
			baseURL = newapiconstant.ChannelBaseURLs[channelType]
		}
		out = append(out, ChannelTypeInfo{
			ChannelType: channelType,
			Name:        name,
			APIType:     apiType,
			HasAdaptor:  ok,
			BaseURL:     baseURL,
		})
	}
	return out
}
