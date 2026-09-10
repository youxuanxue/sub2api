package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

func (s *UniversalCapabilityService) listCandidateCapabilities(ctx context.Context, key *APIKey, protocol UniversalProtocol) ([]UniversalCapability, error) {
	capabilities, _, err := s.DiscoverCandidates(ctx, key, protocol)
	return capabilities, err
}

// DiscoverCandidates returns support and its actual accounts for metadata
// adapters that enrich the response from upstream. It never admits payment,
// acquires capacity or binds the key to a billing origin.
func (s *UniversalCapabilityService) DiscoverCandidates(ctx context.Context, key *APIKey, protocol UniversalProtocol) ([]UniversalCapability, []Account, error) {
	if !s.CandidateSchedulingEnabled() || key == nil {
		return nil, nil, ErrUniversalCapabilityUnavailable
	}
	groups, err := s.groupsForKey(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	if len(groups) == 0 {
		return []UniversalCapability{}, nil, nil
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	if key.IsUniversal() {
		ctx = WithUniversalKeyRouting(ctx)
	}
	// Each metadata shape builds its own request. In particular, media shapes
	// must not inherit a text Plan from an earlier ingress context.
	ctx = context.WithValue(ctx, protocolRoutingContextKey{}, false)
	// Metadata projects a representative client request. It does not authenticate
	// an inference request or promise that any current slot or budget is available.
	ctx = SetClaudeCodeClient(ctx, true)
	request := &CandidateRequest{resolver: s.resolver, key: key, groups: groups}
	accounts, err := request.accounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	byPlatform := make(map[string]map[string]struct{})
	needsCatalog := make(map[string]bool)
	forcedPlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string)
	for i := range accounts {
		account := &accounts[i]
		if !candidateDiscoveryPlatformMatches(protocol, account.Platform) || (forcedPlatform != "" && forcedPlatform != account.Platform) {
			continue
		}
		if byPlatform[account.Platform] == nil {
			byPlatform[account.Platform] = make(map[string]struct{})
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 && account.Platform != PlatformNewAPI {
			needsCatalog[account.Platform] = true
		}
		for model := range mapping {
			if strings.Contains(model, "*") {
				needsCatalog[account.Platform] = true
				continue
			}
			byPlatform[account.Platform][model] = struct{}{}
		}
		mergeGrokNativeCatalogModels(account.Platform, byPlatform[account.Platform])
	}
	modelSet := make(map[string]struct{})
	for platform, ids := range byPlatform {
		models := make([]string, 0, len(ids))
		for id := range ids {
			models = append(models, id)
		}
		if needsCatalog[platform] && s.fallback != nil {
			fallback, err := s.fallback(ctx, platform)
			if err != nil {
				return nil, nil, err
			}
			models = append(models, fallback...)
		}
		if s.modelFilter != nil {
			models, err = s.modelFilter.FilterClientFacingStrict(ctx, platform, models)
			if err != nil {
				return nil, nil, err
			}
		}
		for _, id := range models {
			if id = strings.TrimSpace(id); id != "" {
				modelSet[id] = struct{}{}
			}
		}
	}
	if !key.IsUniversal() {
		for _, group := range groups {
			for model := range group.MessagesDispatchModelConfig.ExactModelMappings {
				modelSet[model] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)
	out := make([]UniversalCapability, 0, len(models))
	accountSet := make(map[int64]Account)
	var catalogSupportFailure error
	for _, model := range models {
		capability := UniversalCapability{ID: model}
		var supportFailure error
		seenRoute := make(map[string]bool)
		seenProtocol := make(map[UniversalProtocol]bool)
		seenModality := make(map[UniversalModality]bool)
		for _, spec := range universalCapabilityShapes {
			if protocol != UniversalProtocolAll && protocol != spec.protocol {
				continue
			}
			if forcedPlatform != "" {
				if spec.forcedPlatform != "" && spec.forcedPlatform != forcedPlatform {
					continue
				}
				spec.forcedPlatform = forcedPlatform
			}
			routeKey := string(spec.protocol) + "|" + string(spec.modality)
			if seenRoute[routeKey] {
				continue
			}
			path, body := candidateDiscoveryRequest(model, spec)
			request.shape, request.path, request.model, request.body, request.forcePlatform = spec.shape, path, model, body, spec.forcedPlatform
			requestCtx := s.resolver.WithRequest(ctx, spec.shape, path, model, body)
			var selected *Group
			var failure error
			for i := range accounts {
				var subscriptions, balances []Group
				for j := range groups {
					candidate, err := request.evaluatePath(requestCtx, &accounts[i], &groups[j])
					if err != nil {
						if !candidateIgnorableSupportError(err) {
							failure = fmt.Errorf("account %d group %d: %w", accounts[i].ID, groups[j].ID, err)
						}
						continue
					}
					if candidate == nil {
						continue
					}
					if candidate.group.IsSubscriptionType() {
						subscriptions = append(subscriptions, *candidate.group)
					} else {
						balances = append(balances, *candidate.group)
					}
				}
				for _, origins := range [][]Group{subscriptions, balances} {
					if len(origins) == 0 {
						continue
					}
					gateway := s.resolver.candidateGateway
					origin, err := selectCandidateBillingOrigin(requestCtx, key.UserID, &accounts[i], origins, model, spec.shape, gateway.channelService, gateway.userGroupRateResolver, gateway.accountRepo)
					if err != nil {
						failure = err
						continue
					}
					accountSet[accounts[i].ID] = accounts[i]
					if selected == nil || origin.ID < selected.ID {
						selected = origin
					}
				}
			}
			if selected == nil {
				if failure != nil {
					if !errors.Is(failure, ErrCandidatePolicyConflict) && !errors.Is(failure, ErrProtocolCapabilityUnknown) {
						return nil, nil, fmt.Errorf("discover %s: %w", model, failure)
					}
					// An unverified protocol must not hide a verified route for
					// the same model. Preserve the error if every shape fails.
					supportFailure = failure
				}
				continue
			}
			// Keep the existing response schema: this is an authorization example,
			// never an execution binding or a quote for a future request.
			capability.Routes = append(capability.Routes, UniversalCapabilityRoute{Protocol: spec.protocol, Modality: spec.modality,
				Group: UniversalSelectedGroup{ID: selected.ID, Name: selected.Name, Platform: selected.Platform}})
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
			out = append(out, capability)
		} else if supportFailure != nil && catalogSupportFailure == nil {
			// One unverified model must not hide other verified models. Retain
			// the error when no model has a legal route in this catalog.
			catalogSupportFailure = fmt.Errorf("discover %s: %w", model, supportFailure)
		}
	}
	if len(out) == 0 && catalogSupportFailure != nil {
		return nil, nil, catalogSupportFailure
	}
	supportedAccounts := make([]Account, 0, len(accountSet))
	for _, account := range accountSet {
		supportedAccounts = append(supportedAccounts, account)
	}
	sort.Slice(supportedAccounts, func(i, j int) bool {
		if supportedAccounts[i].Priority != supportedAccounts[j].Priority {
			return supportedAccounts[i].Priority < supportedAccounts[j].Priority
		}
		return supportedAccounts[i].ID < supportedAccounts[j].ID
	})
	return out, supportedAccounts, nil
}

func candidateDiscoveryPlatformMatches(protocol UniversalProtocol, platform string) bool {
	switch protocol {
	case UniversalProtocolCodex:
		return platform == PlatformOpenAI
	case UniversalProtocolAntigravity:
		return platform == PlatformAntigravity
	default:
		return true
	}
}

func candidateDiscoveryRequest(model string, spec universalCapabilityShape) (string, []byte) {
	path := "/v1/chat/completions"
	document := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "Hello"}}}
	switch spec.shape {
	case ShapeOpenAIAudioSpeech:
		path = "/v1/audio/speech"
		document = map[string]any{"model": model, "input": "Hello"}
	case ShapeOpenAIAudioTranscription:
		path = "/v1/audio/transcriptions"
		document = map[string]any{"model": model}
	case ShapeAnthropicMessages:
		path = "/v1/messages"
		document["max_tokens"] = 1
	case ShapeGemini:
		path = "/v1beta/models/" + model + ":generateContent"
		document = map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Hello"}}}}}
	case ShapeOpenAIImages, ShapeOpenAIImagesEdit:
		path = "/v1/images/generations"
		document = map[string]any{"model": model, "prompt": "A landscape"}
	case ShapeOpenAIEmbeddings:
		path = "/v1/embeddings"
		document = map[string]any{"model": model, "input": "Hello"}
	case ShapeOpenAIVideo:
		path = "/v1/videos"
		document = map[string]any{"model": model, "prompt": "A landscape"}
	}
	if spec.protocol == UniversalProtocolCodex {
		path = "/v1/responses"
		document = map[string]any{"model": model, "input": "Hello"}
	}
	body, _ := json.Marshal(document)
	return path, body
}
