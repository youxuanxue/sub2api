package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// UniversalProtocol identifies a client adapter, not an upstream provider.
type UniversalProtocol string

const (
	UniversalProtocolAll         UniversalProtocol = "all"
	UniversalProtocolAnthropic   UniversalProtocol = "anthropic"
	UniversalProtocolOpenAI      UniversalProtocol = "openai"
	UniversalProtocolGemini      UniversalProtocol = "gemini"
	UniversalProtocolCodex       UniversalProtocol = "codex"
	UniversalProtocolAntigravity UniversalProtocol = "antigravity"
)

type UniversalModality string

const (
	UniversalModalityChat      UniversalModality = "chat"
	UniversalModalityEmbedding UniversalModality = "embedding"
	UniversalModalityImage     UniversalModality = "image"
	UniversalModalityVideo     UniversalModality = "video"
)

type UniversalSelectedGroup struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

type UniversalCapabilityRoute struct {
	Protocol UniversalProtocol      `json:"protocol"`
	Modality UniversalModality      `json:"modality"`
	Group    UniversalSelectedGroup `json:"selected_group"`
}

type UniversalCapability struct {
	ID            string                     `json:"id"`
	Protocols     []UniversalProtocol        `json:"protocols"`
	Modalities    []UniversalModality        `json:"modalities"`
	Routes        []UniversalCapabilityRoute `json:"routes"`
	SelectedGroup UniversalSelectedGroup     `json:"selected_group"`
}

var ErrUniversalCapabilityUnavailable = errors.New("api key capability discovery unavailable")

type universalCapabilityCandidates func(ctx context.Context, groupID int64, platform string) (ids []string, catalogFallback bool, err error)
type universalCapabilitySupports func(ctx context.Context, groupID int64, platform, model string, shape UniversalShape) (bool, error)
type universalCapabilityFallback func(ctx context.Context, platform string) ([]string, error)

type UniversalCapabilityService struct {
	entitlements availableGroupsLister
	candidates   universalCapabilityCandidates
	supports     universalCapabilitySupports
	fallback     universalCapabilityFallback
}

func NewUniversalCapabilityService(
	apiKeys *APIKeyService,
	gateway *GatewayService,
	filter *ModelListFilter,
) *UniversalCapabilityService {
	if apiKeys == nil || gateway == nil {
		return newUniversalCapabilityService(apiKeys, nil, nil, nil)
	}
	return newUniversalCapabilityService(
		apiKeys,
		func(ctx context.Context, groupID int64, platform string) ([]string, bool, error) {
			ids, passthrough, err := gateway.GetAvailableModelsForDiscovery(ctx, groupID, platform)
			if err != nil {
				return nil, false, err
			}
			filtered, filterErr := filter.FilterClientFacingStrict(ctx, platform, ids)
			return filtered, passthrough, filterErr
		},
		func(ctx context.Context, groupID int64, platform, model string, shape UniversalShape) (bool, error) {
			return gateway.UniversalGroupSupportsRequestStrict(ctx, groupID, platform, model, shape)
		},
		func(ctx context.Context, platform string) ([]string, error) {
			return filter.ServableClientFacingIDsStrict(ctx, platform)
		},
	)
}

func newUniversalCapabilityService(
	entitlements availableGroupsLister,
	candidates universalCapabilityCandidates,
	supports universalCapabilitySupports,
	fallback universalCapabilityFallback,
) *UniversalCapabilityService {
	return &UniversalCapabilityService{
		entitlements: entitlements,
		candidates:   candidates,
		supports:     supports,
		fallback:     fallback,
	}
}

type universalCapabilityShape struct {
	protocol       UniversalProtocol
	modality       UniversalModality
	shape          UniversalShape
	forcedPlatform string
}

var universalCapabilityShapes = []universalCapabilityShape{
	{protocol: UniversalProtocolAnthropic, modality: UniversalModalityChat, shape: ShapeAnthropicMessages},
	{protocol: UniversalProtocolOpenAI, modality: UniversalModalityChat, shape: ShapeOpenAIChat},
	{protocol: UniversalProtocolOpenAI, modality: UniversalModalityEmbedding, shape: ShapeOpenAIEmbeddings},
	{protocol: UniversalProtocolOpenAI, modality: UniversalModalityImage, shape: ShapeOpenAIImages},
	{protocol: UniversalProtocolOpenAI, modality: UniversalModalityImage, shape: ShapeOpenAIImagesEdit},
	{protocol: UniversalProtocolOpenAI, modality: UniversalModalityVideo, shape: ShapeOpenAIVideo},
	{protocol: UniversalProtocolGemini, modality: UniversalModalityChat, shape: ShapeGemini},
	{protocol: UniversalProtocolCodex, modality: UniversalModalityChat, shape: ShapeOpenAIChat, forcedPlatform: PlatformOpenAI},
	{protocol: UniversalProtocolAntigravity, modality: UniversalModalityChat, shape: ShapeAnthropicMessages, forcedPlatform: PlatformAntigravity},
	{protocol: UniversalProtocolAntigravity, modality: UniversalModalityChat, shape: ShapeGemini, forcedPlatform: PlatformAntigravity},
	{protocol: UniversalProtocolAntigravity, modality: UniversalModalityChat, shape: ShapeOpenAIChat, forcedPlatform: PlatformAntigravity},
}

