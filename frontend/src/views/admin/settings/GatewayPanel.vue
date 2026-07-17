<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI } from "@/api";
import type {
  OpenAIFastPolicyRule,
  WebSearchEmulationConfig,
  WebSearchProviderConfig,
  WebSearchTestResult,
} from "@/api/admin/settings";
import type { Proxy } from "@/types";
import { useSettingsState } from "@/composables/useSettingsState";
import { extractApiErrorMessage } from "@/utils/apiError";
import { useAppStore } from "@/stores";
import Icon from "@/components/icons/Icon.vue";
import Toggle from "@/components/common/Toggle.vue";
import Select from "@/components/common/Select.vue";
import OpenAIFastPolicySettingsCard from "@/components/admin/settings/OpenAIFastPolicySettingsCard.vue";
import ProxySelector from "@/components/common/ProxySelector.vue";
import {
  parseFingerprintSignalsToRows,
  serializeFingerprintRowsToJSON,
  defaultFingerprintSignalRows,
  type FingerprintSignalRow,
} from "../codexFingerprintSignals";

const { t } = useI18n();
const appStore = useAppStore();
const { form } = useSettingsState();

// =====================================================================
// Overload Cooldown (529)
// =====================================================================

const overloadCooldownLoading = ref(true);
const overloadCooldownSaving = ref(false);
const overloadCooldownForm = reactive({
  enabled: true,
  cooldown_minutes: 10,
});

async function loadOverloadCooldownSettings() {
  overloadCooldownLoading.value = true;
  try {
    const settings = await adminAPI.settings.getOverloadCooldownSettings();
    Object.assign(overloadCooldownForm, settings);
  } catch (_error: unknown) {
    // Silent fail - settings will use defaults
  } finally {
    overloadCooldownLoading.value = false;
  }
}

async function saveOverloadCooldownSettings() {
  overloadCooldownSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateOverloadCooldownSettings({
      enabled: overloadCooldownForm.enabled,
      cooldown_minutes: overloadCooldownForm.cooldown_minutes,
    });
    Object.assign(overloadCooldownForm, updated);
    appStore.showSuccess(t("admin.settings.overloadCooldown.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(
        error,
        t("admin.settings.overloadCooldown.saveFailed"),
      ),
    );
  } finally {
    overloadCooldownSaving.value = false;
  }
}

// =====================================================================
// Rate Limit Cooldown (429)
// =====================================================================

const rateLimit429CooldownLoading = ref(true);
const rateLimit429CooldownSaving = ref(false);
const rateLimit429CooldownForm = reactive({
  enabled: true,
  cooldown_seconds: 5,
});

async function loadRateLimit429CooldownSettings() {
  rateLimit429CooldownLoading.value = true;
  try {
    const settings = await adminAPI.settings.getRateLimit429CooldownSettings();
    Object.assign(rateLimit429CooldownForm, settings);
  } catch (_error: unknown) {
    // Silent fail - settings will use defaults
  } finally {
    rateLimit429CooldownLoading.value = false;
  }
}

async function saveRateLimit429CooldownSettings() {
  rateLimit429CooldownSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateRateLimit429CooldownSettings({
      enabled: rateLimit429CooldownForm.enabled,
      cooldown_seconds: rateLimit429CooldownForm.cooldown_seconds,
    });
    Object.assign(rateLimit429CooldownForm, updated);
    appStore.showSuccess(t("admin.settings.rateLimit429Cooldown.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(
        error,
        t("admin.settings.rateLimit429Cooldown.saveFailed"),
      ),
    );
  } finally {
    rateLimit429CooldownSaving.value = false;
  }
}

// =====================================================================
// Stream Timeout
// =====================================================================

const streamTimeoutLoading = ref(true);
const streamTimeoutSaving = ref(false);
const streamTimeoutForm = reactive({
  enabled: true,
  action: "temp_unsched" as "temp_unsched" | "error" | "none",
  temp_unsched_minutes: 5,
  threshold_count: 3,
  threshold_window_minutes: 10,
});

async function loadStreamTimeoutSettings() {
  streamTimeoutLoading.value = true;
  try {
    const settings = await adminAPI.settings.getStreamTimeoutSettings();
    Object.assign(streamTimeoutForm, settings);
  } catch (_error: unknown) {
    // Silent fail - settings will use defaults
  } finally {
    streamTimeoutLoading.value = false;
  }
}

async function saveStreamTimeoutSettings() {
  streamTimeoutSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateStreamTimeoutSettings({
      enabled: streamTimeoutForm.enabled,
      action: streamTimeoutForm.action,
      temp_unsched_minutes: streamTimeoutForm.temp_unsched_minutes,
      threshold_count: streamTimeoutForm.threshold_count,
      threshold_window_minutes: streamTimeoutForm.threshold_window_minutes,
    });
    Object.assign(streamTimeoutForm, updated);
    appStore.showSuccess(t("admin.settings.streamTimeout.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(
        error,
        t("admin.settings.streamTimeout.saveFailed"),
      ),
    );
  } finally {
    streamTimeoutSaving.value = false;
  }
}

// =====================================================================
// Rectifier
// =====================================================================

const rectifierLoading = ref(true);
const rectifierSaving = ref(false);
const rectifierForm = reactive({
  enabled: true,
  thinking_signature_enabled: true,
  thinking_budget_enabled: true,
  apikey_signature_enabled: false,
  apikey_signature_patterns: [] as string[],
});

async function loadRectifierSettings() {
  rectifierLoading.value = true;
  try {
    const settings = await adminAPI.settings.getRectifierSettings();
    Object.assign(rectifierForm, settings);
    if (!Array.isArray(rectifierForm.apikey_signature_patterns)) {
      rectifierForm.apikey_signature_patterns = [];
    }
  } catch (_error: unknown) {
    // Silent fail - settings will use defaults
  } finally {
    rectifierLoading.value = false;
  }
}

async function saveRectifierSettings() {
  rectifierSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateRectifierSettings({
      enabled: rectifierForm.enabled,
      thinking_signature_enabled: rectifierForm.thinking_signature_enabled,
      thinking_budget_enabled: rectifierForm.thinking_budget_enabled,
      apikey_signature_enabled: rectifierForm.apikey_signature_enabled,
      apikey_signature_patterns: rectifierForm.apikey_signature_patterns.filter(
        (p) => p.trim() !== "",
      ),
    });
    Object.assign(rectifierForm, updated);
    if (!Array.isArray(rectifierForm.apikey_signature_patterns)) {
      rectifierForm.apikey_signature_patterns = [];
    }
    appStore.showSuccess(t("admin.settings.rectifier.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.rectifier.saveFailed")),
    );
  } finally {
    rectifierSaving.value = false;
  }
}

// =====================================================================
// Beta Policy
// =====================================================================

const betaPolicyLoading = ref(true);
const betaPolicySaving = ref(false);
const betaPolicyForm = reactive({
  rules: [] as Array<{
    beta_token: string;
    action: "pass" | "filter" | "block";
    scope: "all" | "oauth" | "apikey" | "bedrock";
    error_message?: string;
    model_whitelist?: string[];
    fallback_action?: "pass" | "filter" | "block";
    fallback_error_message?: string;
  }>,
});

const betaPolicyActionOptions = computed(() => [
  { value: "pass", label: t("admin.settings.betaPolicy.actionPass") },
  { value: "filter", label: t("admin.settings.betaPolicy.actionFilter") },
  { value: "block", label: t("admin.settings.betaPolicy.actionBlock") },
]);

const betaPolicyScopeOptions = computed(() => [
  { value: "all", label: t("admin.settings.betaPolicy.scopeAll") },
  { value: "oauth", label: t("admin.settings.betaPolicy.scopeOAuth") },
  { value: "apikey", label: t("admin.settings.betaPolicy.scopeAPIKey") },
  { value: "bedrock", label: t("admin.settings.betaPolicy.scopeBedrock") },
]);

const betaDisplayNames: Record<string, string> = {
  "fast-mode-2026-02-01": "Fast Mode",
  "context-1m-2025-08-07": "Context 1M",
};

const betaPresets: Record<
  string,
  Array<{
    label: string;
    description: string;
    action: "pass" | "filter" | "block";
    model_whitelist: string[];
    fallback_action: "pass" | "filter" | "block";
  }>
> = {
  "context-1m-2025-08-07": [
    {
      label: t("admin.settings.betaPolicy.presetOpusOnly"),
      description: t("admin.settings.betaPolicy.presetOpusOnlyDesc"),
      action: "pass",
      model_whitelist: ["claude-opus-4-6"],
      fallback_action: "filter",
    },
  ],
};

const commonModelPatterns = [
  "claude-opus-4-6",
  "claude-sonnet-4-6",
  "claude-opus-*",
  "claude-sonnet-*",
];

function getBetaDisplayName(token: string): string {
  return betaDisplayNames[token] || token;
}

function applyBetaPreset(
  rule: (typeof betaPolicyForm.rules)[number],
  preset: {
    action: "pass" | "filter" | "block";
    model_whitelist: string[];
    fallback_action: "pass" | "filter" | "block";
  },
) {
  rule.action = preset.action;
  rule.model_whitelist = [...preset.model_whitelist];
  rule.fallback_action = preset.fallback_action;
}

function addQuickPattern(
  rule: (typeof betaPolicyForm.rules)[number],
  pattern: string,
) {
  if (!rule.model_whitelist) rule.model_whitelist = [];
  if (!rule.model_whitelist.includes(pattern)) {
    rule.model_whitelist.push(pattern);
  }
}

async function loadBetaPolicySettings() {
  betaPolicyLoading.value = true;
  try {
    const settings = await adminAPI.settings.getBetaPolicySettings();
    betaPolicyForm.rules = settings.rules;
  } catch (_error: unknown) {
    // Silent fail - settings will use defaults
  } finally {
    betaPolicyLoading.value = false;
  }
}

