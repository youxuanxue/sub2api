package engine

import (
	"strconv"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapirelay "github.com/QuantumNous/new-api/relay"
	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type Capability struct {
	Endpoint            string
	Provider            string
	SchedulingPlatforms []string
	RequiresChannelType bool
	RequiresTaskAdaptor bool
}

var capabilityRegistry = map[string]Capability{
	BridgeEndpointChatCompletions: {
		Endpoint:            BridgeEndpointChatCompletions,
		Provider:            ProviderNewAPIBridge,
		SchedulingPlatforms: []string{domain.PlatformOpenAI, domain.PlatformNewAPI},
		RequiresChannelType: true,
	},
	BridgeEndpointResponses: {
		Endpoint:            BridgeEndpointResponses,
		Provider:            ProviderNewAPIBridge,
		SchedulingPlatforms: []string{domain.PlatformOpenAI, domain.PlatformNewAPI},
		RequiresChannelType: true,
	},
	BridgeEndpointEmbeddings: {
		Endpoint:            BridgeEndpointEmbeddings,
		Provider:            ProviderNewAPIBridge,
		SchedulingPlatforms: []string{domain.PlatformOpenAI, domain.PlatformNewAPI},
		RequiresChannelType: true,
	},
	BridgeEndpointImages: {
		Endpoint:            BridgeEndpointImages,
		Provider:            ProviderNewAPIBridge,
		SchedulingPlatforms: []string{domain.PlatformOpenAI, domain.PlatformNewAPI},
		RequiresChannelType: true,
	},
	BridgeEndpointVideoSubmit: {
		Endpoint:            BridgeEndpointVideoSubmit,
		Provider:            ProviderNewAPIBridge,
		SchedulingPlatforms: []string{domain.PlatformOpenAI, domain.PlatformNewAPI},
		RequiresChannelType: true,
		RequiresTaskAdaptor: true,
	},
	BridgeEndpointVideoFetch: {
		Endpoint:            BridgeEndpointVideoFetch,
		Provider:            ProviderNewAPIBridge,
		SchedulingPlatforms: []string{domain.PlatformOpenAI, domain.PlatformNewAPI},
		RequiresChannelType: true,
		RequiresTaskAdaptor: true,
	},
}

func CapabilityForEndpoint(endpoint string) (Capability, bool) {
	capability, ok := capabilityRegistry[endpoint]
	return capability, ok
}

func (c Capability) SupportsSchedulingPlatform(platform string) bool {
	for _, candidate := range c.SchedulingPlatforms {
		if candidate == platform {
			return true
		}
	}
	return false
}

func BridgeEndpointEnabled(endpoint string) bool {
	_, ok := CapabilityForEndpoint(endpoint)
	return ok
}

func EndpointRequiresTaskAdaptor(endpoint string) bool {
	capability, ok := CapabilityForEndpoint(endpoint)
	return ok && capability.RequiresTaskAdaptor
}

func IsVideoSupportedChannelType(channelType int) bool {
	return taskAdaptorRegistered(channelType)
}

func taskAdaptorRegistered(channelType int) bool {
	if channelType <= 0 {
		return false
	}
	platform := newapiconstant.TaskPlatform(strconv.Itoa(channelType))
	return newapirelay.GetTaskAdaptor(platform) != nil
}
