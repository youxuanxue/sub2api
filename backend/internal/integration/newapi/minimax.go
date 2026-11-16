package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// MiniMaxCNBaseURL is the mainland OpenAI-compatible MiniMax host.
// International api.minimax.io uses a separate key space and must not share
// this property-selected floor.
const MiniMaxCNBaseURL = "https://api.minimaxi.com"

func IsMiniMaxCNBaseURL(channelType int, base string) bool {
	return channelType == newapiconstant.ChannelTypeOpenAI &&
		strings.EqualFold(strings.TrimRight(strings.TrimSpace(base), "/"), MiniMaxCNBaseURL)
}