async function saveBetaPolicySettings() {
  betaPolicySaving.value = true;
  try {
    const cleanedRules = betaPolicyForm.rules.map((rule) => {
      const whitelist = rule.model_whitelist?.filter((p) => p.trim() !== "");
      const hasWhitelist = whitelist && whitelist.length > 0;
      return {
        beta_token: rule.beta_token,
        action: rule.action,
        scope: rule.scope,
        error_message: rule.error_message,
        model_whitelist: hasWhitelist ? whitelist : undefined,
        fallback_action: hasWhitelist
          ? rule.fallback_action || "pass"
          : undefined,
        fallback_error_message:
          hasWhitelist && rule.fallback_action === "block"
            ? rule.fallback_error_message
            : undefined,
      };
    });
    const updated = await adminAPI.settings.updateBetaPolicySettings({
      rules: cleanedRules,
    });
    betaPolicyForm.rules = updated.rules;
    appStore.showSuccess(t("admin.settings.betaPolicy.saved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.betaPolicy.saveFailed")),
    );
  } finally {
    betaPolicySaving.value = false;
  }
}

// =====================================================================
// OpenAI Fast/Flex Policy
// =====================================================================

const openaiFastPolicyForm = reactive({
  rules: [] as OpenAIFastPolicyRule[],
});
const openaiFastPolicyLoaded = ref(false);

// =====================================================================
// Claude OAuth System Prompt Blocks
// =====================================================================

type ClaudeOAuthSystemPromptPreset =
  | "billing"
  | "system"
  | "expansion"
  | "custom";

interface ClaudeOAuthSystemPromptBlock {
  id: string;
  enabled: boolean;
  expanded: boolean;
  type: "text";
  preset: ClaudeOAuthSystemPromptPreset;
  text: string;
  cacheControlEnabled: boolean;
  cacheControlTTL: string;
}

interface ClaudeOAuthSystemPromptRawBlock {
  enabled?: boolean;
  type?: string;
  text?: string;
  cache_control?: unknown;
}

const defaultClaudeCodeSystemPrompt =
  "You are Claude Code, Anthropic's official CLI for Claude.";

const defaultClaudeCodeExpansionPrompt = `You are an interactive agent that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.

IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases.
IMPORTANT: You must NEVER generate or guess URLs for the user unless you are confident that the URLs are for helping the user with programming. You may use URLs provided by the user in their messages or local files.

# Tone and style
 - Only use emojis if the user explicitly requests it. Avoid using emojis in all communication unless asked.
 - Your responses should be short and concise.
 - When referencing specific functions or pieces of code include the pattern file_path:line_number to allow the user to easily navigate to the source code location.
 - When referencing GitHub issues or pull requests, use the owner/repo#123 format (e.g. anthropics/claude-code#100) so they render as clickable links.
 - Do not use a colon before tool calls. Your tool calls may not be shown directly in the output, so text like "Let me read the file:" followed by a read tool call should just be "Let me read the file." with a period.`;

let claudeOAuthSystemPromptBlockID = 0;

function nextClaudeOAuthSystemPromptBlockID(): string {
  claudeOAuthSystemPromptBlockID += 1;
  return `claude-oauth-system-prompt-block-${claudeOAuthSystemPromptBlockID}`;
}

function normalizeClaudeOAuthSystemPromptCacheTTL(value: unknown): string {
  return typeof value === "string" && value.trim() ? value.trim() : "5m";
}

function detectClaudeOAuthSystemPromptPreset(
  text: string,
): ClaudeOAuthSystemPromptPreset {
  const trimmed = text.trim();
  if (trimmed === "{billing_header}") {
    return "billing";
  }
  if (
    trimmed === "{claude_code_system_prompt}" ||
    trimmed === defaultClaudeCodeSystemPrompt
  ) {
    return "system";
  }
  if (
    trimmed === "{claude_code_expansion_prompt}" ||
    trimmed === defaultClaudeCodeExpansionPrompt
  ) {
    return "expansion";
  }
  return "custom";
}

function normalizeClaudeOAuthSystemPromptBlockText(
  text: string,
  expansionPrompt = "",
): string {
  const trimmed = text.trim();
  if (trimmed === "{claude_code_system_prompt}") {
    return defaultClaudeCodeSystemPrompt;
  }
  if (trimmed === "{claude_code_expansion_prompt}") {
    return expansionPrompt.trim() || defaultClaudeCodeExpansionPrompt;
  }
  return text;
}

function createClaudeOAuthSystemPromptBlock(
  overrides: Partial<ClaudeOAuthSystemPromptBlock> = {},
): ClaudeOAuthSystemPromptBlock {
  const text = overrides.text ?? "";
  return {
    id: nextClaudeOAuthSystemPromptBlockID(),
    enabled: overrides.enabled ?? true,
    expanded: overrides.expanded ?? true,
    type: "text",
    preset: overrides.preset ?? detectClaudeOAuthSystemPromptPreset(text),
    text,
    cacheControlEnabled: overrides.cacheControlEnabled ?? false,
    cacheControlTTL: overrides.cacheControlTTL ?? "5m",
  };
}

function createDefaultClaudeOAuthSystemPromptBlocks(
  expansionPrompt = "",
): ClaudeOAuthSystemPromptBlock[] {
  const normalizedExpansionPrompt = expansionPrompt.trim();
  const expansionText =
    normalizedExpansionPrompt || defaultClaudeCodeExpansionPrompt;

  return [
    createClaudeOAuthSystemPromptBlock({
      preset: "billing",
      text: "{billing_header}",
    }),
    createClaudeOAuthSystemPromptBlock({
      preset: "system",
      text: defaultClaudeCodeSystemPrompt,
    }),
    createClaudeOAuthSystemPromptBlock({
      preset:
        expansionText === defaultClaudeCodeExpansionPrompt
          ? "expansion"
          : "custom",
      text: expansionText,
      cacheControlEnabled: true,
      cacheControlTTL: "5m",
    }),
  ];
}

function parseClaudeOAuthSystemPromptCacheControl(cacheControl: unknown): {
  enabled: boolean;
  ttl: string;
} {
  if (cacheControl === true) {
    return { enabled: true, ttl: "5m" };
  }
  if (
    cacheControl &&
    typeof cacheControl === "object" &&
    !Array.isArray(cacheControl)
  ) {
    return {
      enabled: true,
      ttl: normalizeClaudeOAuthSystemPromptCacheTTL(
        (cacheControl as Record<string, unknown>).ttl,
      ),
    };
  }
  return { enabled: false, ttl: "5m" };
}

function parseClaudeOAuthSystemPromptBlocks(
  raw: string,
  expansionPrompt = "",
): ClaudeOAuthSystemPromptBlock[] {
  const trimmed = raw.trim();
  if (!trimmed) {
    return createDefaultClaudeOAuthSystemPromptBlocks(expansionPrompt);
  }

  try {
    const parsed = JSON.parse(trimmed) as
      | ClaudeOAuthSystemPromptRawBlock[]
      | { blocks?: ClaudeOAuthSystemPromptRawBlock[] };
    const rawBlocks = Array.isArray(parsed)
      ? parsed
      : Array.isArray(parsed.blocks)
        ? parsed.blocks
        : [];

    if (rawBlocks.length === 0) {
      return createDefaultClaudeOAuthSystemPromptBlocks(expansionPrompt);
    }

    return rawBlocks.map((block) => {
      const cacheControl = parseClaudeOAuthSystemPromptCacheControl(
        block.cache_control,
      );
      const text = normalizeClaudeOAuthSystemPromptBlockText(
        typeof block.text === "string" ? block.text : "",
        expansionPrompt,
      );
      return createClaudeOAuthSystemPromptBlock({
        enabled: block.enabled !== false,
        type: "text",
        text,
        preset: detectClaudeOAuthSystemPromptPreset(text),
        cacheControlEnabled: cacheControl.enabled,
        cacheControlTTL: cacheControl.ttl,
      });
    });
  } catch (_error) {
    return createDefaultClaudeOAuthSystemPromptBlocks(expansionPrompt);
  }
}

function serializeClaudeOAuthSystemPromptBlocksToJSON(
  blocks: ClaudeOAuthSystemPromptBlock[],
): string {
  const source =
    blocks.length > 0
      ? blocks
      : [
          createClaudeOAuthSystemPromptBlock({
            enabled: false,
            preset: "custom",
            text: "",
          }),
        ];

  const rawBlocks = source.map((block) => {
    const raw: ClaudeOAuthSystemPromptRawBlock = {
      enabled: block.enabled,
      type: block.type || "text",
      text: block.text,
    };
    if (block.cacheControlEnabled) {
      raw.cache_control = {
        type: "ephemeral",
        ttl: normalizeClaudeOAuthSystemPromptCacheTTL(block.cacheControlTTL),
      };
    }
    return raw;
  });

  return JSON.stringify(rawBlocks, null, 2);
}

const defaultClaudeOAuthSystemPromptBlocks =
  serializeClaudeOAuthSystemPromptBlocksToJSON(
    createDefaultClaudeOAuthSystemPromptBlocks(),
  );

const claudeOAuthSystemPromptBlocks = ref<ClaudeOAuthSystemPromptBlock[]>(
  createDefaultClaudeOAuthSystemPromptBlocks(),
);

const claudeOAuthSystemPromptPresetOptions = computed(() => [
  {
    value: "billing",
    label: t("admin.settings.gatewayForwarding.systemBlockPresetBilling"),
  },
  {
    value: "system",
    label: t("admin.settings.gatewayForwarding.systemBlockPresetIdentity"),
  },
  {
    value: "expansion",
    label: t("admin.settings.gatewayForwarding.systemBlockPresetExpansion"),
  },
  {
    value: "custom",
    label: t("admin.settings.gatewayForwarding.systemBlockPresetCustom"),
  },
]);

const claudeOAuthSystemPromptBlockTypeOptions = computed(() => [
  {
    value: "text",
    label: t("admin.settings.gatewayForwarding.systemBlockTypeText"),
  },
]);

const claudeOAuthSystemPromptCacheTTLOptions = computed(() => [
  { value: "5m", label: t("admin.settings.gatewayForwarding.cacheTTL5m") },
  { value: "1h", label: t("admin.settings.gatewayForwarding.cacheTTL1h") },
]);

