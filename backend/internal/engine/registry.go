package engine

func BridgeCapabilities() []Capability {
	capabilities := make([]Capability, 0, len(capabilityRegistry))
	for _, endpoint := range []string{
		BridgeEndpointChatCompletions,
		BridgeEndpointResponses,
		BridgeEndpointEmbeddings,
		BridgeEndpointImages,
		BridgeEndpointVideoSubmit,
		BridgeEndpointVideoFetch,
	} {
		capability, ok := CapabilityForEndpoint(endpoint)
		if ok {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}
