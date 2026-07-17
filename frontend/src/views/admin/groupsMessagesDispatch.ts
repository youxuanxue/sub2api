import type { OpenAIMessagesDispatchModelConfig } from "@/types";
import { PLATFORM_GROK } from '@/constants/gatewayPlatforms'

export interface MessagesDispatchMappingRow {
  claude_model: string;
  target_model: string;
}

export interface MessagesDispatchFormState {
  allow_messages_dispatch: boolean;
  opus_mapped_model: string;
  sonnet_mapped_model: string;
  haiku_mapped_model: string;
  exact_model_mappings: MessagesDispatchMappingRow[];
  messages_compaction_enabled?: boolean;
  messages_compaction_input_tokens_threshold?: number | null;
}

export const OPENAI_MESSAGES_DISPATCH_DEFAULTS = {
  opus_mapped_model: "gpt-5.5",
  sonnet_mapped_model: "gpt-5.3-codex-spark",
  haiku_mapped_model: "gpt-5.4-mini",
} as const;

export const GROK_MESSAGES_DISPATCH_DEFAULTS = {
  opus_mapped_model: "grok-4.3",
  sonnet_mapped_model: "grok-code-fast-1",
  haiku_mapped_model: "grok-code-fast-1",
} as const;

export function messagesDispatchDefaultsForPlatform(
  platform?: string | null,
): Pick<
  MessagesDispatchFormState,
  "opus_mapped_model" | "sonnet_mapped_model" | "haiku_mapped_model"
> {
  if (platform === PLATFORM_GROK) {
    return { ...GROK_MESSAGES_DISPATCH_DEFAULTS };
  }
  return { ...OPENAI_MESSAGES_DISPATCH_DEFAULTS };
}

export function createDefaultMessagesDispatchFormState(
  platform?: string | null,
): MessagesDispatchFormState {
  return {
    allow_messages_dispatch: false,
    ...messagesDispatchDefaultsForPlatform(platform),
    exact_model_mappings: [],
    messages_compaction_enabled: false,
    messages_compaction_input_tokens_threshold: null,
  };
}

export function messagesDispatchConfigToFormState(
  config?: OpenAIMessagesDispatchModelConfig | null,
  platform?: string | null,
): MessagesDispatchFormState {
  const defaults = createDefaultMessagesDispatchFormState(platform);
  const exactMappings = Object.entries(config?.exact_model_mappings || {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([claude_model, target_model]) => ({ claude_model, target_model }));

  return {
    allow_messages_dispatch: false,
    opus_mapped_model:
      config?.opus_mapped_model?.trim() || defaults.opus_mapped_model,
    sonnet_mapped_model:
      config?.sonnet_mapped_model?.trim() || defaults.sonnet_mapped_model,
    haiku_mapped_model:
      config?.haiku_mapped_model?.trim() || defaults.haiku_mapped_model,
    exact_model_mappings: exactMappings,
    messages_compaction_enabled: defaults.messages_compaction_enabled,
    messages_compaction_input_tokens_threshold:
      defaults.messages_compaction_input_tokens_threshold,
  };
}

export function messagesDispatchFormStateToConfig(
  state: MessagesDispatchFormState,
): OpenAIMessagesDispatchModelConfig {
  const exactModelMappings = Object.fromEntries(
    state.exact_model_mappings
      .map((row) => [row.claude_model.trim(), row.target_model.trim()] as const)
      .filter(([claudeModel, targetModel]) => claudeModel && targetModel),
  );

  return {
    opus_mapped_model: state.opus_mapped_model.trim(),
    sonnet_mapped_model: state.sonnet_mapped_model.trim(),
    haiku_mapped_model: state.haiku_mapped_model.trim(),
    exact_model_mappings: exactModelMappings,
  };
}

export function resetMessagesDispatchFormState(
  target: MessagesDispatchFormState,
  platform?: string | null,
): void {
  const defaults = createDefaultMessagesDispatchFormState(platform);
  target.allow_messages_dispatch = defaults.allow_messages_dispatch;
  target.opus_mapped_model = defaults.opus_mapped_model;
  target.sonnet_mapped_model = defaults.sonnet_mapped_model;
  target.haiku_mapped_model = defaults.haiku_mapped_model;
  target.exact_model_mappings = [];
  target.messages_compaction_enabled = defaults.messages_compaction_enabled;
  target.messages_compaction_input_tokens_threshold =
    defaults.messages_compaction_input_tokens_threshold;
}