function getClaudeOAuthPresetLabel(
  preset: ClaudeOAuthSystemPromptPreset,
): string {
  return (
    claudeOAuthSystemPromptPresetOptions.value.find(
      (option) => option.value === preset,
    )?.label || t("admin.settings.gatewayForwarding.systemBlockPresetCustom")
  );
}

function syncClaudeOAuthSystemPromptBlocksFormField(): void {
  form.claude_oauth_system_prompt_blocks =
    serializeClaudeOAuthSystemPromptBlocksToJSON(
      claudeOAuthSystemPromptBlocks.value,
    );
}

function addClaudeOAuthSystemPromptBlock(): void {
  claudeOAuthSystemPromptBlocks.value.push(
    createClaudeOAuthSystemPromptBlock({
      expanded: true,
      preset: "custom",
      text: "",
    }),
  );
  syncClaudeOAuthSystemPromptBlocksFormField();
}

function toggleClaudeOAuthSystemPromptBlock(index: number): void {
  const block = claudeOAuthSystemPromptBlocks.value[index];
  if (!block) {
    return;
  }
  block.expanded = !block.expanded;
}

function removeClaudeOAuthSystemPromptBlock(index: number): void {
  claudeOAuthSystemPromptBlocks.value.splice(index, 1);
  syncClaudeOAuthSystemPromptBlocksFormField();
}

function moveClaudeOAuthSystemPromptBlock(
  index: number,
  direction: -1 | 1,
): void {
  const targetIndex = index + direction;
  if (
    targetIndex < 0 ||
    targetIndex >= claudeOAuthSystemPromptBlocks.value.length
  ) {
    return;
  }
  const blocks = claudeOAuthSystemPromptBlocks.value;
  const current = blocks[index];
  blocks[index] = blocks[targetIndex];
  blocks[targetIndex] = current;
  syncClaudeOAuthSystemPromptBlocksFormField();
}

function applyClaudeOAuthSystemPromptPreset(
  index: number,
  value: string | number | boolean | null,
): void {
  const block = claudeOAuthSystemPromptBlocks.value[index];
  if (!block) {
    return;
  }
  const preset = String(value || "custom") as ClaudeOAuthSystemPromptPreset;
  block.preset = preset;
  block.type = "text";
  if (preset === "billing") {
    block.text = "{billing_header}";
    block.cacheControlEnabled = false;
    block.cacheControlTTL = "5m";
  } else if (preset === "system") {
    block.text = defaultClaudeCodeSystemPrompt;
    block.cacheControlEnabled = false;
    block.cacheControlTTL = "5m";
  } else if (preset === "expansion") {
    block.text =
      form.claude_oauth_system_prompt.trim() ||
      defaultClaudeCodeExpansionPrompt;
    block.cacheControlEnabled = true;
    block.cacheControlTTL = "5m";
  }
  syncClaudeOAuthSystemPromptBlocksFormField();
}

function markClaudeOAuthSystemPromptBlockCustom(
  block: ClaudeOAuthSystemPromptBlock,
): void {
  block.preset = detectClaudeOAuthSystemPromptPreset(block.text);
  syncClaudeOAuthSystemPromptBlocksFormField();
}

function resetClaudeOAuthSystemPromptBlocks(): void {
  claudeOAuthSystemPromptBlocks.value = createDefaultClaudeOAuthSystemPromptBlocks(
    form.claude_oauth_system_prompt,
  );
  syncClaudeOAuthSystemPromptBlocksFormField();
}

// =====================================================================
// Codex CLI black/whitelist & fingerprint signals
// =====================================================================

interface CodexClientRow {
  originator: string;
  uaContains: string;
  skipEngineFingerprint?: boolean;
}

const codexBlacklistRows = ref<CodexClientRow[]>([]);
const codexWhitelistRows = ref<CodexClientRow[]>([]);
const codexFingerprintRows = ref<FingerprintSignalRow[]>([]);
const codexFingerprintNoRequired = computed(
  () => !codexFingerprintRows.value.some((r) => r.required),
);

function addCodexFingerprintRow(): void {
  codexFingerprintRows.value.push({ type: "header_exact", match: "", required: false });
}
function removeCodexFingerprintRow(i: number): void {
  codexFingerprintRows.value.splice(i, 1);
}

function parseCodexEntriesToRows(raw: string): CodexClientRow[] {
  if (!raw || !raw.trim()) return [];
  try {
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return [];
    return arr.map((e) => ({
      originator: typeof e?.originator === "string" ? e.originator : "",
      uaContains: Array.isArray(e?.ua_contains)
        ? e.ua_contains
            .filter((x: unknown) => typeof x === "string")
            .join(", ")
        : "",
      skipEngineFingerprint: e?.skip_engine_fingerprint === true,
    }));
  } catch {
    return [];
  }
}

function serializeCodexRowsToJSON(rows: CodexClientRow[]): string {
  const entries = rows
    .map((r) => {
      const entry: {
        originator: string;
        ua_contains: string[];
        skip_engine_fingerprint?: boolean;
      } = {
        originator: r.originator.trim(),
        ua_contains: r.uaContains
          .split(",")
          .map((s) => s.trim())
          .filter((s) => s.length > 0),
      };
      if (r.skipEngineFingerprint) entry.skip_engine_fingerprint = true;
      return entry;
    })
    .filter((e) => e.originator !== "" || e.ua_contains.length > 0);
  return entries.length > 0 ? JSON.stringify(entries) : "";
}

function addCodexBlacklistRow(): void {
  codexBlacklistRows.value.push({ originator: "", uaContains: "" });
}
function removeCodexBlacklistRow(i: number): void {
  codexBlacklistRows.value.splice(i, 1);
}
function addCodexWhitelistRow(): void {
  codexWhitelistRows.value.push({
    originator: "",
    uaContains: "",
    skipEngineFingerprint: false,
  });
}
function removeCodexWhitelistRow(i: number): void {
  codexWhitelistRows.value.splice(i, 1);
}

// =====================================================================
// Web Search Emulation
// =====================================================================

const webSearchProxies = ref<Proxy[]>([]);
const DEFAULT_WEB_SEARCH_QUOTA_LIMIT = 1000;

const webSearchConfig = reactive<WebSearchEmulationConfig>({
  enabled: false,
  providers: [],
});

const expandedProviders = reactive<Record<number, boolean>>({});
const apiKeyVisible = reactive<Record<number, boolean>>({});
const wsTestQuery = ref("");
const wsTestLoading = ref(false);
const wsTestResult = ref<WebSearchTestResult | null>(null);
const wsTestDialogOpen = ref(false);

function openTestDialog() {
  wsTestResult.value = null;
  wsTestDialogOpen.value = true;
}

function toggleProviderExpand(idx: number) {
  expandedProviders[idx] = !expandedProviders[idx];
}

function removeWebSearchProvider(idx: number) {
  webSearchConfig.providers.splice(idx, 1);
  const newExpanded: Record<number, boolean> = {};
  const newVisible: Record<number, boolean> = {};
  for (let i = 0; i < webSearchConfig.providers.length; i++) {
    const oldIdx = i >= idx ? i + 1 : i;
    newExpanded[i] = expandedProviders[oldIdx] ?? false;
    newVisible[i] = apiKeyVisible[oldIdx] ?? false;
  }
  Object.keys(expandedProviders).forEach(
    (k) => delete expandedProviders[Number(k)],
  );
  Object.keys(apiKeyVisible).forEach((k) => delete apiKeyVisible[Number(k)]);
  Object.assign(expandedProviders, newExpanded);
  Object.assign(apiKeyVisible, newVisible);
}

function addWebSearchProvider() {
  const idx = webSearchConfig.providers.length;
  webSearchConfig.providers.push({
    type: "brave",
    api_key: "",
    api_key_configured: false,
    quota_limit: DEFAULT_WEB_SEARCH_QUOTA_LIMIT,
    subscribed_at: null,
    proxy_id: null,
    expires_at: null,
  } as WebSearchProviderConfig);
  expandedProviders[idx] = true;
}

function formatSubscribedAt(ts: number | null): string {
  if (!ts) return "";
  const d = new Date(ts * 1000);
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  const day = String(d.getUTCDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

function parseSubscribedAt(dateStr: string): number | null {
  if (!dateStr) return null;
  return Math.floor(new Date(dateStr + "T00:00:00Z").getTime() / 1000);
}

function quotaPercentage(provider: WebSearchProviderConfig): number {
  if (!provider.quota_limit || provider.quota_limit <= 0) return 0;
  return ((provider.quota_used ?? 0) / provider.quota_limit) * 100;
}

async function resetWebSearchUsage(idx: number) {
  const provider = webSearchConfig.providers[idx];
  if (!provider) return;
  if (!confirm(t("admin.settings.webSearchEmulation.resetUsageConfirm")))
    return;
  try {
    await adminAPI.settings.resetWebSearchUsage({
      provider_type: provider.type,
    });
    provider.quota_used = 0;
    appStore.showSuccess(
      t("admin.settings.webSearchEmulation.resetUsageSuccess"),
    );
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
  }
}

async function copyApiKey(idx: number) {
  const key = webSearchConfig.providers[idx]?.api_key;
  if (!key) {
    appStore.showError(
      t("admin.settings.webSearchEmulation.apiKeyPlaceholder"),
    );
    return;
  }
  try {
    await navigator.clipboard.writeText(key);
    appStore.showSuccess(t("admin.settings.webSearchEmulation.copied"));
  } catch {
    appStore.showError(t("common.error"));
  }
}

async function testWebSearchProvider() {
  wsTestLoading.value = true;
  wsTestResult.value = null;
  try {
    const query =
      wsTestQuery.value.trim() ||
      t("admin.settings.webSearchEmulation.testDefaultQuery");
    wsTestResult.value = await adminAPI.settings.testWebSearchEmulation(query);
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
  } finally {
    wsTestLoading.value = false;
  }
}

async function loadWebSearchConfig() {
  try {
    const [resp, proxiesResp] = await Promise.all([
      adminAPI.settings.getWebSearchEmulationConfig(),
      adminAPI.proxies.list().catch(() => ({ items: [] as Proxy[] })),
    ]);
    if (resp) {
      webSearchConfig.enabled = resp.enabled || false;
      webSearchConfig.providers = resp.providers || [];
    }
    webSearchProxies.value = proxiesResp.items || [];
  } catch (err: unknown) {
    const status = (err as { status?: number })?.status;
    if (status !== 404 && status !== undefined) {
      appStore.showError(extractApiErrorMessage(err, t("common.error")));
    }
  }
}

async function saveWebSearchConfig(): Promise<boolean> {
  try {
    for (const p of webSearchConfig.providers) {
      const raw = p.quota_limit;
      if (raw != null && Number(raw) !== 0 && Number(raw) < 1) {
        appStore.showError(
          t("admin.settings.webSearchEmulation.quotaLimitMustBePositive"),
        );
        return false;
      }
    }
    const providers = webSearchConfig.providers.map(
      (p: WebSearchProviderConfig) => ({
        ...p,
        quota_limit: Number(p.quota_limit) > 0 ? Number(p.quota_limit) : null,
      }),
    );
    await adminAPI.settings.updateWebSearchEmulationConfig({
      enabled: webSearchConfig.enabled,
      providers,
    });
    return true;
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t("common.error")));
    return false;
  }
}

