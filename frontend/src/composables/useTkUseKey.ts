/**
 * TokenKey: backing logic for the redesigned "Use API Key" modal
 * (components/keys/UseKeyModal.vue).
 *
 * Built from a 7-day prod ops_error_logs study of real client-parameter
 * failures. The top fixable buckets — wrong/retired/bare model names (~950/wk),
 * invalid API key / auth header (~2800/wk), CC-only groups hit by non-cc
 * clients (~414/wk) — are all eliminated here by making the error-prone fields
 * impossible to mistype:
 *
 *   1. The model is chosen from the key's live servable menu: the capability
 *      SSOT for automatic routing, or the bound-group pricing catalog for a
 *      direct key. Retired / bare / unsupported names simply do not appear.
 *      The chosen id is injected into every snippet, so all client tabs stay
 *      in lock-step with a real model.
 *   2. A "Test key" action fires the user's own key against the gateway with a
 *      canonical-correct body and surfaces the verbatim 200/4xx inline — the
 *      same response they would otherwise only see after wiring a client and
 *      reading server logs.
 *
 * This composable owns data + behaviour; UseKeyModal.vue stays template + wiring
 * (TokenKey upstream-isolation pattern, CLAUDE.md §5).
 */

import { computed, ref, type Ref } from 'vue'
import { resolveBrowserGatewayFetchBaseUrl, gatewayWarmupConnection } from '@/api/playground'
import { getMePricingCatalog, type MePricingModel } from '@/api/me-pricing'
import {
  getAPIKeyCapabilities,
  type APIKeyCapabilityModel,
  type APIKeyCapabilityProtocol,
} from '@/api/api-key-capabilities'
import type { GroupPlatform, KeyRoutingMode } from '@/types'
import { PLATFORM_ANTHROPIC, PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI } from '@/constants/gatewayPlatforms'

/**
 * A snippet "flavor" is the single-model protocol a given client tab speaks.
 * antigravity hosts two (claude + gemini), so the flavor — not the platform —
 * is what scopes the model picker and the test request.
 */
export type UseKeyFlavor = 'anthropic' | 'openai' | 'gemini'
export type UseKeyDiscoveryProtocol = Extract<
  APIKeyCapabilityProtocol,
  'anthropic' | 'openai' | 'gemini' | 'codex'
>

export interface UseKeyServableModel {
  id: string
  capabilities: string[]
  protocols?: APIKeyCapabilityProtocol[]
  modalities?: string[]
  contextWindow?: number
  maxOutput?: number
}

export type TestStatus = 'idle' | 'running' | 'ok' | 'error'

export interface TestState {
  status: TestStatus
  httpStatus?: number
  /** End-to-end wall time for the full probe sequence. */
  latencyMs?: number
  /** GET /v1/models — key validity and routing. */
  authLatencyMs?: number
  /** Model protocol probe (chat/messages/generateContent); omitted for key-only checks. */
  modelLatencyMs?: number
  /** verbatim upstream/gateway message on failure (the actionable signal) */
  message?: string
  reason?: 'missing_tool_call'
  /** true when the check was key-validity only (CC-only groups) */
  keyOnly?: boolean
  /** true when the response completed a forced, side-effect-free tool call probe */
  toolCall?: boolean
}

export interface RunTestOptions {
  requireToolCall?: boolean
  protocol?: UseKeyDiscoveryProtocol
}

/** Build human-readable latency fragments for Quickstart / Use Key test banners. */
export function formatProbeLatencyDetail(
  state: TestState,
  t: (key: string, params?: Record<string, unknown>) => string,
): string {
  const parts: string[] = []
  if (state.httpStatus) parts.push(String(state.httpStatus))
  if (state.authLatencyMs != null) {
    parts.push(t('quickstart.probeAuthMs', { ms: state.authLatencyMs }))
  }
  if (state.modelLatencyMs != null) {
    parts.push(t('quickstart.probeModelMs', { ms: state.modelLatencyMs }))
  }
  if (state.latencyMs != null && state.modelLatencyMs != null) {
    parts.push(t('quickstart.probeTotalMs', { ms: state.latencyMs }))
  } else if (state.latencyMs != null && state.authLatencyMs == null) {
    parts.push(`${state.latencyMs}ms`)
  }
  if (state.toolCall) parts.push(t('quickstart.toolCallOk'))
  else if (state.keyOnly) parts.push(t('keys.useKeyModal.testKeyValid'))
  else if (state.modelLatencyMs != null || state.keyOnly) parts.push(t('keys.useKeyModal.testModelOk'))
  return parts.filter(Boolean).join(' · ')
}

