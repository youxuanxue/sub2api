import { describe, expect, it } from "vitest";

import {
  createDefaultMessagesDispatchFormState,
  GROK_MESSAGES_DISPATCH_DEFAULTS,
  messagesDispatchConfigToFormState,
  messagesDispatchDefaultsForPlatform,
  messagesDispatchFormStateToConfig,
  resetMessagesDispatchFormState,
} from "../groupsMessagesDispatch";

describe("groupsMessagesDispatch", () => {
  it("returns the expected default form state", () => {
    expect(createDefaultMessagesDispatchFormState()).toEqual({
      allow_messages_dispatch: false,
      opus_mapped_model: "gpt-5.5",
      sonnet_mapped_model: "gpt-5.3-codex",
      haiku_mapped_model: "gpt-5.4-mini",
      exact_model_mappings: [],
      messages_compaction_enabled: false,
      messages_compaction_input_tokens_threshold: null,
    });
  });

  it("sanitizes exact model mapping rows when converting to config", () => {
    const config = messagesDispatchFormStateToConfig({
      allow_messages_dispatch: true,
      opus_mapped_model: " gpt-5.4 ",
      sonnet_mapped_model: "gpt-5.3-codex",
      haiku_mapped_model: " gpt-5.4-mini ",
      exact_model_mappings: [
        {
          claude_model: " claude-sonnet-4-5-20250929 ",
          target_model: " gpt-5.2 ",
        },
        { claude_model: "", target_model: "gpt-5.4" },
        { claude_model: "claude-opus-4-6", target_model: " " },
      ],
    });

    expect(config).toEqual({
      opus_mapped_model: "gpt-5.4",
      sonnet_mapped_model: "gpt-5.3-codex",
      haiku_mapped_model: "gpt-5.4-mini",
      exact_model_mappings: {
        "claude-sonnet-4-5-20250929": "gpt-5.2",
      },
    });
  });

  it("hydrates form state from api config", () => {
    expect(
      messagesDispatchConfigToFormState({
        opus_mapped_model: "gpt-5.4",
        sonnet_mapped_model: "gpt-5.2",
        haiku_mapped_model: "gpt-5.4-mini",
        exact_model_mappings: {
          "claude-opus-4-6": "gpt-5.4",
          "claude-haiku-4-5-20251001": "gpt-5.4-mini",
        },
      }),
    ).toEqual({
      allow_messages_dispatch: false,
      opus_mapped_model: "gpt-5.4",
      sonnet_mapped_model: "gpt-5.2",
      haiku_mapped_model: "gpt-5.4-mini",
      exact_model_mappings: [
        {
          claude_model: "claude-haiku-4-5-20251001",
          target_model: "gpt-5.4-mini",
        },
        { claude_model: "claude-opus-4-6", target_model: "gpt-5.4" },
      ],
      messages_compaction_enabled: false,
      messages_compaction_input_tokens_threshold: null,
    });
  });

  it("returns grok defaults when platform is grok", () => {
    expect(createDefaultMessagesDispatchFormState("grok")).toEqual({
      allow_messages_dispatch: false,
      ...GROK_MESSAGES_DISPATCH_DEFAULTS,
      exact_model_mappings: [],
      messages_compaction_enabled: false,
      messages_compaction_input_tokens_threshold: null,
    });
    expect(messagesDispatchDefaultsForPlatform("grok")).toEqual(
      GROK_MESSAGES_DISPATCH_DEFAULTS,
    );
  });

  it("hydrates grok form defaults from empty api config", () => {
    expect(messagesDispatchConfigToFormState({}, "grok")).toEqual({
      allow_messages_dispatch: false,
      ...GROK_MESSAGES_DISPATCH_DEFAULTS,
      exact_model_mappings: [],
      messages_compaction_enabled: false,
      messages_compaction_input_tokens_threshold: null,
    });
  });

  it("resets mutable form state to openai defaults", () => {
    const state = {
      allow_messages_dispatch: true,
      opus_mapped_model: "gpt-5.2",
      sonnet_mapped_model: "gpt-5.4",
      haiku_mapped_model: "gpt-5.1",
      exact_model_mappings: [
        { claude_model: "claude-opus-4-6", target_model: "gpt-5.4" },
      ],
    };

    resetMessagesDispatchFormState(state);

    expect(state).toEqual({
      allow_messages_dispatch: false,
      opus_mapped_model: "gpt-5.5",
      sonnet_mapped_model: "gpt-5.3-codex",
      haiku_mapped_model: "gpt-5.4-mini",
      exact_model_mappings: [],
      messages_compaction_enabled: false,
      messages_compaction_input_tokens_threshold: null,
    });
  });

  it("resets mutable form state to grok defaults", () => {
    const state = {
      allow_messages_dispatch: true,
      opus_mapped_model: "gpt-5.2",
      sonnet_mapped_model: "gpt-5.4",
      haiku_mapped_model: "gpt-5.1",
      exact_model_mappings: [
        { claude_model: "claude-opus-4-6", target_model: "gpt-5.4" },
      ],
    };

    resetMessagesDispatchFormState(state, "grok");

    expect(state).toEqual({
      allow_messages_dispatch: false,
      ...GROK_MESSAGES_DISPATCH_DEFAULTS,
      exact_model_mappings: [],
      messages_compaction_enabled: false,
      messages_compaction_input_tokens_threshold: null,
    });
  });
});