// =====================================================================
// Hydration: sync local state from form fields after loadSettings
// =====================================================================

/**
 * Hydrate panel-local state from form fields.
 * Called on mount and whenever loadSettings updates form.
 */
function hydrateFromForm(): void {
  // Claude OAuth system prompt blocks
  if (!form.claude_oauth_system_prompt_blocks?.trim()) {
    form.claude_oauth_system_prompt_blocks =
      defaultClaudeOAuthSystemPromptBlocks;
  }
  claudeOAuthSystemPromptBlocks.value = parseClaudeOAuthSystemPromptBlocks(
    form.claude_oauth_system_prompt_blocks,
    form.claude_oauth_system_prompt,
  );
  syncClaudeOAuthSystemPromptBlocksFormField();

  // Codex blacklist/whitelist rows
  codexBlacklistRows.value = parseCodexEntriesToRows(
    form.codex_cli_only_blacklist,
  );
  codexWhitelistRows.value = parseCodexEntriesToRows(
    form.codex_cli_only_whitelist,
  );

  // Codex fingerprint signals
  codexFingerprintRows.value = form.codex_cli_only_engine_fingerprint_signals
    ? parseFingerprintSignalsToRows(form.codex_cli_only_engine_fingerprint_signals)
    : defaultFingerprintSignalRows();
}

// Watch form.codex_cli_only_blacklist for external updates (loadSettings)
watch(
  () => form.codex_cli_only_blacklist,
  (newVal) => {
    codexBlacklistRows.value = parseCodexEntriesToRows(newVal);
  },
);

watch(
  () => form.codex_cli_only_whitelist,
  (newVal) => {
    codexWhitelistRows.value = parseCodexEntriesToRows(newVal);
  },
);

watch(
  () => form.codex_cli_only_engine_fingerprint_signals,
  (newVal) => {
    codexFingerprintRows.value = newVal
      ? parseFingerprintSignalsToRows(newVal)
      : defaultFingerprintSignalRows();
  },
);

watch(
  () => form.claude_oauth_system_prompt_blocks,
  (newVal) => {
    if (!newVal?.trim()) return;
    claudeOAuthSystemPromptBlocks.value = parseClaudeOAuthSystemPromptBlocks(
      newVal,
      form.claude_oauth_system_prompt,
    );
  },
);

// =====================================================================
// Lifecycle
// =====================================================================

onMounted(() => {
  loadOverloadCooldownSettings();
  loadRateLimit429CooldownSettings();
  loadStreamTimeoutSettings();
  loadRectifierSettings();
  loadBetaPolicySettings();
  loadWebSearchConfig();
  // Hydrate panel state from form (already loaded by parent)
  hydrateFromForm();
});

// =====================================================================
// Expose for parent (saveSettings serialization)
// =====================================================================

defineExpose({
  // Codex serialization (called by parent saveSettings)
  codexBlacklistRows,
  codexWhitelistRows,
  codexFingerprintRows,
  serializeCodexRowsToJSON,
  serializeFingerprintRowsToJSON,
  // Claude OAuth blocks
  claudeOAuthSystemPromptBlocks,
  serializeClaudeOAuthSystemPromptBlocksToJSON,
  // Web search
  saveWebSearchConfig,
  // OpenAI fast policy
  openaiFastPolicyForm,
  openaiFastPolicyLoaded,
  // Hydration
  hydrateFromForm,
});
</script>

