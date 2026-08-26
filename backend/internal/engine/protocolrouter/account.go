package protocolrouter

import (
	"errors"
	"fmt"
	"strings"
)

type OfficialEndpointProfile string

const (
	OfficialEndpointAnthropic   OfficialEndpointProfile = "anthropic_official"
	OfficialEndpointOpenAI      OfficialEndpointProfile = "openai_official"
	OfficialEndpointOpenAICodex OfficialEndpointProfile = "openai_codex_official"
)

type GeminiEndpointProfile string

const (
	GeminiEndpointNone                 GeminiEndpointProfile = ""
	GeminiEndpointAntigravityCloudCode GeminiEndpointProfile = "antigravity_cloudcode"
	GeminiEndpointVertexServiceAccount GeminiEndpointProfile = "vertex_service_account"
)

func (p GeminiEndpointProfile) Valid() bool {
	switch p {
	case GeminiEndpointAntigravityCloudCode, GeminiEndpointVertexServiceAccount:
		return true
	default:
		return false
	}
}

type TransportID string

const TransportHTTP TransportID = "http"

type AccountSnapshotInput struct {
	AccountID          int64
	Revision           string
	SupportedProtocols []Protocol
	ResolvedModel      string
	CustomBaseURL      string
	CustomBaseURLs     map[Protocol]string
	ExactEndpoints     map[Protocol]string
	OfficialProfile    OfficialEndpointProfile
	GeminiProfile      GeminiEndpointProfile
	ModelAllowed       map[Protocol]bool
	Transports         []TransportID
}

type AccountSnapshot struct {
	accountID          int64
	revision           string
	supportedProtocols map[Protocol]struct{}
	resolvedModel      string
	customBaseURL      string
	customBaseURLs     map[Protocol]string
	exactEndpoints     map[Protocol]string
	officialProfile    OfficialEndpointProfile
	geminiProfile      GeminiEndpointProfile
	modelAllowed       map[Protocol]bool
	transports         map[TransportID]struct{}
}

func NewAccountSnapshot(input AccountSnapshotInput) (AccountSnapshot, error) {
	if input.AccountID <= 0 {
		return AccountSnapshot{}, errors.New("account id must be positive")
	}
	revision := strings.TrimSpace(input.Revision)
	if revision == "" {
		return AccountSnapshot{}, errors.New("account revision is required")
	}
	model := strings.TrimSpace(input.ResolvedModel)
	if model == "" {
		return AccountSnapshot{}, errors.New("resolved model is required")
	}
	supported := make(map[Protocol]struct{}, len(input.SupportedProtocols))
	for _, protocol := range input.SupportedProtocols {
		if !protocol.Valid() {
			return AccountSnapshot{}, fmt.Errorf("invalid supported protocol %q", protocol)
		}
		supported[protocol] = struct{}{}
	}
	modelAllowed := make(map[Protocol]bool, len(input.ModelAllowed))
	for protocol, allowed := range input.ModelAllowed {
		if !protocol.Valid() {
			return AccountSnapshot{}, fmt.Errorf("invalid model policy protocol %q", protocol)
		}
		modelAllowed[protocol] = allowed
	}
	transports := make(map[TransportID]struct{}, len(input.Transports))
	for _, transport := range input.Transports {
		if strings.TrimSpace(string(transport)) == "" {
			return AccountSnapshot{}, errors.New("transport id is required")
		}
		transports[transport] = struct{}{}
	}
	customBaseURLs := make(map[Protocol]string, len(input.CustomBaseURLs))
	for protocol, baseURL := range input.CustomBaseURLs {
		if !protocol.Valid() {
			return AccountSnapshot{}, fmt.Errorf("invalid endpoint protocol %q", protocol)
		}
		if trimmed := strings.TrimSpace(baseURL); trimmed != "" {
			customBaseURLs[protocol] = trimmed
		}
	}
	exactEndpoints := make(map[Protocol]string, len(input.ExactEndpoints))
	for protocol, endpoint := range input.ExactEndpoints {
		if !protocol.Valid() {
			return AccountSnapshot{}, fmt.Errorf("invalid exact endpoint protocol %q", protocol)
		}
		if trimmed := strings.TrimSpace(endpoint); trimmed != "" {
			exactEndpoints[protocol] = trimmed
		}
	}
	if input.GeminiProfile != GeminiEndpointNone && !input.GeminiProfile.Valid() {
		return AccountSnapshot{}, fmt.Errorf("invalid Gemini endpoint profile %q", input.GeminiProfile)
	}
	return AccountSnapshot{
		accountID:          input.AccountID,
		revision:           revision,
		supportedProtocols: supported,
		resolvedModel:      model,
		customBaseURL:      strings.TrimSpace(input.CustomBaseURL),
		customBaseURLs:     customBaseURLs,
		exactEndpoints:     exactEndpoints,
		officialProfile:    input.OfficialProfile,
		geminiProfile:      input.GeminiProfile,
		modelAllowed:       modelAllowed,
		transports:         transports,
	}, nil
}

func (a AccountSnapshot) AccountID() int64                     { return a.accountID }
func (a AccountSnapshot) Revision() string                     { return a.revision }
func (a AccountSnapshot) ResolvedModel() string                { return a.resolvedModel }
func (a AccountSnapshot) GeminiProfile() GeminiEndpointProfile { return a.geminiProfile }

func (a AccountSnapshot) supports(protocol Protocol) bool {
	_, ok := a.supportedProtocols[protocol]
	return ok
}

func (a AccountSnapshot) permitsModel(protocol Protocol) bool {
	return a.modelAllowed[protocol]
}

func (a AccountSnapshot) hasTransport(transport TransportID) bool {
	_, ok := a.transports[transport]
	return ok
}
