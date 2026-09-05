package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/engine"
)

func accountSupportsOpenAIVideoCapability(account *Account) bool {
	if account == nil {
		return false
	}
	if UsesGrokNativeVideoArm(account) {
		return true
	}
	return engine.IsVideoSupportedForAccount(account.Platform, account.ChannelType)
}

func accountSupportsOpenAIRequestCapabilities(
	account *Account,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requiredVideoSupport bool,
) bool {
	if !accountSupportsOpenAICapabilities(account, requiredCapability, requiredImageCapability) {
		return false
	}
	if requiredVideoSupport && !accountSupportsOpenAIVideoCapability(account) {
		return false
	}
	return true
}

func accountSupportsOpenAIRequestCapabilitiesForContext(
	ctx context.Context,
	account *Account,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requiredVideoSupport bool,
) bool {
	if protocolRoutingOwnsOpenAITextCapability(ctx, requiredCapability) {
		return account != nil &&
			account.SupportsOpenAIImageCapability(requiredImageCapability) &&
			(!requiredVideoSupport || accountSupportsOpenAIVideoCapability(account))
	}
	return accountSupportsOpenAIRequestCapabilities(
		account,
		requiredCapability,
		requiredImageCapability,
		requiredVideoSupport,
	)
}

func accountSupportsOpenAICapabilities(account *Account, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability) bool {
	if account == nil {
		return false
	}
	if !tkOpenAICompatEndpointCapabilityAllows(account, requiredCapability) {
		return false
	}
	return account.SupportsOpenAIImageCapability(requiredImageCapability)
}