<template>
  <div class="space-y-6">
    <!-- ============================================================= -->
    <!-- Gateway Block 1: Overload, Rate Limit, Stream, Rectifier, Beta -->
    <!-- ============================================================= -->

    <!-- Overload Cooldown (529) Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.overloadCooldown.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.overloadCooldown.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div
          v-if="overloadCooldownLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <div class="flex items-center justify-between">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">{{
                t("admin.settings.overloadCooldown.enabled")
              }}</label>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.overloadCooldown.enabledHint") }}
              </p>
            </div>
            <Toggle v-model="overloadCooldownForm.enabled" />
          </div>

          <div
            v-if="overloadCooldownForm.enabled"
            class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.overloadCooldown.cooldownMinutes") }}
              </label>
              <input
                v-model.number="overloadCooldownForm.cooldown_minutes"
                type="number"
                min="1"
                max="120"
                class="input w-32"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  t("admin.settings.overloadCooldown.cooldownMinutesHint")
                }}
              </p>
            </div>
          </div>

          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              @click="saveOverloadCooldownSettings"
              :disabled="overloadCooldownSaving"
              class="btn btn-primary btn-sm"
            >
              <svg
                v-if="overloadCooldownSaving"
                class="mr-1 h-4 w-4 animate-spin"
                fill="none"
                viewBox="0 0 24 24"
              >
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
              {{
                overloadCooldownSaving
                  ? t("common.saving")
                  : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- Rate Limit Cooldown (429) Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.rateLimit429Cooldown.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.rateLimit429Cooldown.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div
          v-if="rateLimit429CooldownLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <div class="flex items-center justify-between">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">{{
                t("admin.settings.rateLimit429Cooldown.enabled")
              }}</label>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.rateLimit429Cooldown.enabledHint") }}
              </p>
            </div>
            <Toggle v-model="rateLimit429CooldownForm.enabled" />
          </div>

          <div
            v-if="rateLimit429CooldownForm.enabled"
            class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{
                  t(
                    "admin.settings.rateLimit429Cooldown.cooldownSeconds",
                  )
                }}
              </label>
              <input
                v-model.number="rateLimit429CooldownForm.cooldown_seconds"
                type="number"
                min="1"
                max="7200"
                class="input w-32"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  t(
                    "admin.settings.rateLimit429Cooldown.cooldownSecondsHint",
                  )
                }}
              </p>
            </div>
          </div>

          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              @click="saveRateLimit429CooldownSettings"
              :disabled="rateLimit429CooldownSaving"
              class="btn btn-primary btn-sm"
            >
              <svg
                v-if="rateLimit429CooldownSaving"
                class="mr-1 h-4 w-4 animate-spin"
                fill="none"
                viewBox="0 0 24 24"
              >
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
              {{
                rateLimit429CooldownSaving
                  ? t("common.saving")
                  : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- Stream Timeout Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.streamTimeout.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.streamTimeout.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Loading State -->
        <div
          v-if="streamTimeoutLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <!-- Enable Stream Timeout -->
          <div class="flex items-center justify-between">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">{{
                t("admin.settings.streamTimeout.enabled")
              }}</label>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.streamTimeout.enabledHint") }}
              </p>
            </div>
            <Toggle v-model="streamTimeoutForm.enabled" />
          </div>

          <!-- Settings - Only show when enabled -->
          <div
            v-if="streamTimeoutForm.enabled"
            class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <!-- Action -->
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.streamTimeout.action") }}
              </label>
              <select
                v-model="streamTimeoutForm.action"
                class="input w-64"
              >
                <option value="temp_unsched">
                  {{
                    t("admin.settings.streamTimeout.actionTempUnsched")
                  }}
                </option>
                <option value="error">
                  {{ t("admin.settings.streamTimeout.actionError") }}
                </option>
                <option value="none">
                  {{ t("admin.settings.streamTimeout.actionNone") }}
                </option>
              </select>
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.streamTimeout.actionHint") }}
              </p>
            </div>

            <!-- Temp Unsched Minutes (only show when action is temp_unsched) -->
            <div v-if="streamTimeoutForm.action === 'temp_unsched'">
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.streamTimeout.tempUnschedMinutes") }}
              </label>
              <input
                v-model.number="streamTimeoutForm.temp_unsched_minutes"
                type="number"
                min="1"
                max="60"
                class="input w-32"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  t("admin.settings.streamTimeout.tempUnschedMinutesHint")
                }}
              </p>
            </div>

            <!-- Threshold Count -->
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.streamTimeout.thresholdCount") }}
              </label>
              <input
                v-model.number="streamTimeoutForm.threshold_count"
                type="number"
                min="1"
                max="10"
                class="input w-32"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.streamTimeout.thresholdCountHint") }}
              </p>
            </div>

            <!-- Threshold Window Minutes -->
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{
                  t("admin.settings.streamTimeout.thresholdWindowMinutes")
                }}
              </label>
              <input
                v-model.number="
                  streamTimeoutForm.threshold_window_minutes
                "
                type="number"
                min="1"
                max="60"
                class="input w-32"
              />
              <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
                {{
                  t(
                    "admin.settings.streamTimeout.thresholdWindowMinutesHint",
                  )
                }}
              </p>
            </div>
          </div>

          <!-- Save Button -->
          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              @click="saveStreamTimeoutSettings"
              :disabled="streamTimeoutSaving"
              class="btn btn-primary btn-sm"
            >
              <svg
                v-if="streamTimeoutSaving"
                class="mr-1 h-4 w-4 animate-spin"
                fill="none"
                viewBox="0 0 24 24"
              >
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
              {{
                streamTimeoutSaving
                  ? t("common.saving")
                  : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- Request Rectifier Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.rectifier.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.rectifier.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Loading State -->
        <div
          v-if="rectifierLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <!-- Master Toggle -->
          <div class="flex items-center justify-between">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">{{
                t("admin.settings.rectifier.enabled")
              }}</label>
              <p class="text-sm text-gray-500 dark:text-gray-400">
                {{ t("admin.settings.rectifier.enabledHint") }}
              </p>
            </div>
            <Toggle v-model="rectifierForm.enabled" />
          </div>

          <!-- Sub-toggles (only show when master is enabled) -->
          <div
            v-if="rectifierForm.enabled"
            class="space-y-4 border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <!-- Thinking Signature Rectifier -->
            <div class="flex items-center justify-between">
              <div>
                <label
                  class="text-sm font-medium text-gray-700 dark:text-gray-300"
                  >{{
                    t("admin.settings.rectifier.thinkingSignature")
                  }}</label
                >
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{
                    t("admin.settings.rectifier.thinkingSignatureHint")
                  }}
                </p>
              </div>
              <Toggle
                v-model="rectifierForm.thinking_signature_enabled"
              />
            </div>

            <!-- Thinking Budget Rectifier -->
            <div class="flex items-center justify-between">
              <div>
                <label
                  class="text-sm font-medium text-gray-700 dark:text-gray-300"
                  >{{
                    t("admin.settings.rectifier.thinkingBudget")
                  }}</label
                >
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t("admin.settings.rectifier.thinkingBudgetHint") }}
                </p>
              </div>
              <Toggle v-model="rectifierForm.thinking_budget_enabled" />
            </div>

            <!-- API Key Signature Rectifier -->
            <div class="flex items-center justify-between">
              <div>
                <label
                  class="text-sm font-medium text-gray-700 dark:text-gray-300"
                  >{{
                    t("admin.settings.rectifier.apikeySignature")
                  }}</label
                >
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t("admin.settings.rectifier.apikeySignatureHint") }}
                </p>
              </div>
              <Toggle v-model="rectifierForm.apikey_signature_enabled" />
            </div>

            <!-- Custom Patterns (only when apikey_signature_enabled) -->
            <div
              v-if="rectifierForm.apikey_signature_enabled"
              class="ml-4 space-y-3 border-l-2 border-gray-200 pl-4 dark:border-dark-600"
            >
              <div>
                <label
                  class="text-sm font-medium text-gray-700 dark:text-gray-300"
                  >{{
                    t("admin.settings.rectifier.apikeyPatterns")
                  }}</label
                >
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t("admin.settings.rectifier.apikeyPatternsHint") }}
                </p>
              </div>
              <div
                v-for="(
                  _, index
                ) in rectifierForm.apikey_signature_patterns"
                :key="index"
                class="flex items-center gap-2"
              >
                <input
                  v-model="rectifierForm.apikey_signature_patterns[index]"
                  type="text"
                  class="input input-sm flex-1"
                  :placeholder="
                    t('admin.settings.rectifier.apikeyPatternPlaceholder')
                  "
                />
                <button
                  type="button"
                  @click="
                    rectifierForm.apikey_signature_patterns.splice(
                      index,
                      1,
                    )
                  "
                  class="btn btn-ghost btn-xs text-red-500 hover:text-red-700"
                >
                  <svg
                    class="h-4 w-4"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="2"
                      d="M6 18L18 6M6 6l12 12"
                    />
                  </svg>
                </button>
              </div>
              <button
                type="button"
                @click="rectifierForm.apikey_signature_patterns.push('')"
                class="btn btn-ghost btn-xs text-primary-600 dark:text-primary-400"
              >
                + {{ t("admin.settings.rectifier.addPattern") }}
              </button>
            </div>
          </div>

          <!-- Save Button -->
          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              @click="saveRectifierSettings"
              :disabled="rectifierSaving"
              class="btn btn-primary btn-sm"
            >
              <svg
                v-if="rectifierSaving"
                class="mr-1 h-4 w-4 animate-spin"
                fill="none"
                viewBox="0 0 24 24"
              >
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
              {{
                rectifierSaving ? t("common.saving") : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- Beta Policy Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.betaPolicy.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.betaPolicy.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Loading State -->
        <div
          v-if="betaPolicyLoading"
          class="flex items-center gap-2 text-gray-500"
        >
          <div
            class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"
          ></div>
          {{ t("common.loading") }}
        </div>

        <template v-else>
          <!-- Rule Cards -->
          <div
            v-for="rule in betaPolicyForm.rules"
            :key="rule.beta_token"
            class="rounded-lg border border-gray-200 p-4 dark:border-dark-600"
          >
            <div class="mb-3 flex items-center gap-2">
              <span
                class="text-sm font-medium text-gray-900 dark:text-white"
              >
                {{ getBetaDisplayName(rule.beta_token) }}
              </span>
              <span
                class="rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400"
              >
                {{ rule.beta_token }}
              </span>
            </div>

            <div class="grid grid-cols-2 gap-4">
              <!-- Action -->
              <div>
                <label
                  class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400"
                >
                  {{ t("admin.settings.betaPolicy.action") }}
                </label>
                <Select
                  :modelValue="rule.action"
                  @update:modelValue="rule.action = $event as any"
                  :options="betaPolicyActionOptions"
                />
              </div>

              <!-- Scope -->
              <div>
                <label
                  class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400"
                >
                  {{ t("admin.settings.betaPolicy.scope") }}
                </label>
                <Select
                  :modelValue="rule.scope"
                  @update:modelValue="rule.scope = $event as any"
                  :options="betaPolicyScopeOptions"
                />
              </div>
            </div>

            <!-- Error Message (only when action=block) -->
            <div v-if="rule.action === 'block'" class="mt-3">
              <label
                class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400"
              >
                {{ t("admin.settings.betaPolicy.errorMessage") }}
              </label>
              <input
                v-model="rule.error_message"
                type="text"
                class="input"
                :placeholder="
                  t('admin.settings.betaPolicy.errorMessagePlaceholder')
                "
              />
              <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">
                {{ t("admin.settings.betaPolicy.errorMessageHint") }}
              </p>
            </div>

            <!-- Quick Presets (only for tokens with presets) -->
            <div v-if="betaPresets[rule.beta_token]?.length" class="mt-3">
              <label
                class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400"
              >
                {{ t("admin.settings.betaPolicy.quickPresets") }}
              </label>
              <div class="flex flex-wrap gap-2">
                <button
                  v-for="preset in betaPresets[rule.beta_token]"
                  :key="preset.label"
                  type="button"
                  class="inline-flex items-center gap-1 rounded-md border border-primary-200 bg-primary-50 px-2.5 py-1 text-xs font-medium text-primary-700 transition-colors hover:bg-primary-100 dark:border-primary-800 dark:bg-primary-900/30 dark:text-primary-300 dark:hover:bg-primary-900/50"
                  @click="applyBetaPreset(rule, preset)"
                  :title="preset.description"
                >
                  {{ preset.label }}
                </button>
              </div>
            </div>

            <!-- Model Whitelist -->
            <div class="mt-3">
              <label
                class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400"
              >
                {{ t("admin.settings.betaPolicy.modelWhitelist") }}
              </label>
              <p class="mb-2 text-xs text-gray-400 dark:text-gray-500">
                {{ t("admin.settings.betaPolicy.modelWhitelistHint") }}
              </p>
              <!-- Existing patterns -->
              <div
                v-for="(_, index) in rule.model_whitelist || []"
                :key="index"
                class="mb-1.5 flex items-center gap-2"
              >
                <input
                  v-model="rule.model_whitelist![index]"
                  type="text"
                  class="input input-sm flex-1"
                  :placeholder="
                    t('admin.settings.betaPolicy.modelPatternPlaceholder')
                  "
                />
                <button
                  type="button"
                  @click="rule.model_whitelist!.splice(index, 1)"
                  class="shrink-0 rounded p-1 text-red-400 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
                >
                  <svg
                    class="h-4 w-4"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    stroke-width="2"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      d="M6 18L18 6M6 6l12 12"
                    />
                  </svg>
                </button>
              </div>
              <!-- Add pattern button -->
              <button
                type="button"
                @click="
                  if (!rule.model_whitelist) rule.model_whitelist = [];
                  rule.model_whitelist.push('');
                "
                class="mb-2 inline-flex items-center gap-1 text-xs text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
              >
                <svg
                  class="h-3.5 w-3.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  stroke-width="2"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    d="M12 4v16m8-8H4"
                  />
                </svg>
                {{ t("admin.settings.betaPolicy.addModelPattern") }}
              </button>
              <!-- Common pattern chips -->
              <div class="flex flex-wrap items-center gap-1.5">
                <span class="text-xs text-gray-400 dark:text-gray-500"
                  >{{
                    t("admin.settings.betaPolicy.commonPatterns")
                  }}:</span
                >
                <button
                  v-for="pattern in commonModelPatterns"
                  :key="pattern"
                  type="button"
                  class="rounded border border-gray-200 px-2 py-0.5 text-xs text-gray-600 transition-colors hover:border-primary-300 hover:bg-primary-50 hover:text-primary-700 dark:border-dark-600 dark:text-gray-400 dark:hover:border-primary-700 dark:hover:bg-primary-900/30 dark:hover:text-primary-300"
                  @click="addQuickPattern(rule, pattern)"
                >
                  {{ pattern }}
                </button>
              </div>
            </div>

            <!-- Fallback Action (only when model_whitelist is non-empty) -->
            <div
              v-if="
                rule.model_whitelist && rule.model_whitelist.length > 0
              "
              class="mt-3"
            >
              <label
                class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400"
              >
                {{ t("admin.settings.betaPolicy.fallbackAction") }}
              </label>
              <Select
                :modelValue="rule.fallback_action || 'pass'"
                @update:modelValue="rule.fallback_action = $event as any"
                :options="betaPolicyActionOptions"
              />
              <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">
                {{ t("admin.settings.betaPolicy.fallbackActionHint") }}
              </p>
              <!-- Fallback Error Message (only when fallback_action=block) -->
              <div v-if="rule.fallback_action === 'block'" class="mt-2">
                <input
                  v-model="rule.fallback_error_message"
                  type="text"
                  class="input"
                  :placeholder="
                    t(
                      'admin.settings.betaPolicy.fallbackErrorMessagePlaceholder',
                    )
                  "
                />
                <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">
                  {{ t("admin.settings.betaPolicy.errorMessageHint") }}
                </p>
              </div>
            </div>
          </div>

          <!-- Save Button -->
          <div
            class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700"
          >
            <button
              type="button"
              @click="saveBetaPolicySettings"
              :disabled="betaPolicySaving"
              class="btn btn-primary btn-sm"
            >
              <svg
                v-if="betaPolicySaving"
                class="mr-1 h-4 w-4 animate-spin"
                fill="none"
                viewBox="0 0 24 24"
              >
                <circle
                  class="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  stroke-width="4"
                ></circle>
                <path
                  class="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                ></path>
              </svg>
              {{
                betaPolicySaving ? t("common.saving") : t("common.save")
              }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- OpenAI Fast Policy -->
    <OpenAIFastPolicySettingsCard
      v-model:rules="openaiFastPolicyForm.rules"
    />

    <!-- ============================================================= -->
    <!-- Gateway Block 2: Claude Code, Scheduling, Forwarding, WebSearch -->
    <!-- ============================================================= -->

    <!-- Claude Code Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.claudeCode.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.claudeCode.description") }}
        </p>
      </div>
      <div class="p-6">
        <div>
          <label
            class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{ t("admin.settings.claudeCode.minVersion") }}
          </label>
          <input
            v-model="form.min_claude_code_version"
            type="text"
            class="input max-w-xs font-mono text-sm"
            :placeholder="
              t('admin.settings.claudeCode.minVersionPlaceholder')
            "
          />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.claudeCode.minVersionHint") }}
          </p>
        </div>
        <div class="mt-4">
          <label
            class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{ t("admin.settings.claudeCode.maxVersion") }}
          </label>
          <input
            v-model="form.max_claude_code_version"
            type="text"
            class="input max-w-xs font-mono text-sm"
            :placeholder="
              t('admin.settings.claudeCode.maxVersionPlaceholder')
            "
          />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.claudeCode.maxVersionHint") }}
          </p>
        </div>
      </div>
    </div>

    <!-- Codex Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.gatewayForwarding.codexHardeningTitle") }}
        </h2>
      </div>
      <div class="p-6 space-y-4">
          <div>
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t("admin.settings.gatewayForwarding.codexClientRestrictionTitle") }}
            </h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.gatewayForwarding.codexHardeningDesc") }}
            </p>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.gatewayForwarding.minCodexVersion") }}
              </label>
              <input
                v-model="form.min_codex_version"
                type="text"
                class="input w-full font-mono text-sm"
                :placeholder="
                  t(
                    'admin.settings.gatewayForwarding.minCodexVersionPlaceholder',
                  )
                "
              />
            </div>
            <div>
              <label
                class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{ t("admin.settings.gatewayForwarding.maxCodexVersion") }}
              </label>
              <input
                v-model="form.max_codex_version"
                type="text"
                class="input w-full font-mono text-sm"
                :placeholder="
                  t(
                    'admin.settings.gatewayForwarding.maxCodexVersionPlaceholder',
                  )
                "
              />
            </div>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.gatewayForwarding.codexVersionHint") }}
          </p>

          <div>
            <label class="block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.gatewayForwarding.codexFingerprintSignals") }}
            </label>
            <p class="mb-2 mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.gatewayForwarding.codexFingerprintSignalsDesc") }}
            </p>
            <div
              v-for="(row, i) in codexFingerprintRows"
              :key="`codex-fp-${i}`"
              class="mb-2 flex items-center gap-2"
            >
              <select v-model="row.type" class="input w-32 text-sm">
                <option value="header_exact">{{ t("admin.settings.gatewayForwarding.codexFpTypeHeaderExact") }}</option>
                <option value="header_prefix">{{ t("admin.settings.gatewayForwarding.codexFpTypeHeaderPrefix") }}</option>
                <option value="body_path">{{ t("admin.settings.gatewayForwarding.codexFpTypeBodyPath") }}</option>
              </select>
              <input
                v-model="row.match"
                type="text"
                class="input flex-1 font-mono text-sm"
                :placeholder="t('admin.settings.gatewayForwarding.codexFpMatchPlaceholder')"
              />
              <label class="flex shrink-0 items-center gap-1 text-xs text-gray-600 dark:text-gray-400">
                <input v-model="row.required" type="checkbox" />
                {{ t("admin.settings.gatewayForwarding.codexFpRequired") }}
              </label>
              <button
                type="button"
                class="btn btn-secondary btn-sm shrink-0 text-red-600 hover:text-red-700 dark:text-red-400"
                @click="removeCodexFingerprintRow(i)"
              >
                {{ t("admin.settings.gatewayForwarding.codexRemoveRow") }}
              </button>
            </div>
            <button type="button" class="btn btn-secondary btn-sm" @click="addCodexFingerprintRow">
              {{ t("admin.settings.gatewayForwarding.codexAddRow") }}
            </button>
            <p
              v-if="codexFingerprintNoRequired"
              class="mt-2 text-xs text-amber-600 dark:text-amber-500"
            >
              {{ t("admin.settings.gatewayForwarding.codexFingerprintNoRequiredWarn") }}
            </p>
          </div>

          <div class="flex items-center justify-between">
            <div class="pr-4">
              <label
                class="block text-sm font-medium text-gray-700 dark:text-gray-300"
              >
                {{
                  t("admin.settings.gatewayForwarding.codexAllowAppServer")
                }}
              </label>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{
                  t(
                    "admin.settings.gatewayForwarding.codexAllowAppServerDesc",
                  )
                }}
              </p>
            </div>
            <Toggle
              v-model="form.codex_cli_only_allow_app_server_clients"
            />
          </div>

          <div>
            <label
              class="block text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.gatewayForwarding.codexBlacklist") }}
            </label>
            <p class="mb-2 mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.gatewayForwarding.codexBlacklistDesc") }}
            </p>
            <div
              v-for="(row, i) in codexBlacklistRows"
              :key="`codex-bl-${i}`"
              class="mb-2 flex gap-2"
            >
              <input
                v-model="row.originator"
                type="text"
                class="input w-1/3 font-mono text-sm"
                :placeholder="
                  t(
                    'admin.settings.gatewayForwarding.codexOriginatorPlaceholder',
                  )
                "
              />
              <input
                v-model="row.uaContains"
                type="text"
                class="input flex-1 font-mono text-sm"
                :placeholder="
                  t(
                    'admin.settings.gatewayForwarding.codexUaContainsPlaceholder',
                  )
                "
              />
              <button
                type="button"
                class="btn btn-secondary btn-sm shrink-0 text-red-600 hover:text-red-700 dark:text-red-400"
                @click="removeCodexBlacklistRow(i)"
              >
                {{ t("admin.settings.gatewayForwarding.codexRemoveRow") }}
              </button>
            </div>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="addCodexBlacklistRow"
            >
              {{ t("admin.settings.gatewayForwarding.codexAddRow") }}
            </button>
          </div>

          <div>
            <label
              class="block text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.gatewayForwarding.codexWhitelist") }}
            </label>
            <p class="mb-2 mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.gatewayForwarding.codexWhitelistDesc") }}
            </p>
            <div
              v-for="(row, i) in codexWhitelistRows"
              :key="`codex-wl-${i}`"
              class="mb-2 flex gap-2"
            >
              <input
                v-model="row.originator"
                type="text"
                class="input w-1/3 font-mono text-sm"
                :placeholder="
                  t(
                    'admin.settings.gatewayForwarding.codexOriginatorPlaceholder',
                  )
                "
              />
              <input
                v-model="row.uaContains"
                type="text"
                class="input flex-1 font-mono text-sm"
                :placeholder="
                  t(
                    'admin.settings.gatewayForwarding.codexUaContainsPlaceholder',
                  )
                "
              />
              <label
                class="flex shrink-0 items-center gap-1 text-xs text-gray-600 dark:text-gray-400"
                :title="
                  t(
                    'admin.settings.gatewayForwarding.codexWhitelistSkipFingerprintTooltip',
                  )
                "
              >
                <input
                  v-model="row.skipEngineFingerprint"
                  type="checkbox"
                />
                {{
                  t(
                    'admin.settings.gatewayForwarding.codexWhitelistSkipFingerprint',
                  )
                }}
              </label>
              <button
                type="button"
                class="btn btn-secondary btn-sm shrink-0 text-red-600 hover:text-red-700 dark:text-red-400"
                @click="removeCodexWhitelistRow(i)"
              >
                {{ t("admin.settings.gatewayForwarding.codexRemoveRow") }}
              </button>
            </div>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="addCodexWhitelistRow"
            >
              {{ t("admin.settings.gatewayForwarding.codexAddRow") }}
            </button>
          </div>
      </div>
    </div>

    <!-- Gateway Scheduling Settings -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.scheduling.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.scheduling.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.scheduling.allowUngroupedKey") }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.scheduling.allowUngroupedKeyHint") }}
            </p>
          </div>
          <Toggle v-model="form.allow_ungrouped_key_scheduling" />
        </div>

        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.openaiExperimentalScheduler.title") }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t("admin.settings.openaiExperimentalScheduler.description")
              }}
            </p>
          </div>
          <Toggle v-model="form.openai_advanced_scheduler_enabled" />
        </div>
      </div>
    </div>

    <!-- Gateway Forwarding Behavior -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.gatewayForwarding.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.gatewayForwarding.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Fingerprint Unification -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t(
                  "admin.settings.gatewayForwarding.fingerprintUnification",
                )
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.fingerprintUnificationHint",
                )
              }}
            </p>
          </div>
          <Toggle v-model="form.enable_fingerprint_unification" />
        </div>

        <!-- Metadata Passthrough -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t("admin.settings.gatewayForwarding.metadataPassthrough")
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.metadataPassthroughHint",
                )
              }}
            </p>
          </div>
          <Toggle v-model="form.enable_metadata_passthrough" />
        </div>

        <!-- Sticky Routing -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.gatewayForwarding.stickyRouting") }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.gatewayForwarding.stickyRoutingHint") }}
            </p>
          </div>
          <Toggle v-model="form.sticky_routing_enabled" />
        </div>

        <!-- CCH Signing -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.gatewayForwarding.cchSigning") }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.gatewayForwarding.cchSigningHint") }}
            </p>
          </div>
          <Toggle v-model="form.enable_cch_signing" />
        </div>

        <!-- Claude OAuth System Prompt Injection -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t(
                  "admin.settings.gatewayForwarding.claudeOAuthSystemPromptInjection",
                )
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.claudeOAuthSystemPromptInjectionHint",
                )
              }}
            </p>
          </div>
          <Toggle
            v-model="form.enable_claude_oauth_system_prompt_injection"
          />
        </div>

        <div>
          <label
            class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{
              t(
                "admin.settings.gatewayForwarding.claudeOAuthSystemPromptBlocks",
              )
            }}
          </label>
          <div class="space-y-3">
            <div
              v-for="(block, index) in claudeOAuthSystemPromptBlocks"
              :key="block.id"
              class="rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800/60"
            >
              <div
                :class="[
                  'flex flex-wrap items-center justify-between gap-3',
                  block.expanded && 'mb-3',
                ]"
              >
                <div class="min-w-0">
                  <div
                    class="text-sm font-medium text-gray-900 dark:text-white"
                  >
                    {{
                      t(
                        "admin.settings.gatewayForwarding.systemBlockTitle",
                        { index: index + 1 },
                      )
                    }}
                  </div>
                  <div
                    class="mt-0.5 text-xs text-gray-500 dark:text-gray-400"
                  >
                    {{ getClaudeOAuthPresetLabel(block.preset) }}
                  </div>
                </div>
                <div class="flex items-center gap-2">
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm px-2"
                    :title="
                      block.expanded
                        ? t(
                            'admin.settings.gatewayForwarding.systemBlockHide',
                          )
                        : t(
                            'admin.settings.gatewayForwarding.systemBlockShow',
                          )
                    "
                    :aria-label="
                      block.expanded
                        ? t(
                            'admin.settings.gatewayForwarding.systemBlockHide',
                          )
                        : t(
                            'admin.settings.gatewayForwarding.systemBlockShow',
                          )
                    "
                    @click="toggleClaudeOAuthSystemPromptBlock(index)"
                  >
                    <Icon
                      :name="block.expanded ? 'eyeOff' : 'eye'"
                      size="xs"
                    />
                  </button>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm px-2"
                    :disabled="index === 0"
                    @click="moveClaudeOAuthSystemPromptBlock(index, -1)"
                  >
                    <Icon name="arrowUp" size="xs" />
                  </button>
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm px-2"
                    :disabled="
                      index === claudeOAuthSystemPromptBlocks.length - 1
                    "
                    @click="moveClaudeOAuthSystemPromptBlock(index, 1)"
                  >
                    <Icon name="arrowDown" size="xs" />
                  </button>
                  <Toggle v-model="block.enabled" />
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm px-2 text-red-600 hover:text-red-700 dark:text-red-400"
                    @click="removeClaudeOAuthSystemPromptBlock(index)"
                  >
                    <Icon name="trash" size="xs" />
                  </button>
                </div>
              </div>

              <div v-show="block.expanded">
                <div class="grid gap-3 md:grid-cols-2">
                  <div>
                    <label
                      class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300"
                    >
                      {{
                        t(
                          "admin.settings.gatewayForwarding.systemBlockPreset",
                        )
                      }}
                    </label>
                    <Select
                      v-model="block.preset"
                      :options="claudeOAuthSystemPromptPresetOptions"
                      @change="
                        (value) =>
                          applyClaudeOAuthSystemPromptPreset(index, value)
                      "
                    />
                  </div>
                  <div>
                    <label
                      class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300"
                    >
                      {{
                        t(
                          "admin.settings.gatewayForwarding.systemBlockType",
                        )
                      }}
                    </label>
                    <Select
                      v-model="block.type"
                      :options="claudeOAuthSystemPromptBlockTypeOptions"
                    />
                  </div>
                </div>

                <div class="mt-3">
                  <label
                    class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300"
                  >
                    {{ t("admin.settings.gatewayForwarding.systemBlockText") }}
                  </label>
                  <textarea
                    v-model="block.text"
                    rows="6"
                    class="input w-full resize-y font-mono text-xs leading-5"
                    @input="markClaudeOAuthSystemPromptBlockCustom(block)"
                  />
                </div>

                <div
                  class="mt-3 grid gap-3 md:grid-cols-[minmax(0,1fr)_160px]"
                >
                  <div class="flex items-center justify-between gap-4">
                    <div>
                      <label
                        class="text-xs font-medium text-gray-600 dark:text-gray-300"
                      >
                        {{
                          t(
                            "admin.settings.gatewayForwarding.systemBlockCacheControl",
                          )
                        }}
                      </label>
                    </div>
                    <Toggle v-model="block.cacheControlEnabled" />
                  </div>
                  <div v-if="block.cacheControlEnabled">
                    <Select
                      v-model="block.cacheControlTTL"
                      :options="claudeOAuthSystemPromptCacheTTLOptions"
                    />
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div class="mt-3 flex flex-wrap gap-2">
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="addClaudeOAuthSystemPromptBlock"
            >
              <Icon name="plus" size="xs" />
              {{ t("admin.settings.gatewayForwarding.addSystemBlock") }}
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="resetClaudeOAuthSystemPromptBlocks"
            >
              <Icon name="refresh" size="xs" />
              {{
                t("admin.settings.gatewayForwarding.resetSystemBlocks")
              }}
            </button>
          </div>
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.gatewayForwarding.claudeOAuthSystemPromptBlocksHint",
              )
            }}
          </p>
        </div>

        <!-- Anthropic Cache TTL 1h Injection -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t(
                  "admin.settings.gatewayForwarding.anthropicCacheTTL1hInjection",
                )
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.anthropicCacheTTL1hInjectionHint",
                )
              }}
            </p>
          </div>
          <Toggle
            v-model="form.enable_anthropic_cache_ttl_1h_injection"
          />
        </div>

        <!-- messages cache_control rewrite -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t(
                  "admin.settings.gatewayForwarding.rewriteMessageCacheControl",
                )
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.rewriteMessageCacheControlHint",
                )
              }}
            </p>
          </div>
          <Toggle v-model="form.rewrite_message_cache_control" />
        </div>

        <!-- Anthropic Request Normalization -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t(
                  "admin.settings.gatewayForwarding.anthropicRequestNormalize",
                )
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.anthropicRequestNormalizeHint",
                )
              }}
            </p>
          </div>
          <Toggle
            v-model="form.tk_anthropic_request_normalize_enabled"
          />
        </div>

        <!-- Client Dateline Normalization -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{
                t(
                  "admin.settings.gatewayForwarding.clientDatelineNormalization",
                )
              }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                t(
                  "admin.settings.gatewayForwarding.clientDatelineNormalizationHint",
                )
              }}
            </p>
          </div>
          <Toggle
            v-model="form.enable_client_dateline_normalization"
          />
        </div>

        <!-- Antigravity UA Version -->
        <div>
          <label
            class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{
              t(
                "admin.settings.gatewayForwarding.antigravityUserAgentVersion",
              )
            }}
          </label>
          <input
            v-model="form.antigravity_user_agent_version"
            type="text"
            class="input max-w-xs font-mono text-sm"
            :placeholder="
              t(
                'admin.settings.gatewayForwarding.antigravityUserAgentVersionPlaceholder',
              )
            "
          />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.gatewayForwarding.antigravityUserAgentVersionHint",
              )
            }}
          </p>
        </div>

        <!-- OpenAI Codex UA -->
        <div>
          <label
            class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{
              t(
                "admin.settings.gatewayForwarding.openaiCodexUserAgent",
              )
            }}
          </label>
          <input
            v-model="form.openai_codex_user_agent"
            type="text"
            class="input w-full font-mono text-sm"
            :placeholder="
              t(
                'admin.settings.gatewayForwarding.openaiCodexUserAgentPlaceholder',
              )
            "
          />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{
              t(
                "admin.settings.gatewayForwarding.openaiCodexUserAgentHint",
              )
            }}
          </p>
        </div>

      </div>
    </div>

    <!-- Web Search Emulation -->
    <div class="card">
      <div
        class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
      >
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.webSearchEmulation.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.webSearchEmulation.description") }}
        </p>
      </div>
      <div class="space-y-5 p-6">
        <!-- Global Toggle -->
        <div class="flex items-center justify-between">
          <div>
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.webSearchEmulation.enabled") }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.webSearchEmulation.enabledHint") }}
            </p>
          </div>
          <Toggle v-model="webSearchConfig.enabled" />
        </div>

        <!-- Providers -->
        <div v-if="webSearchConfig.enabled" class="space-y-4">
          <div class="flex items-center justify-between">
            <label
              class="text-sm font-medium text-gray-700 dark:text-gray-300"
            >
              {{ t("admin.settings.webSearchEmulation.providers") }}
            </label>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              @click="addWebSearchProvider"
            >
              {{ t("admin.settings.webSearchEmulation.addProvider") }}
            </button>
          </div>

          <div
            v-if="webSearchConfig.providers.length === 0"
            class="rounded-lg border border-dashed border-gray-300 p-4 text-center text-sm text-gray-400 dark:border-dark-600"
          >
            {{ t("admin.settings.webSearchEmulation.noProviders") }}
          </div>

          <div
            v-for="(provider, pIdx) in webSearchConfig.providers"
            :key="pIdx"
            class="rounded-lg border border-gray-200 dark:border-dark-600"
          >
            <!-- Collapsible header -->
            <div
              class="flex cursor-pointer items-center justify-between px-4 py-3"
              @click="toggleProviderExpand(pIdx)"
            >
              <div class="flex items-center gap-3">
                <svg
                  class="h-4 w-4 text-gray-400 transition-transform"
                  :class="{ 'rotate-90': expandedProviders[pIdx] }"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M9 5l7 7-7 7"
                  />
                </svg>
                <Select
                  v-model="provider.type"
                  :options="[
                    { value: 'brave', label: 'Brave Search' },
                    { value: 'tavily', label: 'Tavily' },
                  ]"
                  class="w-36"
                  @click.stop
                />
                <!-- Quota summary (always visible) -->
                <span class="text-xs text-gray-400">
                  {{ provider.quota_used ?? 0 }} /
                  {{
                    provider.quota_limit != null &&
                    provider.quota_limit > 0
                      ? provider.quota_limit
                      : "∞"
                  }}
                </span>
                <span
                  v-if="
                    !expandedProviders[pIdx] &&
                    provider.api_key_configured
                  "
                  class="text-xs text-green-500"
                >
                  {{
                    t(
                      "admin.settings.webSearchEmulation.apiKeyConfigured",
                    )
                  }}
                </span>
              </div>
              <button
                type="button"
                class="text-red-500 hover:text-red-700 text-xs"
                @click.stop="removeWebSearchProvider(pIdx)"
              >
                {{
                  t("admin.settings.webSearchEmulation.removeProvider")
                }}
              </button>
            </div>

            <!-- Expanded content -->
            <div
              v-if="expandedProviders[pIdx]"
              class="space-y-3 border-t border-gray-100 px-4 pb-4 pt-3 dark:border-dark-700"
            >
              <!-- API Key with inline show/copy -->
              <div>
                <label class="text-xs text-gray-500">{{
                  t("admin.settings.webSearchEmulation.apiKey")
                }}</label>
                <div class="relative">
                  <input
                    v-model="provider.api_key"
                    :type="apiKeyVisible[pIdx] ? 'text' : 'password'"
                    class="input w-full text-sm"
                    :class="
                      provider.api_key || provider.api_key_configured
                        ? 'pr-16'
                        : ''
                    "
                    :placeholder="
                      provider.api_key_configured
                        ? '••••••••'
                        : t(
                            'admin.settings.webSearchEmulation.apiKeyPlaceholder',
                          )
                    "
                  />
                  <div
                    v-if="provider.api_key || provider.api_key_configured"
                    class="absolute inset-y-0 right-0 flex items-center pr-1.5"
                  >
                    <button
                      type="button"
                      class="rounded p-1 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                      :title="
                        apiKeyVisible[pIdx]
                          ? t(
                              'admin.settings.webSearchEmulation.hideApiKey',
                            )
                          : t(
                              'admin.settings.webSearchEmulation.showApiKey',
                            )
                      "
                      @click="apiKeyVisible[pIdx] = !apiKeyVisible[pIdx]"
                    >
                      <svg
                        v-if="!apiKeyVisible[pIdx]"
                        class="h-4 w-4"
                        fill="none"
                        viewBox="0 0 24 24"
                        stroke="currentColor"
                      >
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          stroke-width="2"
                          d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
                        />
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          stroke-width="2"
                          d="M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z"
                        />
                      </svg>
                      <svg
                        v-else
                        class="h-4 w-4"
                        fill="none"
                        viewBox="0 0 24 24"
                        stroke="currentColor"
                      >
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          stroke-width="2"
                          d="M13.875 18.825A10.05 10.05 0 0112 19c-4.478 0-8.268-2.943-9.543-7a9.97 9.97 0 011.563-3.029m5.858.908a3 3 0 114.243 4.243M9.878 9.878l4.242 4.242M9.878 9.878L3 3m6.878 6.878L21 21"
                        />
                      </svg>
                    </button>
                    <button
                      type="button"
                      class="rounded p-1 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                      :class="{
                        'opacity-30 cursor-not-allowed':
                          !provider.api_key,
                      }"
                      :title="
                        t('admin.settings.webSearchEmulation.copyApiKey')
                      "
                      :disabled="!provider.api_key"
                      @click="copyApiKey(pIdx)"
                    >
                      <svg
                        class="h-4 w-4"
                        fill="none"
                        viewBox="0 0 24 24"
                        stroke="currentColor"
                      >
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          stroke-width="2"
                          d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                        />
                      </svg>
                    </button>
                  </div>
                </div>
              </div>

              <!-- Quota + Subscription in compact row -->
              <div class="grid grid-cols-2 gap-3">
                <div>
                  <label class="text-xs text-gray-500">{{
                    t("admin.settings.webSearchEmulation.quotaLimit")
                  }}</label>
                  <input
                    v-model="provider.quota_limit"
                    type="number"
                    min="1"
                    class="input text-sm"
                    :placeholder="'∞'"
                  />
                  <p class="mt-0.5 text-xs text-gray-400">
                    {{
                      t(
                        "admin.settings.webSearchEmulation.quotaLimitHint",
                      )
                    }}
                  </p>
                </div>
                <div>
                  <label class="text-xs text-gray-500">{{
                    t("admin.settings.webSearchEmulation.subscribedAt")
                  }}</label>
                  <input
                    :value="formatSubscribedAt(provider.subscribed_at)"
                    type="date"
                    class="input text-sm"
                    @input="
                      provider.subscribed_at = parseSubscribedAt(
                        ($event.target as HTMLInputElement).value,
                      )
                    "
                  />
                  <p class="mt-0.5 text-xs text-gray-400">
                    {{
                      t(
                        "admin.settings.webSearchEmulation.subscribedAtHint",
                      )
                    }}
                  </p>
                </div>
              </div>

              <!-- Usage display -->
              <div class="flex items-center gap-2">
                <span class="text-xs text-gray-500"
                  >{{
                    t("admin.settings.webSearchEmulation.quotaUsage")
                  }}:</span
                >
                <div
                  v-if="
                    provider.quota_limit != null &&
                    provider.quota_limit > 0
                  "
                  class="flex-1 rounded-full bg-gray-200 dark:bg-dark-600"
                  style="height: 6px"
                >
                  <div
                    class="h-full rounded-full transition-all"
                    :class="
                      quotaPercentage(provider) > 90
                        ? 'bg-red-500'
                        : quotaPercentage(provider) > 70
                          ? 'bg-yellow-500'
                          : 'bg-green-500'
                    "
                    :style="{
                      width:
                        Math.min(quotaPercentage(provider), 100) + '%',
                    }"
                  />
                </div>
                <div v-else class="flex-1" />
                <span class="text-xs text-gray-500"
                  >{{ provider.quota_used ?? 0 }} /
                  {{
                    provider.quota_limit != null &&
                    provider.quota_limit > 0
                      ? provider.quota_limit
                      : "∞"
                  }}</span
                >
                <button
                  v-if="(provider.quota_used ?? 0) > 0"
                  type="button"
                  class="text-xs text-primary-600 hover:text-primary-700"
                  @click="resetWebSearchUsage(pIdx)"
                >
                  {{ t("admin.settings.webSearchEmulation.resetUsage") }}
                </button>
              </div>

              <!-- Proxy + Test on same row -->
              <div class="flex items-end gap-3">
                <div class="flex-1">
                  <label class="text-xs text-gray-500">{{
                    t("admin.settings.webSearchEmulation.proxy")
                  }}</label>
                  <ProxySelector
                    v-model="provider.proxy_id"
                    :proxies="webSearchProxies"
                  />
                </div>
                <button
                  type="button"
                  class="btn btn-secondary btn-sm whitespace-nowrap"
                  @click="openTestDialog()"
                >
                  {{ t("admin.settings.webSearchEmulation.test") }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Web Search Test Dialog -->
    <div
      v-if="wsTestDialogOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
      @click.self="wsTestDialogOpen = false"
    >
      <div
        class="mx-4 w-full max-w-lg rounded-xl bg-white p-6 shadow-xl dark:bg-dark-800"
      >
        <h3
          class="mb-4 text-lg font-semibold text-gray-900 dark:text-white"
        >
          {{ t("admin.settings.webSearchEmulation.testResultTitle") }}
        </h3>
        <div class="flex items-center gap-2">
          <input
            v-model="wsTestQuery"
            type="text"
            class="input flex-1 text-sm"
            :placeholder="
              t('admin.settings.webSearchEmulation.testDefaultQuery')
            "
            @keyup.enter="testWebSearchProvider()"
          />
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="wsTestLoading"
            @click="testWebSearchProvider()"
          >
            {{
              wsTestLoading
                ? t("admin.settings.webSearchEmulation.testing")
                : t("admin.settings.webSearchEmulation.test")
            }}
          </button>
        </div>
        <!-- Test results -->
        <div
          v-if="wsTestResult"
          class="mt-4 max-h-80 overflow-y-auto rounded-lg bg-gray-50 p-4 dark:bg-dark-700"
        >
          <p
            class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300"
          >
            {{
              t("admin.settings.webSearchEmulation.testResultProvider")
            }}: {{ wsTestResult.provider }}
          </p>
          <div
            v-if="wsTestResult.results.length === 0"
            class="text-sm text-gray-400"
          >
            {{ t("admin.settings.webSearchEmulation.testNoResults") }}
          </div>
          <div
            v-for="(r, rIdx) in wsTestResult.results"
            :key="rIdx"
            class="mt-2 border-t border-gray-200 pt-2 first:mt-0 first:border-0 first:pt-0 dark:border-dark-600"
          >
            <a
              :href="r.url"
              target="_blank"
              class="text-sm font-medium text-blue-600 hover:underline dark:text-blue-400"
              >{{ r.title }}</a
            >
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ r.snippet }}
            </p>
          </div>
        </div>
        <div class="mt-4 flex justify-end">
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            @click="wsTestDialogOpen = false"
          >
            {{ t("common.close") }}
          </button>
        </div>
      </div>
    </div>

    <!-- Usage Records Settings -->
    <div class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('admin.settings.usageRecords.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.usageRecords.description') }}
        </p>
      </div>
      <div class="space-y-4 p-6">
        <!-- User error requests visibility -->
        <div class="flex items-center justify-between">
          <div>
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.user_error_view.label') }}
            </label>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.user_error_view.description') }}
            </p>
          </div>
          <label class="toggle">
            <input v-model="form.allow_user_view_error_requests" type="checkbox" />
            <span class="toggle-slider"></span>
          </label>
        </div>
      </div>
    </div>
  </div>
</template>
