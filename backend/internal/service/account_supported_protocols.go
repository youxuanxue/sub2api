package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
)

const SupportedProtocolsExtraKey = "supported_protocols"

type supportedProtocolsExtraWriter interface {
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

func NormalizeSupportedProtocols(protocols []protocolrouter.Protocol) ([]protocolrouter.Protocol, error) {
	set := make(map[protocolrouter.Protocol]struct{}, len(protocols))
	for _, protocol := range protocols {
		if !protocol.Valid() {
			return nil, fmt.Errorf("invalid supported protocol %q", protocol)
		}
		set[protocol] = struct{}{}
	}
	result := make([]protocolrouter.Protocol, 0, len(set))
	for _, protocol := range protocolrouter.AllProtocols() {
		if _, ok := set[protocol]; ok {
			result = append(result, protocol)
		}
	}
	return result, nil
}

func (a *Account) SupportedProtocols() []protocolrouter.Protocol {
	if a == nil || a.ProtocolEndpointCapability == nil {
		return nil
	}
	normalized, err := NormalizeSupportedProtocols(a.ProtocolEndpointCapability.SupportedProtocols)
	if err != nil {
		return nil
	}
	return append([]protocolrouter.Protocol(nil), normalized...)
}

func ReplaceSupportedProtocols(
	ctx context.Context,
	writer supportedProtocolsExtraWriter,
	accountID int64,
	protocols []protocolrouter.Protocol,
) error {
	if writer == nil {
		return errors.New("supported protocols writer is required")
	}
	if accountID <= 0 {
		return errors.New("account id must be positive")
	}
	update, err := BuildSupportedProtocolsUpdate(protocols)
	if err != nil {
		return err
	}
	return writer.UpdateExtra(ctx, accountID, update)
}

func BuildSupportedProtocolsUpdate(protocols []protocolrouter.Protocol) (map[string]any, error) {
	normalized, err := NormalizeSupportedProtocols(protocols)
	if err != nil {
		return nil, err
	}
	persisted := make([]string, len(normalized))
	for i, protocol := range normalized {
		persisted[i] = string(protocol)
	}
	return map[string]any{
		SupportedProtocolsExtraKey: persisted,
	}, nil
}

// LegacySupportedProtocolsProjection decodes the previous image's rollback
// field for one-time migration seeding only. Runtime routing, APIs, and UI must
// use Account.SupportedProtocols, which reads the linked capability row.
func LegacySupportedProtocolsProjection(account *Account) []protocolrouter.Protocol {
	if account == nil || account.Extra == nil {
		return nil
	}
	raw, ok := account.Extra[SupportedProtocolsExtraKey]
	if !ok {
		return nil
	}
	protocols := make([]protocolrouter.Protocol, 0)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				protocols = append(protocols, protocolrouter.Protocol(strings.TrimSpace(text)))
			}
		}
	case []string:
		for _, value := range values {
			protocols = append(protocols, protocolrouter.Protocol(strings.TrimSpace(value)))
		}
	case []protocolrouter.Protocol:
		protocols = append(protocols, values...)
	}
	normalized, err := NormalizeSupportedProtocols(protocols)
	if err != nil {
		return nil
	}
	return normalized
}

func applySupportedProtocolsUpdate(account *Account, update map[string]any) {
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[SupportedProtocolsExtraKey] = update[SupportedProtocolsExtraKey]
}

func SeedOfficialSupportedProtocols(account *Account) bool {
	if account == nil {
		return false
	}
	if account.ProtocolEndpointCapability != nil {
		return false
	}
	protocols := officialSupportedProtocols(account)
	if len(protocols) == 0 {
		return false
	}
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		return false
	}
	id := account.ID
	if id <= 0 {
		id = 1
	}
	account.ProtocolEndpointCapabilityID = &id
	account.ProtocolEndpointCapability = &ProtocolEndpointCapability{
		ID:                 id,
		CapabilityKey:      identity.Key(),
		Identity:           identity,
		SupportedProtocols: append([]protocolrouter.Protocol(nil), protocols...),
		ProbeEvidence: ProtocolProbeEvidence{
			InitialProbeCompleted: true,
			OfficialSeed:          true,
		},
		Revision: 1,
	}
	return true
}

