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

func resolveSupplierManagedTransport(endpoint string) (supplierManagedTransport, error) {
	normalized, err := NormalizeSupplierEndpoint(endpoint)
	if err != nil {
		return supplierManagedTransport{}, err
	}
	if isQianfanSupplierEndpoint(normalized) {
		return supplierManagedTransport{
			ChannelType: newapiconstant.ChannelTypeBaiduV2,
			Endpoint:    newapiintegration.QianfanBaseURL,
		}, nil
	}
	return supplierManagedTransport{
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Endpoint:    normalized,
	}, nil
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
	leftTransport, leftErr := resolveSupplierManagedTransport(left)
	rightTransport, rightErr := resolveSupplierManagedTransport(right)
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
	case newapiconstant.ChannelTypeOpenAI, newapiconstant.ChannelTypeBaiduV2:
		return true
	default:
		return false
	}
}

func supplierReusableAccountTransport(account *Account, sourceEndpoint string) bool {
	if !supplierManagedTransportOK(account) {
		return false
	}
	want, err := resolveSupplierManagedTransport(sourceEndpoint)
	if err != nil {
		return false
	}
	return account.ChannelType == want.ChannelType
}
