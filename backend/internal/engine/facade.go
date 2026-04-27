package engine

type BridgeDispatchInput struct {
	AccountPlatform string
	ChannelType     int
	Endpoint        string
	BridgeEnabled   bool
}

func BuildDispatchPlan(in BridgeDispatchInput) DispatchPlan {
	capability, ok := CapabilityForEndpoint(in.Endpoint)
	if !ok || !in.BridgeEnabled {
		return DispatchPlan{Provider: ProviderNative, Endpoint: in.Endpoint}
	}
	if !capability.SupportsSchedulingPlatform(in.AccountPlatform) {
		return DispatchPlan{Provider: ProviderNative, Endpoint: in.Endpoint}
	}
	if capability.RequiresChannelType && in.ChannelType <= 0 {
		return DispatchPlan{Provider: ProviderNative, Endpoint: in.Endpoint}
	}
	if capability.RequiresTaskAdaptor && !IsVideoSupportedChannelType(in.ChannelType) {
		return DispatchPlan{Provider: ProviderNative, Endpoint: in.Endpoint}
	}
	return DispatchPlan{Provider: capability.Provider, Endpoint: in.Endpoint}
}
