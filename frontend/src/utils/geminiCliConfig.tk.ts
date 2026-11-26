import { isGeminiNativeImageModel } from '@/constants/playgroundMedia.tk'
import type { UseKeyServableModel } from '@/composables/useTkUseKey'

export type GeminiCliShellTab = 'unix' | 'cmd' | 'powershell' | 'windows'

export interface GeminiCliFileConfig {
  path: string
  content: string
  highlighted?: string
  hint?: string
}

export interface GeminiCliHints {
  envHint?: string
  settingsHint?: string
}

export interface GeminiCliHighlighters {
  keyword: (value: string) => string
  variable: (value: string) => string
  operator: (value: string) => string
  string: (value: string) => string
}

const AGY_BUILTIN_MODEL_LABELS: Record<string, string> = {
  'gemini-4-neon': 'Gemini 4 Neon (Medium)',
  'gemini-4-carbon': 'Gemini 4 Carbon (High)',
  'gemini-4-hydrogen': 'Gemini 4 Hydrogen (Medium)',
  'gemini-4-argon': 'Gemini 4 Argon (High)',
  'gemini-3.8-flash': 'Gemini 3.8 Flash (High)',
  'gemini-3.1-pro-preview': 'Gemini 3.1 Pro (High)',
}

function isChatGeminiCliModel(id: string): boolean {
  const lower = id.trim().toLowerCase()
  if (!lower) return false
  if (isGeminiNativeImageModel(lower)) return false
  if (lower.includes('embedding') || lower.includes('imagen') || lower.startsWith('veo')) {
    return false
  }
  return lower.startsWith('gemini-')
}

/**
 * Collect chat-capable Gemini models for CLI `/model` registration.
 * Always places `selectedModel` first (preserving preview `YOUR_MODEL_ID` too).
 */
export function selectGeminiCliModels(
  selectedModel: string,
  servableModels: readonly UseKeyServableModel[] = [],
): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  const push = (id: string) => {
    const trimmed = id.trim()
    if (!trimmed || seen.has(trimmed)) return
    seen.add(trimmed)
    result.push(trimmed)
  }

  if (selectedModel.trim()) {
    push(selectedModel)
  }
  for (const entry of servableModels) {
    if (isChatGeminiCliModel(entry.id)) {
      push(entry.id)
    }
  }
  return result
}

function inferGeminiCliTier(modelId: string): 'flash-lite' | 'flash' | 'pro' {
  const lower = modelId.toLowerCase()
  if (lower.includes('flash-lite')) return 'flash-lite'
  if (lower.includes('flash')) return 'flash'
  return 'pro'
}

function inferGeminiCliFamily(modelId: string): string {
  const lower = modelId.toLowerCase()
  if (lower.startsWith('gemini-4')) return 'gemini-4'
  if (lower.startsWith('gemini-2.5')) return 'gemini-2.5'
  return 'gemini-3'
}

/**
 * Build `~/.gemini/settings.json` for Gemini CLI (`@google/gemini-cli`):
 * - `security.auth.selectedType: "gemini-api-key"` is required so Gemini CLI
 *   neither falls into the invalid `gateway` auth branch when
 *   `GOOGLE_GEMINI_BASE_URL` is set nor stays stuck on a previous Google OAuth
 *   (`oauth-personal`) login.
 * - `experimental.dynamicModelConfiguration: true` + `modelConfigs.modelDefinitions`
 *   with `isVisible: true` and `isPreview: false` expose TokenKey's `gemini*`
 *   models in `/model` -> `Manual` and `gemini models`.
 * - `modelConfigs.modelIdResolutions` with `contexts: []` clears built-in
 *   conditional downgrades (e.g. `gemini-3.8-flash` -> `gemini-3.5-flash`).
 */
export function buildGeminiCliSettingsJson(
  selectedModel: string,
  models: readonly string[],
): string {
  const modelList = models.length > 0 ? models : (selectedModel ? [selectedModel] : [])
  const modelDefinitions: Record<string, unknown> = {}
  const modelIdResolutions: Record<string, unknown> = {}

  for (const id of modelList) {
    modelDefinitions[id] = {
      displayName: id,
      tier: inferGeminiCliTier(id),
      family: inferGeminiCliFamily(id),
      isPreview: false,
      isVisible: true,
      features: {
        thinking: true,
        multimodalToolUse: true,
      },
    }
    modelIdResolutions[id] = {
      default: id,
      contexts: [],
    }
  }

  return JSON.stringify({
    security: {
      auth: {
        selectedType: 'gemini-api-key',
      },
    },
    model: {
      name: selectedModel,
    },
    experimental: {
      dynamicModelConfiguration: true,
    },
    modelConfigs: {
      modelDefinitions,
      modelIdResolutions,
    },
  }, null, 2)
}

export function buildGeminiCliFiles(params: {
  baseUrl: string
  apiKey: string
  model: string
  servableModels?: readonly UseKeyServableModel[]
  os: GeminiCliShellTab
  hints?: GeminiCliHints
}): GeminiCliFileConfig[] {
  const cleanBaseUrl = params.baseUrl.replace(/\/+$/, '')
  const isWindows = params.os !== 'unix'
  const sep = isWindows ? '\\' : '/'
  const root = isWindows ? '%USERPROFILE%\\.gemini' : '~/.gemini'
  const models = selectGeminiCliModels(params.model, params.servableModels)

  return [
    {
      path: `${root}${sep}.env`,
      content: [
        `GOOGLE_GEMINI_BASE_URL="${cleanBaseUrl}"`,
        `GEMINI_API_KEY="${params.apiKey}"`,
        `GEMINI_MODEL="${params.model}"`,
      ].join('\n'),
      hint: params.hints?.envHint,
    },
    {
      path: `${root}${sep}settings.json`,
      content: buildGeminiCliSettingsJson(params.model, models),
      hint: params.hints?.settingsHint,
    },
  ]
}

