import { describe, expect, it } from "vitest";

import { messagesDispatchTierDefaultsForGroup } from "@/constants/messagesDispatchFamilyRegistry.tk";
import {
  createDefaultMessagesDispatchFormState,
  messagesDispatchConfigToFormState,
  messagesDispatchDefaultsForPlatform,
  messagesDispatchFormStateToConfig,
  resetMessagesDispatchFormState,
} from "../groupsMessagesDispatch";

function dispatchFormShell(
  tierDefaults: Pick<
    ReturnType<typeof messagesDispatchDefaultsForPlatform>,
    "opus_mapped_model" | "sonnet_mapped_model" | "haiku_mapped_model"
  >,
) {
  return {
    allow_messages_dispatch: false,
    ...tierDefaults,
    exact_model_mappings: [],
    messages_compaction_enabled: false,
    messages_compaction_input_tokens_threshold: null,
  };
}

describe("groupsMessagesDispatch", () => {
  it("hydrates gemini vendor defaults from registry for Google-Vertex", () => {
    const vertexDefaults = messagesDispatchTierDefaultsForGroup("Google-Vertex", "newapi");
    expect(vertexDefaults).not.toBeNull();
    expect(messagesDispatchConfigToFormState({}, "newapi", "Google-Vertex")).toEqual(
      dispatchFormShell(vertexDefaults!),
    );
  });

  it("returns empty defaults for unknown newapi groups", () => {
    expect(messagesDispatchDefaultsForPlatform("newapi", "brand-new-vendor")).toEqual({
      opus_mapped_model: "",
      sonnet_mapped_model: "",
      haiku_mapped_model: "",
    });
  });

  it("returns empty defaults when platform and group are unknown", () => {
    expect(createDefaultMessagesDispatchFormState()).toEqual(
      dispatchFormShell({
        opus_mapped_model: "",
        sonnet_mapped_model: "",
        haiku_mapped_model: "",
      }),
    );
  });

  it("returns openai defaults when platform is openai", () => {
    expect(createDefaultMessagesDispatchFormState("openai")).toEqual(
      dispatchFormShell(messagesDispatchDefaultsForPlatform("openai")),
    );
  });

  it("sanitizes exact model mapping rows when converting to config", () => {
    const config = messagesDispatchFormStateToConfig({
      allow_messages_dispatch: true,
      opus_mapped_model: " gpt-5.4 ",
      sonnet_mapped_model: "gpt-5.3-codex-spark",
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
      sonnet_mapped_model: "gpt-5.3-codex-spark",
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
    const grokDefaults = messagesDispatchDefaultsForPlatform("grok");
    expect(createDefaultMessagesDispatchFormState("grok")).toEqual(
      dispatchFormShell(grokDefaults),
    );
    expect(messagesDispatchDefaultsForPlatform("grok")).toEqual(grokDefaults);
  });

  it("hydrates grok form defaults from empty api config", () => {
    expect(messagesDispatchConfigToFormState({}, "grok")).toEqual(
      dispatchFormShell(messagesDispatchDefaultsForPlatform("grok")),
    );
  });

  it("resets mutable form state to empty defaults without platform context", () => {
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

    expect(state).toEqual(
      dispatchFormShell({
        opus_mapped_model: "",
        sonnet_mapped_model: "",
        haiku_mapped_model: "",
      }),
    );
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

    resetMessagesDispatchFormState(state, "openai");

    expect(state).toEqual(
      dispatchFormShell(messagesDispatchDefaultsForPlatform("openai")),
    );
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

    expect(state).toEqual(
      dispatchFormShell(messagesDispatchDefaultsForPlatform("grok")),
    );
  });
});
