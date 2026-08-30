package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

const protocolEndpointCapabilityKeySchemaVersion = 1

type ProtocolEndpoint struct {
	URL        string `json:"url"`
	APIVersion string `json:"api_version"`
}

type ProtocolEndpointIdentity struct {
	KeySchemaVersion       int                                          `json:"key_schema_version"`
	Platform               string                                       `json:"platform"`
	EndpointProfile        string                                       `json:"endpoint_profile"`
	ChannelType            string                                       `json:"channel_type"`
	ProtocolEndpoints      map[protocolrouter.Protocol]ProtocolEndpoint `json:"protocol_endpoints"`
	UpstreamRequestProfile string                                       `json:"upstream_request_profile"`
	RoutingHeaders         map[string]string                            `json:"routing_headers"`
}

type ProtocolProbeEvidence struct {
	InitialProbeCompleted bool           `json:"initial_probe_completed"`
	OfficialSeed          bool           `json:"official_seed"`
	IdentityConflict      bool           `json:"identity_conflict"`
	Verdicts              map[string]any `json:"verdicts,omitempty"`
}

type ProtocolEndpointCapability struct {
	ID                 int64
	CapabilityKey      string
	Identity           ProtocolEndpointIdentity
	SupportedProtocols []protocolrouter.Protocol
	ProbeEvidence      ProtocolProbeEvidence
	Revision           int64
	LastProbedAt       *time.Time
	ProbeLeaseOwner    *string
	ProbeLeaseUntil    *time.Time
	ProbeGeneration    int64
	IdentityConflict   bool
	LinkedAccountCount int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

var (
	ErrProtocolCapabilityNotFound   = errors.New("protocol endpoint capability not found")
	ErrProtocolCapabilityLeaseBusy  = errors.New("protocol endpoint capability probe lease is busy")
	ErrProtocolCapabilityStaleWrite = errors.New("protocol endpoint capability probe write is stale")
)

type ProtocolProbeLease struct {
	CapabilityKey string
	Generation    int64
	Revision      int64
	Owner         string
}

type ProtocolCapabilityMutation struct {
	SupportedProtocols    []protocolrouter.Protocol
	ProbeEvidence         ProtocolProbeEvidence
	InitialProbeCompleted bool
	IdentityConflict      bool
	LastProbedAt          time.Time
}

type ProtocolEndpointCapabilityLinkInput struct {
	Identity      ProtocolEndpointIdentity
	Governed      bool
	SeedProtocols []protocolrouter.Protocol
	OfficialSeed  bool
}

type ProtocolEndpointCapabilityRepository interface {
	EnsureAccountLink(
		ctx context.Context,
		account *Account,
		identity ProtocolEndpointIdentity,
		historicalPositive []protocolrouter.Protocol,
		officialSeed bool,
	) (*ProtocolEndpointCapability, error)
	GetByAccountID(ctx context.Context, accountID int64) (*ProtocolEndpointCapability, error)
	GetByKey(ctx context.Context, capabilityKey string) (*ProtocolEndpointCapability, error)
	ListLinkedAccountIDs(ctx context.Context, capabilityKey string) ([]int64, error)
	AcquireProbeLease(ctx context.Context, capabilityKey, owner string, now time.Time, ttl time.Duration) (ProtocolProbeLease, bool, error)
	CommitProbeResult(ctx context.Context, lease ProtocolProbeLease, mutation ProtocolCapabilityMutation) (*ProtocolEndpointCapability, int, error)
	CommitPreparedProbeResult(ctx context.Context, lease ProtocolProbeLease, mutation ProtocolCapabilityMutation) (*ProtocolEndpointCapability, int, error)
}

func BuildProtocolEndpointCapabilityLinkInput(account *Account) (ProtocolEndpointCapabilityLinkInput, error) {
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		return ProtocolEndpointCapabilityLinkInput{Identity: identity, Governed: governed}, err
	}

	seedProtocols := officialSupportedProtocols(account)
	officialSeed := len(seedProtocols) > 0
	if capability := account.ProtocolEndpointCapability; capability != nil &&
		account.ProtocolEndpointCapabilityID != nil && capability.ID == *account.ProtocolEndpointCapabilityID &&
		capability.CapabilityKey == identity.Key() {
		seedProtocols = append(seedProtocols, capability.SupportedProtocols...)
	}
	normalized, err := NormalizeSupportedProtocols(seedProtocols)
	if err != nil {
		return ProtocolEndpointCapabilityLinkInput{}, err
	}
	return ProtocolEndpointCapabilityLinkInput{
		Identity:      identity,
		Governed:      true,
		SeedProtocols: normalized,
		OfficialSeed:  officialSeed,
	}, nil
}