/**
 * Build `~/.gemini/antigravity-cli/settings.json` for Antigravity CLI (`agy`):
 * - `modelProvider: "gemini"` is mandatory; setting `GEMINI_API_KEY` alone is
 *   ignored by `agy`.
 * - `customModelsConfig.customModels` registers TokenKey's `gemini*` models in
 *   `/model` and `agy models`. Empirically on `agy` v1.5.16, bare model IDs in
 *   `modelName` route cleanly to `{GOOGLE_GEMINI_BASE_URL}/v1beta/models/<id>`,
 *   whereas `gemini-api://<host>/models/<id>` corrupts the request path.
 * - `toolFormatterType: "TOOL_FORMATTER_TYPE_NONE"` keeps native Gemini
 *   function calling enabled.
 * - Only set top-level `model` when `selectedModel` maps to a built-in `agy`
 *   label; pointing `settings.json` `model` at a custom BYOM entry triggers
 *   `MODEL_GOOGLE_GEMINI_INTERNAL_BYOM` errors in `agy -p` headless mode.
 */
export function buildAntigravityCliSettingsJson(
  selectedModel: string,
  models: readonly string[],
): string {
  const modelList = models.length > 0 ? models : (selectedModel ? [selectedModel] : [])
  const customModels: Record<string, unknown> = {}

  for (const id of modelList) {
    customModels[id] = {
      modelName: id,
      label: id,
      toolFormatterType: 'TOOL_FORMATTER_TYPE_NONE',
    }
  }

  const settings: Record<string, unknown> = {
    modelProvider: 'gemini',
  }
  const builtinLabel = AGY_BUILTIN_MODEL_LABELS[selectedModel]
  if (builtinLabel) {
    settings.model = builtinLabel
  }
  settings.customModelsConfig = { customModels }

  return JSON.stringify(settings, null, 2)
}

export function buildAntigravityCliFiles(params: {
  baseUrl: string
  apiKey: string
  model: string
  servableModels?: readonly UseKeyServableModel[]
  os: GeminiCliShellTab
  hints?: GeminiCliHints
  highlight?: GeminiCliHighlighters
}): GeminiCliFileConfig[] {
  const cleanBaseUrl = params.baseUrl.replace(/\/+$/, '')
  const isWindows = params.os !== 'unix'
  const settingsPath = isWindows
    ? '%USERPROFILE%\\.gemini\\antigravity-cli\\settings.json'
    : '~/.gemini/antigravity-cli/settings.json'
  const models = selectGeminiCliModels(params.model, params.servableModels)
  const h = params.highlight

  let envPath: string
  let envContent: string
  let envHighlighted: string | undefined

  switch (params.os) {
    case 'cmd':
      envPath = 'Command Prompt'
      envContent = [
        `set GOOGLE_GEMINI_BASE_URL=${cleanBaseUrl}`,
        `set GEMINI_API_KEY=${params.apiKey}`,
      ].join('\n')
      if (h) {
        envHighlighted = [
          `${h.keyword('set')} ${h.variable('GOOGLE_GEMINI_BASE_URL')}${h.operator('=')}${h.string(cleanBaseUrl)}`,
          `${h.keyword('set')} ${h.variable('GEMINI_API_KEY')}${h.operator('=')}${h.string(params.apiKey)}`,
        ].join('\n')
      }
      break
    case 'powershell':
    case 'windows':
      envPath = 'PowerShell'
      envContent = [
        `$env:GOOGLE_GEMINI_BASE_URL="${cleanBaseUrl}"`,
        `$env:GEMINI_API_KEY="${params.apiKey}"`,
      ].join('\n')
      if (h) {
        envHighlighted = [
          `${h.keyword('$env:')}${h.variable('GOOGLE_GEMINI_BASE_URL')}${h.operator('=')}${h.string(`"${cleanBaseUrl}"`)}`,
          `${h.keyword('$env:')}${h.variable('GEMINI_API_KEY')}${h.operator('=')}${h.string(`"${params.apiKey}"`)}`,
        ].join('\n')
      }
      break
    case 'unix':
    default:
      envPath = 'Terminal'
      envContent = [
        `export GOOGLE_GEMINI_BASE_URL="${cleanBaseUrl}"`,
        `export GEMINI_API_KEY="${params.apiKey}"`,
      ].join('\n')
      if (h) {
        envHighlighted = [
          `${h.keyword('export')} ${h.variable('GOOGLE_GEMINI_BASE_URL')}${h.operator('=')}${h.string(`"${cleanBaseUrl}"`)}`,
          `${h.keyword('export')} ${h.variable('GEMINI_API_KEY')}${h.operator('=')}${h.string(`"${params.apiKey}"`)}`,
        ].join('\n')
      }
      break
  }

  return [
    {
      path: envPath,
      content: envContent,
      highlighted: envHighlighted,
      hint: params.hints?.envHint,
    },
    {
      path: settingsPath,
      content: buildAntigravityCliSettingsJson(params.model, models),
      hint: params.hints?.settingsHint,
    },
  ]
}
