package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestUS048_ResolveSupplierManagedTransportSelectsBaiduV2ForQianfan(t *testing.T) {
	for _, endpoint := range []string{
		"https://qianfan.baidubce.com",
		"https://qianfan.baidubce.com/",
		"https://qianfan.baidubce.com/v1",
		"https://qianfan.baidubce.com/v2",
		"https://Qianfan.BaiduBce.com/v2/chat/completions",
	} {
		transport, err := resolveSupplierManagedTransport(endpoint)
		require.NoError(t, err, endpoint)
		require.Equal(t, newapiconstant.ChannelTypeBaiduV2, transport.ChannelType, endpoint)
		require.Equal(t, newapiintegration.QianfanBaseURL, transport.Endpoint, endpoint)
	}
}

func TestUS048_ResolveSupplierManagedTransportKeepsOpenAIForGenericHosts(t *testing.T) {
	transport, err := resolveSupplierManagedTransport("https://token.vstecscloud.com/v1")
	require.NoError(t, err)
	require.Equal(t, newapiconstant.ChannelTypeOpenAI, transport.ChannelType)
	require.Equal(t, "https://token.vstecscloud.com/v1", transport.Endpoint)
}

func TestUS048_SupplierManagedEndpointsEqualCollapsesQianfanPathVariants(t *testing.T) {
	require.True(t, supplierManagedEndpointsEqual(
		"https://qianfan.baidubce.com/v2",
		"https://qianfan.baidubce.com",
	))
	require.False(t, supplierManagedEndpointsEqual(
		"https://qianfan.baidubce.com",
		"https://token.vstecscloud.com/v1",
	))
}