func (i ProtocolEndpointIdentity) CanonicalJSON() ([]byte, error) {
	if i.KeySchemaVersion != protocolEndpointCapabilityKeySchemaVersion {
		return nil, fmt.Errorf("unsupported protocol endpoint identity schema version %d", i.KeySchemaVersion)
	}
	if strings.TrimSpace(i.Platform) == "" || strings.TrimSpace(i.EndpointProfile) == "" || strings.TrimSpace(i.UpstreamRequestProfile) == "" {
		return nil, errors.New("protocol endpoint identity is incomplete")
	}
	if len(i.ProtocolEndpoints) == 0 {
		return nil, errors.New("protocol endpoint identity requires at least one endpoint")
	}
	return json.Marshal(i)
}

func (i ProtocolEndpointIdentity) Key() string {
	encoded, err := i.CanonicalJSON()
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func BuildProtocolEndpointIdentity(account *Account) (ProtocolEndpointIdentity, bool, error) {
	if !protocolRoutingGovernsAccount(account) {
		return ProtocolEndpointIdentity{}, false, nil
	}
	identity := ProtocolEndpointIdentity{
		KeySchemaVersion:       protocolEndpointCapabilityKeySchemaVersion,
		Platform:               strings.ToLower(strings.TrimSpace(account.Platform)),
		EndpointProfile:        protocolEndpointProfile(account),
		ChannelType:            strconv.Itoa(account.ChannelType),
		ProtocolEndpoints:      make(map[protocolrouter.Protocol]ProtocolEndpoint),
		UpstreamRequestProfile: protocolUpstreamRequestProfile(account),
		RoutingHeaders:         canonicalRoutingHeaders(account),
	}

	baseURL, protocolBaseURLs, officialProfile := protocolAccountEndpoints(account)
	if officialProfile != "" {
		switch officialProfile {
		case protocolrouter.OfficialEndpointAnthropic:
			identity.ProtocolEndpoints[protocolrouter.ProtocolMessages] = ProtocolEndpoint{URL: "https://api.anthropic.com/v1/messages"}
		case protocolrouter.OfficialEndpointOpenAICodex:
			identity.ProtocolEndpoints[protocolrouter.ProtocolResponses] = ProtocolEndpoint{URL: "https://chatgpt.com/backend-api/codex/responses"}
		default:
			return ProtocolEndpointIdentity{}, true, fmt.Errorf("unsupported official endpoint profile %q", officialProfile)
		}
	} else if geminiProfile := protocolGeminiEndpointProfile(account); geminiProfile.Valid() {
		endpoint, err := canonicalGeminiIdentityEndpoint(account, geminiProfile)
		if err != nil {
			return ProtocolEndpointIdentity{}, true, err
		}
		identity.ProtocolEndpoints[protocolrouter.ProtocolGeminiGenerateContent] = endpoint
	} else {
		for _, protocol := range []protocolrouter.Protocol{
			protocolrouter.ProtocolMessages,
			protocolrouter.ProtocolChatCompletions,
			protocolrouter.ProtocolResponses,
		} {
			configured := strings.TrimSpace(protocolBaseURLs[protocol])
			if configured == "" {
				configured = strings.TrimSpace(baseURL)
			}
			if configured == "" {
				continue
			}
			endpointURL, err := normalizeProtocolEndpointURL(configured, protocol)
			if err != nil {
				return ProtocolEndpointIdentity{}, true, fmt.Errorf("normalize %s endpoint identity: %w", protocol, err)
			}
			identity.ProtocolEndpoints[protocol] = ProtocolEndpoint{
				URL:        endpointURL,
				APIVersion: protocolAPIVersion(account, protocol),
			}
		}
	}
	if len(identity.ProtocolEndpoints) == 0 {
		return ProtocolEndpointIdentity{}, true, errors.New("governed account has no explicit protocol endpoint identity")
	}
	if _, err := identity.CanonicalJSON(); err != nil {
		return ProtocolEndpointIdentity{}, true, err
	}
	return identity, true, nil
}

func protocolEndpointProfile(account *Account) string {
	if account == nil {
		return ""
	}
	if account.Platform == PlatformOpenAI && account.IsOpenAIOAuthLike() {
		return "openai_codex_official"
	}
	if account.Platform == PlatformAnthropic && account.IsAnthropicOAuthOrSetupToken() && !account.IsCustomBaseURLEnabled() {
		return "anthropic_official"
	}
	if profile := protocolGeminiEndpointProfile(account); profile.Valid() {
		return string(profile)
	}
	switch account.Type {
	case AccountTypeAPIKey:
		return "custom_api_key"
	case AccountTypeUpstream:
		return "custom_upstream"
	case AccountTypeOAuth, AccountTypeSetupToken:
		return "custom_oauth"
	default:
		return "custom_" + strings.ToLower(strings.TrimSpace(account.Type))
	}
}

func protocolUpstreamRequestProfile(account *Account) string {
	if account == nil {
		return ""
	}
	if account.Platform == PlatformOpenAI && account.IsOpenAIOAuthLike() {
		return "openai_codex_responses_v1"
	}
	if account.Platform == PlatformAnthropic {
		return "anthropic_json_v1"
	}
	if profile := protocolGeminiEndpointProfile(account); profile.Valid() {
		return string(profile)
	}
	if account.IsGrok() {
		return "grok_json_v1"
	}
	return "openai_json_v1"
}

func canonicalRoutingHeaders(account *Account) map[string]string {
	headers := account.GetHeaderOverrides()
	if len(headers) == 0 {
		return map[string]string{}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, strings.ToLower(strings.TrimSpace(name)))
	}
	sort.Strings(names)
	result := make(map[string]string, len(names))
	for _, name := range names {
		if name == "" || isHeaderOverrideBlockedName(name) {
			continue
		}
		result[name] = strings.TrimSpace(headers[name])
	}
	return result
}