/** Per-flavor fallback when the live catalog is still loading or empty. Kept in
 * sync with the historical hardcoded defaults so behaviour never regresses. */
const FLAVOR_DEFAULT_MODEL: Record<UseKeyFlavor, string> = {
  anthropic: 'claude-opus-4-8',
  openai: 'gpt-5.5',
  gemini: 'gemini-2.5-flash',
}

/** Human labels for the capability strings LiteLLM metadata emits. Unknown
 * values are title-cased rather than dropped. */
const CAPABILITY_LABELS: Record<string, string> = {
  vision: '图像输入',
  tools: '工具调用',
  function_calling: '工具调用',
  thinking: '深度思考',
  reasoning: '深度思考',
  audio: '音频',
  json: 'JSON 模式',
  prompt_caching: '提示缓存',
}

export function capabilityLabel(cap: string): string {
  return CAPABILITY_LABELS[cap] ?? cap.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
}

function stripTrailingSlashes(u: string): string {
  return u.replace(/\/+$/, '')
}

function perfNow(): number {
  return typeof performance !== 'undefined' ? performance.now() : 0
}

function elapsedMs(since: number): number {
  return Math.max(0, Math.round(perfNow() - since))
}

interface GatewayAuthProbeResult {
  ok: boolean
  httpStatus: number
  latencyMs: number
  message?: string
}

async function probeGatewayAuth(
  root: string,
  key: string,
  signal: AbortSignal,
): Promise<GatewayAuthProbeResult> {
  const t0 = perfNow()
  const res = await fetch(`${root}/v1/models`, {
    method: 'GET',
    headers: { Authorization: `Bearer ${key}`, Accept: 'application/json' },
    signal,
  })
  const latencyMs = elapsedMs(t0)
  if (res.ok) {
    return { ok: true, httpStatus: res.status, latencyMs }
  }
  const text = await res.text().catch(() => '')
  return {
    ok: false,
    httpStatus: res.status,
    latencyMs,
    message: extractMessage(text) || `HTTP ${res.status}`,
  }
}

/** Classify a model id into a flavor by its wire-name shape. */
export function flavorOfModel(id: string): UseKeyFlavor {
  const lower = id.toLowerCase()
  if (lower.startsWith('claude') || lower.includes('claude')) return 'anthropic'
  if (lower.startsWith('gemini') || lower.includes('gemini') || lower.startsWith('imagen')) return 'gemini'
  return 'openai'
}

/**
 * The anthropic env var wants the 1M-window alias for opus-class models
 * (claude-opus-4-8 → claude-opus-4-8[1m]); the gateway collapses the [1m]
 * suffix back to the servable id while the separate context-1m beta header
 * activates the wide window. Non-opus picks pass through unchanged.
 */
export function anthropicEnvModel(id: string): string {
  if (/^claude-opus/i.test(id) && !/\[1m\]$/i.test(id)) return `${id}[1m]`
  return id
}

/**
 * Claude Code on OpenAI-compat messages dispatch uses the same trailing [1m]
 * alias for 1M-window GPT picks (gpt-5.5 → gpt-5.5[1m]). The gateway strips
 * the suffix before Codex forward (openai_compat_context_window_alias_tk.go).
 */
export function openaiCompatContextWindowEnvModel(id: string): string {
  if (/\[1m\]$/i.test(id)) return id
  const bare = id.replace(/-(low|medium|high|xhigh)$/i, '')
  if (/^gpt-5\.(?:4|5)$/i.test(bare)) return `${bare}[1m]`
  return id
}

