package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestIsMiniMaxCNBaseURL(t *testing.T) {
	t.Parallel()
	require.True(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeOpenAI, MiniMaxCNBaseURL))
	require.True(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeOpenAI, MiniMaxCNBaseURL+"/"))
	require.True(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeOpenAI, "HTTPS://API.MINIMAXI.COM"))
	require.False(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeOpenAI, "https://api.minimax.io"))
	require.False(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeOpenAI, "https://api.minimax.chat"))
	require.False(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeOpenAI, MiniMaxCNBaseURL+"/v1"))
	require.False(t, IsMiniMaxCNBaseURL(newapiconstant.ChannelTypeMiniMax, MiniMaxCNBaseURL))
}