func protocolAPIVersion(account *Account, protocol protocolrouter.Protocol) string {
	if account == nil || account.Credentials == nil {
		return ""
	}
	if versions, ok := account.Credentials["api_versions"].(map[string]any); ok {
		if value, ok := versions[string(protocol)].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	if value, ok := account.Credentials["api_version"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func normalizeProtocolEndpointURL(raw string, protocol protocolrouter.Protocol) (string, error) {
	parsed, err := normalizeEndpointIdentityURL(raw)
	if err != nil {
		return "", err
	}
	// Qianfan BaiduV2 chat lives under /v2, not the OpenAI-compat /v1 suffix.
	if strings.EqualFold(parsed.Hostname(), "qianfan.baidubce.com") &&
		protocol == protocolrouter.ProtocolChatCompletions {
		parsed.Path = "/v2/chat/completions"
		return parsed.String(), nil
	}
	endpointPath := ""
	switch protocol {
	case protocolrouter.ProtocolMessages:
		endpointPath = "/v1/messages"
	case protocolrouter.ProtocolChatCompletions:
		endpointPath = "/v1/chat/completions"
	case protocolrouter.ProtocolResponses:
		endpointPath = "/v1/responses"
	default:
		return "", fmt.Errorf("unsupported endpoint identity protocol %q", protocol)
	}
	basePath := strings.TrimSuffix(parsed.Path, "/")
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(endpointPath, "/v1/") {
		endpointPath = strings.TrimPrefix(endpointPath, "/v1")
	}
	parsed.Path = path.Clean(basePath + endpointPath)
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}
	return parsed.String(), nil
}

func normalizeEndpointIdentityURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("endpoint scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("endpoint must not contain credentials or fragments")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port == "" {
		parsed.Host = hostname
	} else {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	query := parsed.Query()
	for name := range query {
		if protocolEndpointCredentialQueryParam(name) {
			query.Del(name)
		}
	}
	parsed.RawQuery = query.Encode()
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Path = path.Clean(parsed.Path)
	return parsed, nil
}

func protocolEndpointCredentialQueryParam(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "auth", "key", "sig":
		return true
	default:
		return isSensitiveKey(name)
	}
}

func canonicalGeminiIdentityEndpoint(account *Account, profile protocolrouter.GeminiEndpointProfile) (ProtocolEndpoint, error) {
	switch profile {
	case protocolrouter.GeminiEndpointAntigravityCloudCode:
		base, err := normalizeEndpointIdentityURL(resolveAntigravityForwardBaseURL(account))
		if err != nil {
			return ProtocolEndpoint{}, err
		}
		base.Path = path.Clean(strings.TrimSuffix(base.Path, "/") + "/v1internal:streamGenerateContent")
		return ProtocolEndpoint{URL: base.String()}, nil
	case protocolrouter.GeminiEndpointVertexServiceAccount:
		projectID := strings.TrimSpace(account.VertexProjectID())
		if projectID == "" {
			return ProtocolEndpoint{}, errors.New("vertex project id is required for endpoint identity")
		}
		location := strings.TrimSpace(account.GetCredential("location"))
		if location == "" {
			location = strings.TrimSpace(account.GetCredential("vertex_location"))
		}
		if location == "" {
			location = "us-central1"
		}
		return ProtocolEndpoint{
			URL:        fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/{model}:{action}", strings.ToLower(location), url.PathEscape(projectID), url.PathEscape(location)),
			APIVersion: "v1",
		}, nil
	default:
		return ProtocolEndpoint{}, fmt.Errorf("unsupported Gemini endpoint profile %q", profile)
	}
}
