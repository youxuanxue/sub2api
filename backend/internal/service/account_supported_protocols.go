package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
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
	if a == nil || a.Extra == nil {
		return nil
	}
	raw, ok := a.Extra[SupportedProtocolsExtraKey]
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
	default:
		return nil
	}
	normalized, err := NormalizeSupportedProtocols(protocols)
	if err != nil {
		return nil
	}
	return normalized
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
	if account.Extra != nil {
		if _, exists := account.Extra[SupportedProtocolsExtraKey]; exists {
			return false
		}
	}
	protocols := officialSupportedProtocols(account)
	if len(protocols) == 0 {
		return false
	}
	update, err := BuildSupportedProtocolsUpdate(protocols)
	if err != nil {
		return false
	}
	applySupportedProtocolsUpdate(account, update)
	return true
}

func ProtocolAccountSnapshot(account *Account, requestedModel string) (protocolrouter.AccountSnapshot, error) {
	return protocolAccountSnapshot(account, requestedModel, false)
}

func protocolAccountSnapshotForRequest(account *Account, request protocolrouter.CanonicalRequest) (protocolrouter.AccountSnapshot, error) {
	requireCompact := request.InboundProtocol() == protocolrouter.ProtocolResponses && request.ResponsesPath() == protocolrouter.ResponsesPathCompact
	return protocolAccountSnapshot(account, request.RequestedModel(), requireCompact)
}

func protocolAccountSnapshot(account *Account, requestedModel string, requireCompact bool) (protocolrouter.AccountSnapshot, error) {
	if account == nil {
		return protocolrouter.AccountSnapshot{}, errors.New("account is required")
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return protocolrouter.AccountSnapshot{}, errors.New("requested model is required")
	}
	protocols := account.SupportedProtocols()
	resolvedModel := protocolResolvedUpstreamModel(account, requestedModel, requireCompact)
	customBaseURL, customBaseURLs, officialProfile := protocolAccountEndpoints(account)
	modelAllowed := make(map[protocolrouter.Protocol]bool, len(protocols))
	for _, protocol := range protocols {
		modelAllowed[protocol] = protocolResolvedModelAllowedForTarget(
			account,
			officialProfile,
			protocol,
			requestedModel,
			resolvedModel,
		)
	}
	exactEndpoints, err := protocolExactEndpoints(account, resolvedModel)
	if err != nil {
		return protocolrouter.AccountSnapshot{}, err
	}
	revision, err := protocolAccountRevision(account)
	if err != nil {
		return protocolrouter.AccountSnapshot{}, err
	}
	return protocolrouter.NewAccountSnapshot(protocolrouter.AccountSnapshotInput{
		AccountID:          account.ID,
		Revision:           revision,
		SupportedProtocols: protocols,
		ResolvedModel:      resolvedModel,
		CustomBaseURL:      customBaseURL,
		CustomBaseURLs:     customBaseURLs,
		ExactEndpoints:     exactEndpoints,
		OfficialProfile:    officialProfile,
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
) bool {
	if account == nil || strings.TrimSpace(resolvedModel) == "" {
		return false
	}
	switch profile {
	case protocolrouter.OfficialEndpointOpenAICodex:
		return target == protocolrouter.ProtocolResponses && isOpenAIOAuthServableModel(resolvedModel)
	case protocolrouter.OfficialEndpointAnthropic:
		return target == protocolrouter.ProtocolMessages && account.IsModelSupported(requestedModel)
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

func protocolExactEndpoints(account *Account, resolvedModel string) (map[protocolrouter.Protocol]string, error) {
	if account == nil || account.Platform != PlatformNewAPI || account.ChannelType <= 0 {
		return nil, nil
	}
	in := newAPIBridgeChannelInputForModel(account, 0, "", resolvedModel).WithoutModelMapping()
	endpoints := make(map[protocolrouter.Protocol]string, 2)
	for protocol, format := range map[protocolrouter.Protocol]newapitypes.RelayFormat{
		protocolrouter.ProtocolChatCompletions: newapitypes.RelayFormatOpenAI,
		protocolrouter.ProtocolResponses:       newapitypes.RelayFormatOpenAIResponses,
	} {
		endpoint, err := bridge.ResolveTextEndpoint(in, format, resolvedModel)
		if err != nil {
			return nil, fmt.Errorf("resolve newapi %s endpoint: %w", protocol, err)
		}
		endpoints[protocol] = endpoint
	}
	return endpoints, nil
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

func protocolAccountRevision(account *Account) (string, error) {
	input := struct {
		ID          int64          `json:"id"`
		Platform    string         `json:"platform"`
		Type        string         `json:"type"`
		ChannelType int            `json:"channel_type"`
		Credentials map[string]any `json:"credentials"`
		Extra       map[string]any `json:"extra"`
		UpdatedAt   int64          `json:"updated_at_unix_nano"`
	}{
		ID:          account.ID,
		Platform:    account.Platform,
		Type:        account.Type,
		ChannelType: account.ChannelType,
		Credentials: account.Credentials,
		Extra:       account.Extra,
		UpdatedAt:   account.UpdatedAt.UTC().UnixNano(),
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal protocol account revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
