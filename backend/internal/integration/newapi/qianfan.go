package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// QianfanBaseURL is the OpenAI-compatible Baidu Qianfan v2 API root.
const QianfanBaseURL = "https://qianfan.baidubce.com"

// IsQianfanBaseURL reports whether channelType/base resolve to the Baidu Qianfan v2 endpoint.
func IsQianfanBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeBaiduV2 {
		return false
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return false
	}
	base = strings.TrimRight(strings.ToLower(base), "/")
	return base == QianfanBaseURL
}
