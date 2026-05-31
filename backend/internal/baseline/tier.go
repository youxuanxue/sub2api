package baseline

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/model"
)

// TierBaselineDoc mirrors anthropic-oauth-stability-baselines-tiered.json.
// Only the fields the backend needs are typed; the rest stay as raw maps so
// future JSON-only additions don't require a Go change to parse.
type TierBaselineDoc struct {
	SchemaVersion int `json:"schema_version"`
	Policy        struct {
		TierOrder []string `json:"tier_order"`
	} `json:"policy"`
	SharedBaseline struct {
		Account     map[string]any `json:"account"`
		Credentials map[string]any `json:"credentials"`
		Extra       map[string]any `json:"extra"`
		TLSProfile  tlsProfileSpec `json:"tls_profile"`
	} `json:"shared_baseline"`
	Tiers map[string]struct {
		Baseline struct {
			Account struct {
				Concurrency    int     `json:"concurrency"`
				Priority       int     `json:"priority"`
				RateMultiplier float64 `json:"rate_multiplier"`
			} `json:"account"`
			Extra map[string]any `json:"extra"`
		} `json:"baseline"`
	} `json:"tiers"`
}

// tlsProfileSpec is the shared_baseline.tls_profile block. JSON numbers unmarshal
// directly into []uint16 (values are all within uint16 range by construction).
type tlsProfileSpec struct {
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	EnableGREASE        bool     `json:"enable_grease"`
	CipherSuites        []uint16 `json:"cipher_suites"`
	Curves              []uint16 `json:"curves"`
	PointFormats        []uint16 `json:"point_formats"`
	SignatureAlgorithms []uint16 `json:"signature_algorithms"`
	ALPNProtocols       []string `json:"alpn_protocols"`
	SupportedVersions   []uint16 `json:"supported_versions"`
	KeyShareGroups      []uint16 `json:"key_share_groups"`
	PSKModes            []uint16 `json:"psk_modes"`
	Extensions          []uint16 `json:"extensions"`
}

// EffectiveTierBaseline is the merged (shared_baseline + per-tier) result that the
// ApplyTier service writes onto an account. Extra/Credentials are fresh copies the
// caller may mutate; concurrency/priority/rate_multiplier come from the tier.
type EffectiveTierBaseline struct {
	Tier           string
	Concurrency    int
	Priority       int
	RateMultiplier float64
	// Extra is shared_baseline.extra overlaid with tiers[tier].baseline.extra.
	Extra map[string]any
	// Credentials is shared_baseline.credentials (e.g. temp_unschedulable_rules).
	Credentials map[string]any
	// TLSProfileName is the canonical profile name accounts must bind to.
	TLSProfileName string
	// tlsProfile is the raw spec used to build the model on demand.
	tlsProfile tlsProfileSpec
}

var (
	tierDocOnce sync.Once
	tierDoc     *TierBaselineDoc
	tierDocErr  error
)

// LoadTierBaselineDoc parses the embedded tier baseline JSON once and caches it.
func LoadTierBaselineDoc() (*TierBaselineDoc, error) {
	tierDocOnce.Do(func() {
		doc := &TierBaselineDoc{}
		if err := json.Unmarshal(tierBaselineJSON, doc); err != nil {
			tierDocErr = fmt.Errorf("parse embedded tier baseline: %w", err)
			return
		}
		if len(doc.Tiers) == 0 {
			tierDocErr = fmt.Errorf("embedded tier baseline has no tiers")
			return
		}
		if strings.TrimSpace(doc.SharedBaseline.TLSProfile.Name) == "" {
			tierDocErr = fmt.Errorf("embedded tier baseline shared_baseline.tls_profile.name is empty")
			return
		}
		tierDoc = doc
	})
	return tierDoc, tierDocErr
}

// TierOrder returns the ordered tier ids (l1..l5) from the embedded baseline.
func TierOrder() ([]string, error) {
	doc, err := LoadTierBaselineDoc()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(doc.Policy.TierOrder))
	copy(out, doc.Policy.TierOrder)
	return out, nil
}

// EffectiveBaselineForTier merges shared_baseline with the given tier's baseline.
// The tier id is matched case-insensitively against the JSON keys.
func EffectiveBaselineForTier(tier string) (*EffectiveTierBaseline, error) {
	doc, err := LoadTierBaselineDoc()
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(strings.TrimSpace(tier))
	t, ok := doc.Tiers[key]
	if !ok {
		return nil, fmt.Errorf("unknown tier %q (known: %s)", tier, strings.Join(doc.Policy.TierOrder, ", "))
	}

	// extra = copy(shared.extra) overlaid with tier.extra
	extra := make(map[string]any, len(doc.SharedBaseline.Extra)+len(t.Baseline.Extra))
	for k, v := range doc.SharedBaseline.Extra {
		extra[k] = v
	}
	for k, v := range t.Baseline.Extra {
		extra[k] = v
	}

	creds := make(map[string]any, len(doc.SharedBaseline.Credentials))
	for k, v := range doc.SharedBaseline.Credentials {
		creds[k] = v
	}

	return &EffectiveTierBaseline{
		Tier:           key,
		Concurrency:    t.Baseline.Account.Concurrency,
		Priority:       t.Baseline.Account.Priority,
		RateMultiplier: t.Baseline.Account.RateMultiplier,
		Extra:          extra,
		Credentials:    creds,
		TLSProfileName: doc.SharedBaseline.TLSProfile.Name,
		tlsProfile:     doc.SharedBaseline.TLSProfile,
	}, nil
}

// CanonicalTLSProfile builds the model.TLSFingerprintProfile for the canonical
// profile referenced by the tier baseline (name = tk_canonical_cc_oauth). The
// returned profile carries no ID; the caller upserts by name and reads back the id.
func (e *EffectiveTierBaseline) CanonicalTLSProfile() *model.TLSFingerprintProfile {
	p := e.tlsProfile
	var desc *string
	if strings.TrimSpace(p.Description) != "" {
		d := p.Description
		desc = &d
	}
	return &model.TLSFingerprintProfile{
		Name:                p.Name,
		Description:         desc,
		EnableGREASE:        p.EnableGREASE,
		CipherSuites:        append([]uint16(nil), p.CipherSuites...),
		Curves:              append([]uint16(nil), p.Curves...),
		PointFormats:        append([]uint16(nil), p.PointFormats...),
		SignatureAlgorithms: append([]uint16(nil), p.SignatureAlgorithms...),
		ALPNProtocols:       append([]string(nil), p.ALPNProtocols...),
		SupportedVersions:   append([]uint16(nil), p.SupportedVersions...),
		KeyShareGroups:      append([]uint16(nil), p.KeyShareGroups...),
		PSKModes:            append([]uint16(nil), p.PSKModes...),
		Extensions:          append([]uint16(nil), p.Extensions...),
	}
}