// routingSupportedProtocols is the Plan() view of capability.supported_protocols.
// Prod OpenAI edge-mirror stubs (api-*.tokenkey.dev) accept /v1/messages as
// TokenKey ingress and convert simple probes, but the next hop's OpenAI OAuth
// pool only speaks responses. Treating probe-positive messages as identity
// dumps Claude Code bodies onto the edge, which then fail-closes to 503/502.
func routingSupportedProtocols(account *Account) []protocolrouter.Protocol {
	protocols := account.SupportedProtocols()
	if !tkIsOpenAIEdgeMirrorStub(account) {
		return protocols
	}
	filtered := make([]protocolrouter.Protocol, 0, len(protocols))
	hasOpenAINative := false
	for _, protocol := range protocols {
		if protocol == protocolrouter.ProtocolMessages {
			continue
		}
		filtered = append(filtered, protocol)
		if protocol == protocolrouter.ProtocolResponses || protocol == protocolrouter.ProtocolChatCompletions {
			hasOpenAINative = true
		}
	}
	if !hasOpenAINative {
		return protocols
	}
	return filtered
}

func ProtocolAccountSnapshot(account *Account, requestedModel string) (protocolrouter.AccountSnapshot, error) {
	return protocolAccountSnapshot(account, requestedModel, false, false, nil)
}

func protocolAccountSnapshotForRequest(account *Account, request protocolrouter.CanonicalRequest) (protocolrouter.AccountSnapshot, error) {
	return protocolAccountSnapshotForRequestWithThinking(account, request, nil)
}

func protocolAccountSnapshotForRequestWithThinking(
	account *Account,
	request protocolrouter.CanonicalRequest,
	thinkingEnabled *bool,
) (protocolrouter.AccountSnapshot, error) {
	requireCompact := request.InboundProtocol() == protocolrouter.ProtocolResponses && request.ResponsesPath() == protocolrouter.ResponsesPathCompact
	return protocolAccountSnapshot(account, request.RequestedModel(), requireCompact, request.Profile().Stream, thinkingEnabled)
}

func protocolAccountSnapshot(account *Account, requestedModel string, requireCompact bool, stream bool, thinkingEnabled *bool) (protocolrouter.AccountSnapshot, error) {
	if account == nil {
		return protocolrouter.AccountSnapshot{}, errors.New("account is required")
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return protocolrouter.AccountSnapshot{}, errors.New("requested model is required")
	}
	capability := account.ProtocolEndpointCapability
	if capability == nil || account.ProtocolEndpointCapabilityID == nil || capability.ID != *account.ProtocolEndpointCapabilityID {
		return protocolrouter.AccountSnapshot{}, errors.New("governed account is missing protocol endpoint capability link")
	}
	if capability.CapabilityKey == "" || capability.Revision <= 0 ||
		!protocolCapabilityHasVerifiedRoutingEvidence(capability) {
		return protocolrouter.AccountSnapshot{}, errors.New("protocol endpoint capability is invalid or conflicted")
	}
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil {
		return protocolrouter.AccountSnapshot{}, err
	}
	if !governed || identity.Key() != capability.CapabilityKey {
		return protocolrouter.AccountSnapshot{}, errors.New("account endpoint identity does not match linked capability")
	}
	protocols := routingSupportedProtocols(account)
	resolvedModel := protocolResolvedUpstreamModel(account, requestedModel, requireCompact)
	customBaseURL, customBaseURLs, officialProfile := protocolAccountEndpoints(account)
	geminiProfile := protocolGeminiEndpointProfile(account)
	modelAllowed := make(map[protocolrouter.Protocol]bool, len(protocols))
	for _, protocol := range protocols {
		modelAllowed[protocol] = protocolResolvedModelAllowedForTarget(
			account,
			officialProfile,
			protocol,
			requestedModel,
			resolvedModel,
			thinkingEnabled,
		)
	}
	exactEndpoints, err := protocolExactEndpoints(account, resolvedModel, geminiProfile, stream)
	if err != nil {
		return protocolrouter.AccountSnapshot{}, err
	}
	protocols = retainResolvedNewAPIExactProtocols(account, protocols, exactEndpoints)
	return protocolrouter.NewAccountSnapshot(protocolrouter.AccountSnapshotInput{
		AccountID:          account.ID,
		CapabilityKey:      capability.CapabilityKey,
		SupportedProtocols: protocols,
		ResolvedModel:      resolvedModel,
		CustomBaseURL:      customBaseURL,
		CustomBaseURLs:     customBaseURLs,
		ExactEndpoints:     exactEndpoints,
		OfficialProfile:    officialProfile,
		GeminiProfile:      geminiProfile,
		ModelAllowed:       modelAllowed,
		Transports:         []protocolrouter.TransportID{protocolrouter.TransportHTTP},
	})
}

