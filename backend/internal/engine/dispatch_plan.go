package engine

type DispatchPlan struct {
	Provider string
	Endpoint string
}

func (p DispatchPlan) UsesNewAPIBridge() bool {
	return p.Provider == ProviderNewAPIBridge
}