// List returns the callable model capabilities for one key and client protocol.
// The resolver is used only against an in-memory entitlement snapshot, so metadata
// discovery never locks the request key to one backing group.
func (s *UniversalCapabilityService) List(ctx context.Context, apiKey *APIKey, protocol UniversalProtocol) ([]UniversalCapability, error) {
	if s == nil || s.entitlements == nil || s.candidates == nil {
		return nil, ErrUniversalCapabilityUnavailable
	}
	if apiKey == nil {
		return nil, fmt.Errorf("%w: api key is nil", ErrUniversalCapabilityUnavailable)
	}
	ctx = withUniversalCapabilityAccountCache(ctx)

	groups, err := s.groupsForKey(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	groups = universalCapabilityGroupsForProtocol(groups, protocol)
	if len(groups) == 0 {
		return []UniversalCapability{}, nil
	}

	modelSet := make(map[string]struct{})
	for i := range groups {
		group := groups[i]
		ids, catalogFallback, candidateErr := s.candidates(ctx, group.ID, group.Platform)
		if candidateErr != nil {
			return nil, fmt.Errorf("list capability candidates for group %d: %w", group.ID, candidateErr)
		}
		if catalogFallback && s.fallback != nil {
			fallbackIDs, fallbackErr := s.fallback(ctx, group.Platform)
			if fallbackErr != nil {
				return nil, fmt.Errorf("list capability fallback candidates for group %d: %w", group.ID, fallbackErr)
			}
			ids = append(ids, fallbackIDs...)
		}
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				modelSet[id] = struct{}{}
			}
		}
	}

	modelIDs := make([]string, 0, len(modelSet))
	for id := range modelSet {
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)
	if len(modelIDs) > 0 && s.supports == nil {
		return nil, ErrUniversalCapabilityUnavailable
	}

	out := make([]UniversalCapability, 0, len(modelIDs))
	for _, model := range modelIDs {
		capability, capabilityErr := s.capabilityForModel(ctx, apiKey, groups, model, protocol)
		if capabilityErr != nil {
			return nil, capabilityErr
		}
		if len(capability.Routes) > 0 {
			out = append(out, capability)
		}
	}
	return out, nil
}

func (s *UniversalCapabilityService) groupsForKey(ctx context.Context, apiKey *APIKey) ([]Group, error) {
	if apiKey.IsUniversal() {
		groups, err := s.entitlements.GetAvailableGroups(ctx, apiKey.UserID)
		if err != nil {
			return nil, fmt.Errorf("list capability entitlements: %w", err)
		}
		return activeCapabilityGroups(groups), nil
	}
	if apiKey.Group != nil && apiKey.Group.IsActive() {
		return []Group{*apiKey.Group}, nil
	}
	if apiKey.GroupID == nil {
		return []Group{}, nil
	}
	groups, err := s.entitlements.GetAvailableGroups(ctx, apiKey.UserID)
	if err != nil {
		return nil, fmt.Errorf("list capability entitlements: %w", err)
	}
	for i := range groups {
		if groups[i].ID == *apiKey.GroupID && groups[i].IsActive() && !isUniversalProbeGroup(groups[i]) {
			return []Group{groups[i]}, nil
		}
	}
	return []Group{}, nil
}

func activeCapabilityGroups(groups []Group) []Group {
	out := make([]Group, 0, len(groups))
	for i := range groups {
		if groups[i].IsActive() && !isUniversalProbeGroup(groups[i]) {
			out = append(out, groups[i])
		}
	}
	return out
}

func universalCapabilityGroupsForProtocol(groups []Group, protocol UniversalProtocol) []Group {
	forcedPlatform := ""
	for _, spec := range universalCapabilityShapes {
		if spec.protocol == protocol && spec.forcedPlatform != "" {
			forcedPlatform = spec.forcedPlatform
			break
		}
	}
	if forcedPlatform == "" {
		return groups
	}
	out := make([]Group, 0, len(groups))
	for i := range groups {
		if groups[i].Platform == forcedPlatform {
			out = append(out, groups[i])
		}
	}
	return out
}

