import registry from "../../../backend/internal/service/tk_messages_dispatch_family_registry.json";

export type MessagesDispatchTierDefaults = {
  opus_mapped_model: string;
  sonnet_mapped_model: string;
  haiku_mapped_model: string;
};

type MessagesDispatchFamilyRegistry = {
  platform_defaults: Record<string, MessagesDispatchTierDefaults>;
  group_defaults: Record<string, MessagesDispatchTierDefaults>;
  group_families: Record<string, string>;
  family_prefixes: Record<string, string[]>;
};

const familyRegistry = registry as MessagesDispatchFamilyRegistry;

export function messagesDispatchTierDefaultsForGroup(
  groupName?: string | null,
  platform?: string | null,
): Pick<
  MessagesDispatchTierDefaults,
  "opus_mapped_model" | "sonnet_mapped_model" | "haiku_mapped_model"
> | null {
  const normalizedName = groupName?.trim();
  if (normalizedName && familyRegistry.group_defaults[normalizedName]) {
    return { ...familyRegistry.group_defaults[normalizedName] };
  }
  const normalizedPlatform = platform?.trim();
  if (normalizedPlatform === "grok" && familyRegistry.platform_defaults.grok) {
    return { ...familyRegistry.platform_defaults.grok };
  }
  if (normalizedPlatform === "openai" && familyRegistry.platform_defaults.openai) {
    return { ...familyRegistry.platform_defaults.openai };
  }
  if (normalizedPlatform === "gemini" && familyRegistry.platform_defaults.gemini) {
    return { ...familyRegistry.platform_defaults.gemini };
  }
  return null;
}

export function messagesDispatchFamilyRegistryExport() {
  return familyRegistry;
}
