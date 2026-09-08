package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

const NVIDIABuildBaseURL = "https://integrate.api.nvidia.com"

func IsNVIDIABuildBaseURL(channelType int, base string) bool {
	return channelType == newapiconstant.ChannelTypeOpenAI &&
		strings.TrimRight(strings.TrimSpace(base), "/") == NVIDIABuildBaseURL
}