func (s *UniversalCapabilityService) capabilityForModel(
	ctx context.Context,
	apiKey *APIKey,
	groups []Group,
	model string,
	protocol UniversalProtocol,
) (UniversalCapability, error) {
	capability := UniversalCapability{ID: model}
	seenProtocol := make(map[UniversalProtocol]bool)
	seenModality := make(map[UniversalModality]bool)
	seenRoute := make(map[string]bool)

	for _, spec := range universalCapabilityShapes {
		if protocol != UniversalProtocolAll && spec.protocol != protocol {
			continue
		}
		routeKey := string(spec.protocol) + "|" + string(spec.modality)
		if seenRoute[routeKey] {
			continue
		}
		selected, err := s.resolveModelShape(ctx, apiKey, groups, model, spec)
		if err != nil {
			return UniversalCapability{}, err
		}
		if selected == nil {
			continue
		}
		group := UniversalSelectedGroup{ID: selected.ID, Name: selected.Name, Platform: selected.Platform}
		capability.Routes = append(capability.Routes, UniversalCapabilityRoute{
			Protocol: spec.protocol,
			Modality: spec.modality,
			Group:    group,
		})
		seenRoute[routeKey] = true
		if !seenProtocol[spec.protocol] {
			capability.Protocols = append(capability.Protocols, spec.protocol)
			seenProtocol[spec.protocol] = true
		}
		if !seenModality[spec.modality] {
			capability.Modalities = append(capability.Modalities, spec.modality)
			seenModality[spec.modality] = true
		}
	}
	if len(capability.Routes) > 0 {
		capability.SelectedGroup = capability.Routes[0].Group
	}
	return capability, nil
}

func (s *UniversalCapabilityService) resolveModelShape(
	ctx context.Context,
	apiKey *APIKey,
	groups []Group,
	model string,
	spec universalCapabilityShape,
) (*Group, error) {
	supportByGroup := make(map[int64]bool, len(groups))
	supportedGroups := make([]Group, 0, len(groups))
	for i := range groups {
		supported, err := s.supports(ctx, groups[i].ID, groups[i].Platform, model, spec.shape)
		if err != nil {
			return nil, fmt.Errorf("check capability support for group %d: %w", groups[i].ID, err)
		}
		supportByGroup[groups[i].ID] = supported
		if supported {
			supportedGroups = append(supportedGroups, groups[i])
		}
	}
	if len(supportedGroups) == 0 {
		return nil, nil
	}

	resolver := NewUniversalRoutingResolver(&fixedCapabilityGroups{groups: supportedGroups})
	resolver.SetModelSupportProvider(func(_ context.Context, groupID *int64, _ string, _ string, _ UniversalShape) (bool, bool) {
		if groupID == nil {
			return false, true
		}
		return supportByGroup[*groupID], true
	})
	selected, err := resolver.Resolve(ctx, apiKey, spec.shape, model, spec.forcedPlatform)
	if errors.Is(err, ErrUniversalNoEntitledGroup) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: resolve %s for %s: %v", ErrUniversalCapabilityUnavailable, model, spec.protocol, err)
	}
	return selected, nil
}

type fixedCapabilityGroups struct {
	groups []Group
}

func (s *fixedCapabilityGroups) GetAvailableGroups(context.Context, int64) ([]Group, error) {
	return s.groups, nil
}

// GetAvailableModelsForDiscovery preserves the distinction that the legacy
// []string API cannot express: native passthrough and wildcard mappings require
// expansion against the client-facing catalog, while an empty pool does not.
func (s *GatewayService) GetAvailableModelsForDiscovery(ctx context.Context, groupID int64, platform string) ([]string, bool, error) {
	if s == nil || s.accountRepo == nil {
		return nil, false, ErrUniversalCapabilityUnavailable
	}
	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, groupID)
	if err != nil {
		return nil, false, err
	}
	filtered := make([]Account, 0, len(accounts))
	useMixed := platform == PlatformAnthropic || platform == PlatformGemini
	for i := range accounts {
		if s.isAccountAllowedForPlatform(&accounts[i], platform, useMixed) {
			filtered = append(filtered, accounts[i])
		}
	}
	if len(filtered) == 0 {
		return []string{}, false, nil
	}

	modelSet := make(map[string]struct{})
	needsCatalogFallback := false
	for i := range filtered {
		mapping := filtered[i].GetModelMapping()
		if len(mapping) == 0 {
			if platform != PlatformNewAPI {
				needsCatalogFallback = true
			}
			continue
		}
		for model := range mapping {
			model = strings.TrimSpace(model)
			if strings.HasSuffix(model, "*") {
				needsCatalogFallback = true
				continue
			}
			if model != "" {
				modelSet[model] = struct{}{}
			}
		}
	}
	mergeGrokNativeCatalogModels(platform, modelSet)
	if len(modelSet) == 0 {
		return []string{}, needsCatalogFallback, nil
	}
	ids := make([]string, 0, len(modelSet))
	for model := range modelSet {
		ids = append(ids, model)
	}
	sort.Strings(ids)
	return ids, needsCatalogFallback, nil
}
