package handler

import (
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
)

// TKProviderSet is the Wire provider set for TokenKey-only handler DI.
// Composed into ProviderSet so upstream-shaped wire.go stays free of TK
// provider listings (CLAUDE.md §5 companion pattern).
var TKProviderSet = wire.NewSet(
	ProvideTKAPIKeyHandler,
	ProvideTKPricingCatalogHandler,
	NewMePricingCatalogHandler,
	ProvideTKGatewayHandlerModelList,
	ProvideEdgeCapacityHandler,
	NewGeminiWebSessionHandler,
	admin.NewTierHandler,
	ProvideTrialProvisionHandler,
)

func ProvideTKAPIKeyHandler(
	apiKeyService *service.APIKeyService,
	capabilities *service.UniversalCapabilityService,
) *APIKeyHandler {
	h := NewAPIKeyHandler(apiKeyService)
	h.SetCapabilityService(apiKeyService, capabilities)
	return h
}

// TKGatewayHandlerModelListReady is a wire sentinel: holding it proves
// GatewayHandler.SetModelListFilter has been called with the production
// ModelListFilter. provideCleanup (cmd/server/wire.go) takes this type as an
// unused parameter to force wire to evaluate the side-effect.
type TKGatewayHandlerModelListReady struct{}

// ProvideTKGatewayHandlerModelList wires the model-list filter onto
// GatewayHandler post-construction. Mirrors ProvideTKGatewayPricingAvailability
// in shape; SetModelListFilter is nil-safe (degraded → fail-open).
func ProvideTKGatewayHandlerModelList(
	h *GatewayHandler,
	openAI *OpenAIGatewayHandler,
	f *service.ModelListFilter,
	capabilities *service.UniversalCapabilityService,
) TKGatewayHandlerModelListReady {
	if h != nil {
		h.SetModelListFilter(f)
		h.SetUniversalCapabilityService(capabilities)
	}
	if openAI != nil {
		openAI.SetUniversalCapabilityService(capabilities)
	}
	return TKGatewayHandlerModelListReady{}
}

// ProvideTKPricingCatalogHandler wraps the upstream-shape NewPricingCatalogHandler
// constructor with TK-only post-construction wiring for the pricing-availability
// observability service. Keeping NewPricingCatalogHandler's signature stable
// (CLAUDE.md §5 — minimal injection point) lets upstream merges of the
// constructor not touch TK extensions, AND the assignment survives `go run wire`
// regenerations (a manual edit in wire_gen.go would not).
//
// Mirrors ProvideOpenAIGatewayHandler in shape.
func ProvideTKPricingCatalogHandler(
	catalog *service.PricingCatalogService,
	gate *service.SettingService,
	avail *service.PricingAvailabilityService,
) *PricingCatalogHandler {
	h := NewPricingCatalogHandler(catalog, gate)
	h.SetAvailabilityService(avail)
	return h
}

// ProvideEdgeCapacityHandler adapts the wire-provided service.AccountRepository
// (which satisfies the handler's narrow schedulingCapacityReader interface) to
// the edge capacity handler. A dedicated provider avoids needing a wire.Bind for
// the unexported interface and keeps NewEdgeCapacityHandler unit-test friendly.
func ProvideEdgeCapacityHandler(accountRepo service.AccountRepository) *EdgeCapacityHandler {
	return NewEdgeCapacityHandler(accountRepo)
}

// ProvideTrialProvisionHandler constructs the Invite-to-Trial service from
// already-wired deps and wraps it in the admin handler. Keeping construction in
// a Provide func (rather than registering a separate service provider) avoids
// adding the concrete service to the provider set. See user_handler_tk_provision.go.
func ProvideTrialProvisionHandler(
	subscriptionService *service.SubscriptionService,
	apiKeyService *service.APIKeyService,
	settingService *service.SettingService,
	userRepo service.UserRepository,
	userGroupRateRepo service.UserGroupRateRepository,
	groupRepo service.GroupRepository,
	redeemCodeRepo service.RedeemCodeRepository,
	entClient *dbent.Client,
) *admin.TrialProvisionHandler {
	svc := service.NewTrialProvisionService(
		subscriptionService,
		apiKeyService,
		settingService,
		userRepo,
		userGroupRateRepo,
		groupRepo,
		redeemCodeRepo,
		entClient,
	)
	return admin.NewTrialProvisionHandler(svc)
}