func protocolResolvedModelAllowedForTarget(
	account *Account,
	profile protocolrouter.OfficialEndpointProfile,
	target protocolrouter.Protocol,
	requestedModel string,
	resolvedModel string,
	thinkingEnabled *bool,
) bool {
	if account == nil || strings.TrimSpace(resolvedModel) == "" {
		return false
	}
	if !accountAdmitsRequestedModel(account, requestedModel, thinkingEnabled) {
		return false
	}
	switch profile {
	case protocolrouter.OfficialEndpointOpenAICodex:
		return target == protocolrouter.ProtocolResponses && isOpenAIOAuthServableModel(resolvedModel)
	case protocolrouter.OfficialEndpointAnthropic:
		return target == protocolrouter.ProtocolMessages
	}
	if target == protocolrouter.ProtocolGeminiGenerateContent {
		return protocolGeminiEndpointProfile(account).Valid()
	}
	if _, matched := account.ResolveMappedModel(requestedModel); matched {
		return true
	}
	return account.IsModelSupported(resolvedModel)
}

func protocolResolvedUpstreamModel(account *Account, requestedModel string, requireCompact bool) string {
	if account == nil {
		return strings.TrimSpace(requestedModel)
	}
	switch account.Platform {
	case PlatformOpenAI, PlatformNewAPI, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek:
		if resolved := resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact); strings.TrimSpace(resolved) != "" {
			return strings.TrimSpace(resolved)
		}
	case PlatformAnthropic:
		resolved := account.GetMappedModel(requestedModel)
		if account.Type != AccountTypeAPIKey {
			resolved = claude.NormalizeModelID(resolved)
		}
		return strings.TrimSpace(resolved)
	}
	return strings.TrimSpace(account.GetMappedModel(requestedModel))
}

func protocolExactEndpoints(
	account *Account,
	resolvedModel string,
	geminiProfile protocolrouter.GeminiEndpointProfile,
	stream bool,
) (map[protocolrouter.Protocol]string, error) {
	if geminiProfile.Valid() {
		endpoint, err := protocolGeminiExactEndpoint(account, resolvedModel, geminiProfile, stream)
		if err != nil {
			return nil, err
		}
		return map[protocolrouter.Protocol]string{
			protocolrouter.ProtocolGeminiGenerateContent: endpoint,
		}, nil
	}
	if account == nil || account.Platform != PlatformNewAPI || account.ChannelType <= 0 {
		return nil, nil
	}
	// Only resolve protocols the linked capability already claims. Qianfan
	// (channel 46) rejects Responses; forcing that URL used to poison
	// chat-only snapshots. A claimed protocol that cannot resolve is omitted
	// so a remaining legal Chat route still plans.
	endpoints := make(map[protocolrouter.Protocol]string, 2)
	var firstErr error
	for _, protocol := range routingSupportedProtocols(account) {
		switch protocol {
		case protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses:
		default:
			continue
		}
		endpoint, err := protocolExactEndpoint(account, protocol, resolvedModel)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if strings.TrimSpace(endpoint) == "" {
			continue
		}
		endpoints[protocol] = endpoint
	}
	if len(endpoints) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return endpoints, nil
}

// retainResolvedNewAPIExactProtocols drops Chat/Responses that the channel
// adaptor could not resolve. Leaving them in the Plan-facing set lets
// resolveEndpoint invent /v1/responses from customBaseURL.
func retainResolvedNewAPIExactProtocols(
	account *Account,
	protocols []protocolrouter.Protocol,
	exactEndpoints map[protocolrouter.Protocol]string,
) []protocolrouter.Protocol {
	if account == nil || account.Platform != PlatformNewAPI || account.ChannelType <= 0 {
		return protocols
	}
	kept := make([]protocolrouter.Protocol, 0, len(protocols))
	for _, protocol := range protocols {
		switch protocol {
		case protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses:
			if strings.TrimSpace(exactEndpoints[protocol]) == "" {
				continue
			}
		}
		kept = append(kept, protocol)
	}
	return kept
}

