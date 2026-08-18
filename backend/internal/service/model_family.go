package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const modelFamilyRulesSchemaVersion = 1

type ModelFamily string

type ModelFamilyRule struct {
	Family   ModelFamily `json:"family"`
	Prefixes []string    `json:"prefixes"`
}

type ModelFamilyRulesArtifact struct {
	SchemaVersion int               `json:"schema_version"`
	Rules         []ModelFamilyRule `json:"rules"`
	Checksum      string            `json:"checksum_sha256"`
}

var modelFamilyRules = []ModelFamilyRule{
	{Family: "claude", Prefixes: []string{"claude-"}},
	{Family: "gpt", Prefixes: []string{"gpt-", "o1", "o3", "o4"}},
	{Family: "gemini", Prefixes: []string{"gemini-"}},
	{Family: "grok", Prefixes: []string{"grok-"}},
	{Family: "deepseek", Prefixes: []string{"deepseek-"}},
	{Family: "qwen", Prefixes: []string{"qwen"}},
	{Family: "glm", Prefixes: []string{"glm-"}},
	{Family: "minimax", Prefixes: []string{"minimax-"}},
}

var modelProviderQualifiers = []string{
	"amazon.",
	"anthropic.",
	"google.",
	"openai.",
	"xai.",
}

func DetectModelFamily(model string) ModelFamily {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, qualifier := range modelProviderQualifiers {
		if strings.HasPrefix(normalized, qualifier) {
			normalized = strings.TrimPrefix(normalized, qualifier)
			break
		}
	}
	for _, rule := range modelFamilyRules {
		for _, prefix := range rule.Prefixes {
			if strings.HasPrefix(normalized, prefix) {
				return rule.Family
			}
		}
	}
	return ""
}

func ExportModelFamilyRules() ModelFamilyRulesArtifact {
	artifact := ModelFamilyRulesArtifact{
		SchemaVersion: modelFamilyRulesSchemaVersion,
		Rules:         cloneModelFamilyRules(modelFamilyRules),
	}
	artifact.Checksum = modelFamilyRulesChecksum(artifact.SchemaVersion, artifact.Rules)
	return artifact
}

func VerifyModelFamilyRulesArtifact(data []byte) bool {
	var artifact ModelFamilyRulesArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return false
	}
	if artifact.SchemaVersion != modelFamilyRulesSchemaVersion || len(artifact.Rules) == 0 {
		return false
	}
	return artifact.Checksum == modelFamilyRulesChecksum(artifact.SchemaVersion, artifact.Rules)
}

func modelFamilyRulesChecksum(schemaVersion int, rules []ModelFamilyRule) string {
	payload, err := json.Marshal(struct {
		SchemaVersion int               `json:"schema_version"`
		Rules         []ModelFamilyRule `json:"rules"`
	}{SchemaVersion: schemaVersion, Rules: rules})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func cloneModelFamilyRules(rules []ModelFamilyRule) []ModelFamilyRule {
	cloned := make([]ModelFamilyRule, len(rules))
	for i, rule := range rules {
		cloned[i] = ModelFamilyRule{
			Family:   rule.Family,
			Prefixes: append([]string(nil), rule.Prefixes...),
		}
	}
	return cloned
}
