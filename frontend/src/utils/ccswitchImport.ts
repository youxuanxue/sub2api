import type { GroupPlatform } from '@/types'
import { PLATFORM_GEMINI } from '@/constants/gatewayPlatforms'

export const OPENAI_CC_SWITCH_CODEX_MODEL = 'gpt-5.5'
export const GROK_CC_SWITCH_MODEL = 'grok-4.5'

/** CC Switch `app` parameter — matches farion1231/cc-switch AppType variants TokenKey supports. */
export type CcSwitchApp = 'claude' | 'codex' | 'gemini' | 'grokbuild' | 'opencode'

/** Legacy Antigravity disambiguation (Claude Code vs Gemini CLI endpoint). */
export type CcSwitchClientType = 'claude' | 'gemini'

export interface CcSwitchImportConfig {
  app: string
  endpoint: string
  model?: string
}

export interface CcSwitchImportResolveInput {
  platform?: GroupPlatform | null
  ccsApp: CcSwitchApp
  baseUrl: string
}

export interface CcSwitchImportDeeplinkInput {
  baseUrl: string
  platform?: GroupPlatform | null
  ccsApp: CcSwitchApp
  providerName: string
  apiKey: string
  usageScript: string
}

function withV1Endpoint(baseUrl: string): string {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '')
  return normalizedBaseUrl.endsWith('/v1') ? normalizedBaseUrl : `${normalizedBaseUrl}/v1`
}

/** Map a Key group platform to the default CC Switch app when no tool is selected. */
export function defaultCcsAppForPlatform(
  platform: GroupPlatform | undefined | null,
  clientType: CcSwitchClientType = 'claude',
): CcSwitchApp {
  switch (platform) {
    case 'openai':
    case 'newapi':
      return 'codex'
    case 'gemini':
      return 'gemini'
    case 'grok':
      return 'grokbuild'
    case 'antigravity':
      return clientType === PLATFORM_GEMINI ? 'gemini' : 'claude'
    default:
      return 'claude'
  }
}

export function resolveCcSwitchImportConfig(input: CcSwitchImportResolveInput): CcSwitchImportConfig {
  const { platform, ccsApp, baseUrl } = input

  if (platform === 'antigravity') {
    return {
      app: ccsApp === 'gemini' ? 'gemini' : 'claude',
      endpoint: `${baseUrl}/antigravity`,
    }
  }

  switch (ccsApp) {
    case 'codex':
      return {
        app: 'codex',
        endpoint: baseUrl,
        model: OPENAI_CC_SWITCH_CODEX_MODEL,
      }
    case 'grokbuild':
      return {
        app: 'grokbuild',
        endpoint: withV1Endpoint(baseUrl),
        model: GROK_CC_SWITCH_MODEL,
      }
    case 'gemini':
      return {
        app: 'gemini',
        endpoint: baseUrl,
      }
    case 'opencode':
      return {
        app: 'opencode',
        endpoint: baseUrl,
      }
    case 'claude':
    default:
      return {
        app: 'claude',
        endpoint: baseUrl,
      }
  }
}

/** @deprecated Prefer explicit `ccsApp`; kept for Antigravity clientType disambiguation in Keys view. */
export function resolveCcSwitchImportConfigFromPlatform(
  platform: GroupPlatform | undefined | null,
  clientType: CcSwitchClientType,
  baseUrl: string,
): CcSwitchImportConfig {
  return resolveCcSwitchImportConfig({
    platform,
    ccsApp: defaultCcsAppForPlatform(platform, clientType),
    baseUrl,
  })
}

export function buildCcSwitchImportDeeplink(input: CcSwitchImportDeeplinkInput): string {
  const config = resolveCcSwitchImportConfig({
    platform: input.platform,
    ccsApp: input.ccsApp,
    baseUrl: input.baseUrl,
  })
  const entries: [string, string][] = [
    ['resource', 'provider'],
    ['app', config.app],
    ['name', input.providerName],
    ['homepage', input.baseUrl],
    ['endpoint', config.endpoint],
    ['apiKey', input.apiKey],
    ['configFormat', 'json'],
    ['usageEnabled', 'true'],
    ['usageScript', btoa(input.usageScript)],
    ['usageAutoInterval', '30'],
  ]

  if (config.model) {
    entries.splice(2, 0, ['model', config.model])
  }

  return `ccswitch://v1/import?${new URLSearchParams(entries).toString()}`
}

export const CC_SWITCH_USAGE_SCRIPT = `({
  request: {
    url: "{{baseUrl}}/v1/usage",
    method: "GET",
    headers: { "Authorization": "Bearer {{apiKey}}" }
  },
  extractor: function(response) {
    const remaining = response?.remaining ?? response?.quota?.remaining ?? response?.balance;
    const unit = response?.unit ?? response?.quota?.unit ?? "USD";
    return {
      isValid: response?.is_active ?? response?.isValid ?? true,
      remaining,
      unit
    };
  }
})`
