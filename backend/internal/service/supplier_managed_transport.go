package service

import (
	"net/url"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// supplierManagedTransport is the fixed NewAPI channel + credential base_url
// used by supplier-managed accounts for one procurement endpoint.
type supplierManagedTransport struct {
	ChannelType int
	Endpoint    string
}

func validateSupplierChannelType(channelType int) error {
	if channelType <= 0 || !newapiintegration.IsKnownChannelType(channelType) {
		return errSupplierSourceInvalidChannelType
	}
	return nil
}

func inferSupplierChannelTypeFromEndpoint(endpoint string) int {
	normalized, err := NormalizeSupplierEndpoint(endpoint)
	if err != nil {
		return newapiconstant.ChannelTypeOpenAI
	}
	if isQianfanSupplierEndpoint(normalized) {
		return newapiconstant.ChannelTypeBaiduV2
	}
	parsed, parseErr := url.Parse(normalized)
	if parseErr != nil || parsed.Host == "" {
		return newapiconstant.ChannelTypeOpenAI
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, "dashscope.aliyuncs.com") {
		return newapiconstant.ChannelTypeAli
	}
	return newapiconstant.ChannelTypeOpenAI
}

func resolveSupplierManagedTransport(endpoint string, channelType int) (supplierManagedTransport, error) {
	normalized, err := NormalizeSupplierEndpoint(endpoint)
	if err != nil {
		return supplierManagedTransport{}, err
	}
	if channelType <= 0 {
		channelType = inferSupplierChannelTypeFromEndpoint(normalized)
	}
	endpointForTransport := normalized
	switch channelType {
	case newapiconstant.ChannelTypeBaiduV2:
		endpointForTransport = newapiintegration.QianfanBaseURL
	}
	return supplierManagedTransport{ChannelType: channelType, Endpoint: endpointForTransport}, nil
}

func isQianfanSupplierEndpoint(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "qianfan.baidubce.com"
}

func supplierManagedEndpointsEqual(left, right string) bool {
	leftTransport, leftErr := resolveSupplierManagedTransport(left, 0)
	rightTransport, rightErr := resolveSupplierManagedTransport(right, 0)
	if leftErr != nil || rightErr != nil {
		leftNorm, leftNormErr := NormalizeSupplierEndpoint(left)
		rightNorm, rightNormErr := NormalizeSupplierEndpoint(right)
		return leftNormErr == nil && rightNormErr == nil && leftNorm == rightNorm
	}
	return leftTransport.ChannelType == rightTransport.ChannelType &&
		leftTransport.Endpoint == rightTransport.Endpoint
}

func supplierManagedTransportOK(account *Account) bool {
	if account == nil || account.Platform != PlatformNewAPI || account.Type != AccountTypeAPIKey {
		return false
	}
	switch account.ChannelType {
	case newapiconstant.ChannelTypeOpenAI, newapiconstant.ChannelTypeBaiduV2, newapiconstant.ChannelTypeAli, newapiconstant.ChannelTypeAnthropic:
		return true
	default:
		return newapiintegration.IsKnownChannelType(account.ChannelType)
	}
}

func supplierReusableAccountTransport(account *Account, sourceEndpoint string, sourceChannelType int) bool {
	if !supplierManagedTransportOK(account) {
		return false
	}
	want, err := resolveSupplierManagedTransport(sourceEndpoint, sourceChannelType)
	if err != nil {
		return false
	}
	return account.ChannelType == want.ChannelType
}