export function claudeCodeEnvModel(
  id: string,
  opts: { openaiMessagesDispatch?: boolean } = {},
): string {
  if (opts.openaiMessagesDispatch) return openaiCompatContextWindowEnvModel(id)
  return anthropicEnvModel(id)
}

interface UseTkUseKeyArgs {
  apiKeyId: Ref<number | null | undefined>
  apiKey: Ref<string>
  platform: Ref<GroupPlatform | null>
  routingMode?: Ref<KeyRoutingMode | undefined>
  /** anthropic groups gated to claude-cli / /v1/messages only */
  claudeCodeOnly: Ref<boolean | undefined>
  /** stripped gateway root, e.g. https://api.tokenkey.dev (no /v1) */
  baseRoot: Ref<string>
}

function mapMePricingModels(models: MePricingModel[]): UseKeyServableModel[] {
  return models.map((m) => ({
    id: m.model_id,
    capabilities: m.capabilities ?? [],
    contextWindow: m.context_window,
    maxOutput: m.max_output_tokens,
  }))
}

function mapCapabilityModels(models: APIKeyCapabilityModel[]): UseKeyServableModel[] {
  return models.map((model) => ({
    id: model.id,
    capabilities: [],
    protocols: model.protocols,
    modalities: model.modalities,
  }))
}