func protocolGeminiEndpointProfile(account *Account) protocolrouter.GeminiEndpointProfile {
	if account == nil {
		return protocolrouter.GeminiEndpointNone
	}
	if account.Platform == PlatformAntigravity && account.Type == AccountTypeOAuth {
		return protocolrouter.GeminiEndpointAntigravityCloudCode
	}
	if account.IsNewAPIVertexServiceAccount() {
		return protocolrouter.GeminiEndpointVertexServiceAccount
	}
	return protocolrouter.GeminiEndpointNone
}

func protocolGeminiExactEndpoint(
	account *Account,
	resolvedModel string,
	profile protocolrouter.GeminiEndpointProfile,
	stream bool,
) (string, error) {
	switch profile {
	case protocolrouter.GeminiEndpointAntigravityCloudCode:
		return strings.TrimRight(resolveAntigravityForwardBaseURL(account), "/") + "/v1internal:streamGenerateContent", nil
	case protocolrouter.GeminiEndpointVertexServiceAccount:
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		return buildVertexGeminiURL(account.VertexProjectID(), account.VertexLocation(resolvedModel), resolvedModel, action, false)
	default:
		return "", errors.New("gemini endpoint profile is required")
	}
}

func protocolExactEndpoint(account *Account, protocol protocolrouter.Protocol, resolvedModel string) (string, error) {
	if account == nil || account.Platform != PlatformNewAPI || account.ChannelType <= 0 {
		return "", nil
	}
	var format newapitypes.RelayFormat
	switch protocol {
	case protocolrouter.ProtocolChatCompletions:
		format = newapitypes.RelayFormatOpenAI
	case protocolrouter.ProtocolResponses:
		format = newapitypes.RelayFormatOpenAIResponses
	default:
		return "", nil
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		base := strings.TrimRight(newapiintegration.VolcEngineAgentPlanBaseURL, "/")
		if protocol == protocolrouter.ProtocolResponses {
			return base + "/responses", nil
		}
		return base + "/chat/completions", nil
	}
	in := newAPIBridgeChannelInputForModel(account, 0, "", resolvedModel).WithoutModelMapping()
	endpoint, err := bridge.ResolveTextEndpoint(in, format, resolvedModel)
	if err != nil {
		return "", fmt.Errorf("resolve newapi %s endpoint: %w", protocol, err)
	}
	return endpoint, nil
}

func protocolAccountEndpoints(account *Account) (string, map[protocolrouter.Protocol]string, protocolrouter.OfficialEndpointProfile) {
	if account == nil {
		return "", nil, ""
	}
	if account.Platform == PlatformAnthropic && account.IsAnthropicOAuthOrSetupToken() {
		if !account.IsCustomBaseURLEnabled() {
			return "", nil, protocolrouter.OfficialEndpointAnthropic
		}
		if customBaseURL := strings.TrimSpace(account.GetCustomBaseURL()); customBaseURL != "" {
			return "", map[protocolrouter.Protocol]string{
				protocolrouter.ProtocolMessages: customBaseURL,
			}, ""
		}
		return "", nil, ""
	}
	if account.Platform == PlatformOpenAI && account.IsOpenAIOAuthLike() {
		return "", nil, protocolrouter.OfficialEndpointOpenAICodex
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if IsSupplierManagedAccount(account) {
		if account.Platform != PlatformNewAPI || account.Type != AccountTypeAPIKey ||
			account.ChannelType != newapiconstant.ChannelTypeOpenAI || baseURL == "" {
			return "", nil, ""
		}
		return "", map[protocolrouter.Protocol]string{
			protocolrouter.ProtocolChatCompletions: baseURL,
		}, ""
	}
	baseURLs := make(map[protocolrouter.Protocol]string)
	if raw, ok := account.Credentials["api_base_urls"].(map[string]any); ok {
		copyProtocolBaseURL(raw, APIProtocolChatCompletions, protocolrouter.ProtocolChatCompletions, baseURLs)
		copyProtocolBaseURL(raw, APIProtocolResponses, protocolrouter.ProtocolResponses, baseURLs)
		copyProtocolBaseURL(raw, APIProtocolAnthropic, protocolrouter.ProtocolMessages, baseURLs)
	}
	if len(baseURLs) == 0 {
		baseURLs = nil
	}
	return baseURL, baseURLs, ""
}

func copyProtocolBaseURL(raw map[string]any, key string, protocol protocolrouter.Protocol, out map[protocolrouter.Protocol]string) {
	value, _ := raw[key].(string)
	if value = strings.TrimSpace(value); value != "" {
		out[protocol] = value
	}
}
