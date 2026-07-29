package service

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed tk_messages_dispatch_family_registry.json
var tkMessagesDispatchFamilyRegistryRaw []byte

type tkMessagesDispatchTierDefaults struct {
	OpusMappedModel   string `json:"opus_mapped_model"`
	SonnetMappedModel string `json:"sonnet_mapped_model"`
	HaikuMappedModel  string `json:"haiku_mapped_model"`
}

type tkMessagesDispatchFamilyRegistry struct {
	PlatformDefaults map[string]tkMessagesDispatchTierDefaults `json:"platform_defaults"`
	GroupDefaults    map[string]tkMessagesDispatchTierDefaults `json:"group_defaults"`
	GroupFamilies    map[string]string                         `json:"group_families"`
	FamilyPrefixes   map[string][]string                       `json:"family_prefixes"`
}

var (
	tkMessagesDispatchFamilyRegistryOnce sync.Once
	tkMessagesDispatchFamilyRegistryDoc  tkMessagesDispatchFamilyRegistry
)

func loadTkMessagesDispatchFamilyRegistry() tkMessagesDispatchFamilyRegistry {
	tkMessagesDispatchFamilyRegistryOnce.Do(func() {
		if err := json.Unmarshal(tkMessagesDispatchFamilyRegistryRaw, &tkMessagesDispatchFamilyRegistryDoc); err != nil {
			panic(fmt.Sprintf("tk_messages_dispatch_family_registry.json: %v", err))
		}
	})
	return tkMessagesDispatchFamilyRegistryDoc
}

func tkMessagesDispatchTierDefaultsForGroup(groupName, platform, family string) string {
	groupName = strings.TrimSpace(groupName)
	platform = strings.TrimSpace(platform)
	family = strings.TrimSpace(family)
	if family == "" {
		return ""
	}

	doc := loadTkMessagesDispatchFamilyRegistry()
	if groupName != "" {
		if tiers, ok := doc.GroupDefaults[groupName]; ok {
			if mapped := tkMessagesDispatchTierField(tiers, family); mapped != "" {
				return mapped
			}
		}
	}

	switch platform {
	case PlatformGrok:
		if tiers, ok := doc.PlatformDefaults[PlatformGrok]; ok {
			return tkMessagesDispatchTierField(tiers, family)
		}
	case PlatformOpenAI:
		if tiers, ok := doc.PlatformDefaults[PlatformOpenAI]; ok {
			return tkMessagesDispatchTierField(tiers, family)
		}
	case PlatformGemini:
		if tiers, ok := doc.PlatformDefaults[PlatformGemini]; ok {
			return tkMessagesDispatchTierField(tiers, family)
		}
	}
	return ""
}

func tkMessagesDispatchTierField(tiers tkMessagesDispatchTierDefaults, family string) string {
	switch family {
	case "opus":
		return strings.TrimSpace(tiers.OpusMappedModel)
	case "sonnet":
		return strings.TrimSpace(tiers.SonnetMappedModel)
	case "haiku":
		return strings.TrimSpace(tiers.HaikuMappedModel)
	default:
		return ""
	}
}

func tkMessagesDispatchFamilyForGroup(groupName string) (string, bool) {
	groupName = strings.TrimSpace(groupName)
	if groupName == "" {
		return "", false
	}
	doc := loadTkMessagesDispatchFamilyRegistry()
	family, ok := doc.GroupFamilies[groupName]
	return strings.TrimSpace(family), ok && family != ""
}

func tkMessagesDispatchModelMatchesFamily(model, family string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	family = strings.TrimSpace(family)
	if model == "" || family == "" {
		return false
	}
	doc := loadTkMessagesDispatchFamilyRegistry()
	prefixes := doc.FamilyPrefixes[family]
	for _, prefix := range prefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func validateGroupMessagesDispatchModelConfig(group *Group) error {
	if group == nil || !group.AllowMessagesDispatch || !tkGroupKeepsDispatchConfig(group) {
		return nil
	}

	groupName := strings.TrimSpace(group.Name)
	family, registered := tkMessagesDispatchFamilyForGroup(groupName)
	if !registered {
		switch group.Platform {
		case PlatformOpenAI:
			family = "gpt"
		case PlatformGrok:
			family = PlatformGrok
		case PlatformGemini:
			family = "gemini"
		default:
			return fmt.Errorf(
				"group %q (platform=%s) has allow_messages_dispatch but no entry in tk_messages_dispatch_family_registry.json — add group_defaults/group_families before enabling dispatch",
				groupName, group.Platform,
			)
		}
	}

	cfg := normalizeOpenAIMessagesDispatchModelConfig(group.MessagesDispatchModelConfig)
	checks := []struct {
		label string
		model string
	}{
		{label: "opus_mapped_model", model: cfg.OpusMappedModel},
		{label: "sonnet_mapped_model", model: cfg.SonnetMappedModel},
		{label: "haiku_mapped_model", model: cfg.HaikuMappedModel},
	}
	for _, check := range checks {
		if check.model == "" {
			return fmt.Errorf("group %q: %s is required when allow_messages_dispatch is enabled", groupName, check.label)
		}
		if !tkMessagesDispatchModelMatchesFamily(check.model, family) {
			return fmt.Errorf(
				"group %q: %s=%q does not match expected %s family prefixes — fix mapping or update tk_messages_dispatch_family_registry.json",
				groupName, check.label, check.model, family,
			)
		}
	}

	for requestedModel, mappedModel := range cfg.ExactModelMappings {
		if mappedModel == "" {
			continue
		}
		if !tkMessagesDispatchModelMatchesFamily(mappedModel, family) {
			return fmt.Errorf(
				"group %q: exact_model_mappings[%q]=%q does not match expected %s family prefixes",
				groupName, requestedModel, mappedModel, family,
			)
		}
	}
	return nil
}
