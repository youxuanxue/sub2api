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
 *   1. The model is chosen from the key's LIVE servable menu
 *      (GET /me/pricing-catalog), never typed. Retired / bare / unsupported
 *      names simply do not appear. The chosen id is injected into every
 *      snippet, so all client tabs stay in lock-step with a real model.
 *   2. A "Test key" action fires the user's own key against the gateway with a
 *      canonical-correct body and surfaces the verbatim 200/4xx inline — the
 *      same response they would otherwise only see after wiring a client and
 *      reading server logs.
 *
 * This composable owns data + behaviour; UseKeyModal.vue stays template + wiring
 * (TokenKey upstream-isolation pattern, CLAUDE.md §5).
 */

import { computed, ref, type Ref } from 'vue'
import { getMePricingCatalog, type MePricingModel } from '@/api/me-pricing'
import type { GroupPlatform } from '@/types'

/**
 * A snippet "flavor" is the single-model protocol a given client tab speaks.
 * antigravity hosts two (claude + gemini), so the flavor — not the platform —
 * is what scopes the model picker and the test request.
 */
export type UseKeyFlavor = 'anthropic' | 'openai' | 'gemini'

export interface UseKeyServableModel {
  id: string
  capabilities: string[]
  contextWindow?: number
  maxOutput?: number
}

export type TestStatus = 'idle' | 'running' | 'ok' | 'error'

export interface TestState {
  status: TestStatus
  httpStatus?: number
  latencyMs?: number
  /** verbatim upstream/gateway message on failure (the actionable signal) */
  message?: string
  /** true when the check was key-validity only (CC-only groups) */
  keyOnly?: boolean
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

interface UseTkUseKeyArgs {
  apiKeyId: Ref<number | null | undefined>
  apiKey: Ref<string>
  platform: Ref<GroupPlatform | null>
  /** anthropic groups gated to claude-cli / /v1/messages only */
  claudeCodeOnly: Ref<boolean | undefined>
  /** stripped gateway root, e.g. https://api.tokenkey.dev (no /v1) */
  baseRoot: Ref<string>
}

export function useTkUseKey(args: UseTkUseKeyArgs) {
  const servableModels = ref<UseKeyServableModel[]>([])
  const modelsLoading = ref(false)
  /** chosen model id per flavor; persists across tab switches within a session */
  const selectedByFlavor = ref<Record<UseKeyFlavor, string>>({
    anthropic: '',
    openai: '',
    gemini: '',
  })
  const testState = ref<TestState>({ status: 'idle' })
  let testController: AbortController | null = null

  async function loadModels(): Promise<void> {
    const id = args.apiKeyId.value
    servableModels.value = []
    selectedByFlavor.value = { anthropic: '', openai: '', gemini: '' }
    if (id == null) return
    modelsLoading.value = true
    try {
      const res = await getMePricingCatalog({ apiKeyId: id })
      servableModels.value = (res.models ?? []).map((m: MePricingModel) => ({
        id: m.model_id,
        capabilities: m.capabilities ?? [],
        contextWindow: m.context_window,
        maxOutput: m.max_output_tokens,
      }))
    } catch {
      // Load failure leaves servableModels empty; the modal then shows its
      // "couldn't load — type manually" hint and snippets use the fallback id.
      servableModels.value = []
    } finally {
      modelsLoading.value = false
    }
  }

  function modelsForFlavor(flavor: UseKeyFlavor): UseKeyServableModel[] {
    return servableModels.value.filter((m) => flavorOfModel(m.id) === flavor)
  }

  /** Currently effective model for a flavor: explicit pick → first servable of
   * that flavor → hardcoded fallback. Never empty, so snippets always render. */
  function effectiveModel(flavor: UseKeyFlavor): string {
    const picked = selectedByFlavor.value[flavor]
    if (picked) return picked
    const first = modelsForFlavor(flavor)[0]
    return first?.id ?? FLAVOR_DEFAULT_MODEL[flavor]
  }

  function setModel(flavor: UseKeyFlavor, id: string): void {
    selectedByFlavor.value = { ...selectedByFlavor.value, [flavor]: id }
    // a fresh model choice invalidates a prior test verdict
    if (testState.value.status !== 'idle') testState.value = { status: 'idle' }
  }

  const isClaudeCodeOnly = computed(
    () => args.platform.value === 'anthropic' && args.claudeCodeOnly.value === true,
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
   */
  async function runTest(flavor: UseKeyFlavor): Promise<void> {
    cancelTest()
    const root = stripTrailingSlashes(args.baseRoot.value)
    const key = args.apiKey.value
    const model = effectiveModel(flavor)
    const ctrl = new AbortController()
    testController = ctrl
    const timer = setTimeout(() => ctrl.abort(), 15_000)
    testState.value = { status: 'running' }
    const t0 = (typeof performance !== 'undefined' ? performance.now() : 0)

    const keyOnly = isClaudeCodeOnly.value
    let url: string
    let init: RequestInit

    if (keyOnly) {
      url = `${root}/v1/models`
      init = { method: 'GET', headers: { Authorization: `Bearer ${key}`, Accept: 'application/json' }, signal: ctrl.signal }
    } else if (flavor === 'anthropic') {
      const isAntigravity = args.platform.value === 'antigravity'
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
    } else if (flavor === 'gemini') {
      const isAntigravity = args.platform.value === 'antigravity'
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
      init = {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ model, max_tokens: 16, messages: [{ role: 'user', content: 'ping' }], stream: false }),
        signal: ctrl.signal,
      }
    }

    try {
      const res = await fetch(url, init)
      const latencyMs = Math.max(0, Math.round((typeof performance !== 'undefined' ? performance.now() : 0) - t0))
      const text = await res.text().catch(() => '')
      if (res.ok) {
        testState.value = { status: 'ok', httpStatus: res.status, latencyMs, keyOnly }
      } else {
        testState.value = { status: 'error', httpStatus: res.status, latencyMs, message: extractMessage(text) || `HTTP ${res.status}` }
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

  return {
    servableModels,
    modelsLoading,
    testState,
    isClaudeCodeOnly,
    loadModels,
    modelsForFlavor,
    effectiveModel,
    setModel,
    runTest,
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