export function useTkUseKey(args: UseTkUseKeyArgs) {
  const servableModels = ref<UseKeyServableModel[]>([])
  const modelsLoading = ref(false)
  const modelsLoaded = ref(false)
  const modelsError = ref('')
  /** chosen model id per flavor; persists across tab switches within a session */
  const selectedByFlavor = ref<Record<UseKeyFlavor, string>>({
    anthropic: '',
    openai: '',
    gemini: '',
  })
  const testState = ref<TestState>({ status: 'idle' })
  let testController: AbortController | null = null
  let modelLoadEpoch = 0

  async function loadModels(): Promise<void> {
    const epoch = ++modelLoadEpoch
    const id = args.apiKeyId.value
    servableModels.value = []
    modelsLoaded.value = false
    modelsError.value = ''
    selectedByFlavor.value = { anthropic: '', openai: '', gemini: '' }
    if (id == null) {
      modelsLoading.value = false
      return
    }
    modelsLoading.value = true
    try {
      let nextModels: UseKeyServableModel[]
      if (args.routingMode?.value === 'universal') {
        const capabilities = await getAPIKeyCapabilities(id)
        nextModels = mapCapabilityModels(capabilities.models ?? [])
      } else {
        const res = await getMePricingCatalog({ apiKeyId: id })
        nextModels = mapMePricingModels(res.models ?? [])
      }
      if (epoch !== modelLoadEpoch || id !== args.apiKeyId.value) return
      servableModels.value = nextModels
      modelsLoaded.value = true
    } catch (error) {
      if (epoch !== modelLoadEpoch || id !== args.apiKeyId.value) return
      servableModels.value = []
      modelsError.value = error instanceof Error ? error.message : String(error)
    } finally {
      if (epoch === modelLoadEpoch) modelsLoading.value = false
    }
  }

  function modelsForFlavor(
    flavor: UseKeyFlavor,
    protocol: UseKeyDiscoveryProtocol = flavor,
  ): UseKeyServableModel[] {
    return servableModels.value.filter((model) => {
      if (!model.protocols?.length) return flavorOfModel(model.id) === flavor
      return model.protocols.includes(protocol)
    })
  }

  /** Currently effective model for a flavor: explicit pick → first servable of
   * that flavor → hardcoded fallback. Never empty, so snippets always render. */
  function effectiveModel(
    flavor: UseKeyFlavor,
    protocol: UseKeyDiscoveryProtocol = flavor,
  ): string {
    const picked = selectedByFlavor.value[flavor]
    if (picked && modelsForFlavor(flavor, protocol).some((model) => model.id === picked)) return picked
    const first = modelsForFlavor(flavor, protocol)[0]
    return first?.id ?? FLAVOR_DEFAULT_MODEL[flavor]
  }

  function setModel(flavor: UseKeyFlavor, id: string): void {
    selectedByFlavor.value = { ...selectedByFlavor.value, [flavor]: id }
    // a fresh model choice invalidates a prior test verdict
    if (testState.value.status !== 'idle') testState.value = { status: 'idle' }
  }

  /** Pre-select a model from a deep link only when the current key can serve it. */
  function applyInitialModel(
    modelId: string | null | undefined,
    protocol?: UseKeyDiscoveryProtocol,
  ): UseKeyFlavor | null {
    const id = modelId?.trim()
    if (!id) return null
    const flavor = flavorOfModel(id)
    if (!modelsForFlavor(flavor, protocol ?? flavor).some((model) => model.id === id)) return null
    setModel(flavor, id)
    return flavor
  }

  function shouldWarnModelsEmpty(
    flavor: UseKeyFlavor,
    protocol: UseKeyDiscoveryProtocol = flavor,
  ): boolean {
    if (modelsError.value || !modelsLoaded.value) return false
    if (selectedByFlavor.value[flavor]) return false
    return modelsForFlavor(flavor, protocol).length === 0
  }

  const isClaudeCodeOnly = computed(
    () => args.platform.value === PLATFORM_ANTHROPIC && args.claudeCodeOnly.value === true,
  )

  function cancelTest(): void {
    testController?.abort()
    testController = null
    if (testState.value.status === 'running') testState.value = { status: 'idle' }
  }

  /**
   * Fire the user's key against the gateway. flavor decides the protocol/body;
   * a CC-only anthropic group can't be exercised from a browser (the gateway
   * requires a claude-cli User-Agent, which fetch is forbidden from setting),
   * so we fall back to a key-validity probe (GET /v1/models) and say so.
   *
   * Non key-only probes run auth (GET /v1/models) then a minimal model call so
   * Quickstart can show auth vs model latency separately.
   */
  async function runTest(flavor: UseKeyFlavor, options: RunTestOptions = {}): Promise<void> {
    cancelTest()
    const root = stripTrailingSlashes(resolveBrowserGatewayFetchBaseUrl(args.baseRoot.value))
    const key = args.apiKey.value
    const model = effectiveModel(flavor, options.protocol ?? flavor)
    const ctrl = new AbortController()
    testController = ctrl
    const timer = setTimeout(() => ctrl.abort(), 15_000)
    testState.value = { status: 'running' }
    const totalT0 = perfNow()

    const keyOnly = isClaudeCodeOnly.value

    try {
      const auth = await probeGatewayAuth(root, key, ctrl.signal)
      if (!auth.ok) {
        testState.value = {
          status: 'error',
          httpStatus: auth.httpStatus,
          authLatencyMs: auth.latencyMs,
          latencyMs: auth.latencyMs,
          message: auth.message,
        }
        return
      }

      if (keyOnly) {
        testState.value = {
          status: 'ok',
          httpStatus: auth.httpStatus,
          authLatencyMs: auth.latencyMs,
          latencyMs: auth.latencyMs,
          keyOnly: true,
        }
        return
      }

      let url: string
      let init: RequestInit

      if (flavor === PLATFORM_ANTHROPIC) {
        const isAntigravity = args.platform.value === PLATFORM_ANTIGRAVITY
        url = `${root}${isAntigravity ? '/antigravity' : ''}/v1/messages`
        init = {
          method: 'POST',
          headers: {
            'x-api-key': key,
            Authorization: `Bearer ${key}`,
            'anthropic-version': '2023-06-01',
            'Content-Type': 'application/json',
            Accept: 'application/json',
          },
          body: JSON.stringify({ model, max_tokens: 16, messages: [{ role: 'user', content: 'ping' }] }),
          signal: ctrl.signal,
        }
      } else if (flavor === PLATFORM_GEMINI) {
        const isAntigravity = args.platform.value === PLATFORM_ANTIGRAVITY
        const gBase = `${root}${isAntigravity ? '/antigravity' : ''}/v1beta`
        url = `${gBase}/models/${encodeURIComponent(model)}:generateContent`
        init = {
          method: 'POST',
          headers: { 'x-goog-api-key': key, 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ contents: [{ role: 'user', parts: [{ text: 'ping' }] }] }),
          signal: ctrl.signal,
        }
      } else {
        url = `${root}/v1/chat/completions`
        const toolName = 'tokenkey_quickstart_probe'
        const body: Record<string, unknown> = {
          model,
          max_tokens: 16,
          messages: [{
            role: 'user',
            content: options.requireToolCall ? `Call ${toolName} with value "ok".` : 'ping',
          }],
          stream: false,
        }
        if (options.requireToolCall) {
          body.tools = [{
            type: 'function',
            function: {
              name: toolName,
              description: 'Return a fixed probe value without side effects.',
              parameters: {
                type: 'object',
                properties: { value: { type: 'string' } },
                required: ['value'],
                additionalProperties: false,
              },
            },
          }]
          body.tool_choice = { type: 'function', function: { name: toolName } }
        }
        init = {
          method: 'POST',
          headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify(body),
          signal: ctrl.signal,
        }
      }

      const modelT0 = perfNow()
      const res = await fetch(url, init)
      const modelLatencyMs = elapsedMs(modelT0)
      const latencyMs = elapsedMs(totalT0)
      const text = await res.text().catch(() => '')
      if (res.ok) {
        if (options.requireToolCall && !hasToolCall(text, 'tokenkey_quickstart_probe')) {
          testState.value = {
            status: 'error',
            httpStatus: res.status,
            authLatencyMs: auth.latencyMs,
            modelLatencyMs,
            latencyMs,
            reason: 'missing_tool_call',
          }
        } else {
          testState.value = {
            status: 'ok',
            httpStatus: res.status,
            authLatencyMs: auth.latencyMs,
            modelLatencyMs,
            latencyMs,
            keyOnly: false,
            toolCall: options.requireToolCall,
          }
        }
      } else {
        testState.value = {
          status: 'error',
          httpStatus: res.status,
          authLatencyMs: auth.latencyMs,
          modelLatencyMs,
          latencyMs,
          message: extractMessage(text) || `HTTP ${res.status}`,
        }
      }
    } catch (e) {
      if (ctrl.signal.aborted) {
        testState.value = { status: 'idle' }
      } else {
        testState.value = { status: 'error', message: e instanceof Error ? e.message : String(e) }
      }
    } finally {
      clearTimeout(timer)
      if (testController === ctrl) testController = null
    }
  }

  async function warmupGateway(): Promise<void> {
    const key = args.apiKey.value.trim()
    if (!key) return
    const root = stripTrailingSlashes(resolveBrowserGatewayFetchBaseUrl(args.baseRoot.value))
    try {
      await gatewayWarmupConnection(key, root)
    } catch {
      /* connection warmup is best-effort */
    }
  }

  return {
    servableModels,
    modelsLoading,
    modelsLoaded,
    modelsError,
    testState,
    isClaudeCodeOnly,
    loadModels,
    modelsForFlavor,
    effectiveModel,
    setModel,
    applyInitialModel,
    shouldWarnModelsEmpty,
    runTest,
    warmupGateway,
  }
}

function hasToolCall(text: string, expectedName: string): boolean {
  try {
    const payload = JSON.parse(text) as {
      choices?: Array<{ message?: { tool_calls?: Array<{ function?: { name?: string } }> } }>
    }
    return payload.choices?.some((choice) =>
      choice.message?.tool_calls?.some((call) => call.function?.name === expectedName),
    ) === true
  } catch {
    return false
  }
}

/** Pull the most useful human string out of a gateway error body (OpenAI /
 * Anthropic / Gemini all wrap it differently). */
function extractMessage(text: string): string {
  if (!text) return ''
  try {
    const j = JSON.parse(text)
    const err = (j as { error?: unknown }).error
    if (typeof err === 'string') return err
    if (err && typeof err === 'object') {
      const m = (err as { message?: unknown }).message
      if (typeof m === 'string') return m
    }
    const m = (j as { message?: unknown }).message
    if (typeof m === 'string') return m
  } catch {
    /* plain text */
  }
  return text.slice(0, 300)
}
