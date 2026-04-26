package engine

type BridgeDispatchInput struct {
	AccountPlatform string
	ChannelType     int
	Endpoint        string
	BridgeEnabled   bool
}

func BuildDispatchPlan(in BridgeDispatchInput) DispatchPlan {
	if !in.BridgeEnabled || in.ChannelType <= 0 || !BridgeEndpointEnabled(in.Endpoint) {
		return DispatchPlan{Provider: ProviderNative, Endpoint: in.Endpoint}
	}
	return DispatchPlan{Provider: ProviderNewAPIBridge, Endpoint: in.Endpoint}
}
